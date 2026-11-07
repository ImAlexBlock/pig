package daemon

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/alexblock/pig/internal/agent"
	"github.com/alexblock/pig/internal/channel"
	"github.com/alexblock/pig/internal/compact"
	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/ipc"
	"github.com/alexblock/pig/internal/job"
	"github.com/alexblock/pig/internal/prompt"
	"github.com/alexblock/pig/internal/provider"
	"github.com/alexblock/pig/internal/skill"
	"github.com/alexblock/pig/internal/store"
	"github.com/alexblock/pig/internal/tool"
	"github.com/alexblock/pig/internal/tool/browser"
	"github.com/alexblock/pig/internal/tool/search"
)

// Options configures a daemon instance.
type Options struct {
	Config         *config.Config
	Paths          config.Paths
	Logger         *Logger
	RequireChannel bool
	NoChannels     bool
}

// Server is the single runtime that owns sessions, runs, jobs and channels.
type Server struct {
	cfg   *config.Config
	paths config.Paths
	st    *store.Store
	log   *Logger

	reg     *tool.Registry
	runner  *provider.Runner
	comp    *compact.Manager
	ag      *agent.Agent
	jobs    *job.Manager
	skills  *skill.Registry
	browser *browser.Tool
	fan     *fanout

	ipcSrv   *ipc.Server
	ln       interface{ Close() error }
	sockPath string
	started  time.Time

	mu           sync.Mutex
	runs         map[string]*runRecord
	bySession    map[string]*runRecord
	queue        map[string][]queuedInput
	connSubs     map[int][]*subscriber
	shuttingDown bool

	requireChannel bool
	noChannels     bool
	channels       []channel.Adapter
	pairings       map[string]*channel.Pairing
	progress       map[string]*chatProgress

	lastRequest  map[string]time.Time
	lastActivity time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type queuedInput struct {
	Text  string
	Model string
}

// New builds a daemon server, opening the store and registering tools.
func New(opt Options) (*Server, error) {
	st, err := store.Open(opt.Paths.DB(), opt.Paths.Home)
	if err != nil {
		return nil, err
	}
	cfg := opt.Config
	s := &Server{
		cfg:            cfg,
		paths:          opt.Paths,
		st:             st,
		log:            opt.Logger,
		runs:           map[string]*runRecord{},
		bySession:      map[string]*runRecord{},
		queue:          map[string][]queuedInput{},
		connSubs:       map[int][]*subscriber{},
		pairings:       map[string]*channel.Pairing{},
		progress:       map[string]*chatProgress{},
		lastRequest:    map[string]time.Time{},
		lastActivity:   time.Now(),
		fan:            newFanout(),
		started:        time.Now(),
		requireChannel: opt.RequireChannel,
		noChannels:     opt.NoChannels,
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.reg = tool.Builtin()
	if cfg.Tools.Enabled == nil || len(cfg.Tools.Enabled) == 0 {
		cfg.Tools.Enabled = config.Default().Tools.Enabled
	}
	s.reg.Register(browser.NewTool(cfg))
	s.browser = nil
	if bt, ok := s.reg.Get("browser"); ok {
		if b, ok2 := bt.(*browser.Tool); ok2 {
			s.browser = b
		}
	}
	s.reg.Register(search.NewTool(cfg))

	cwd := cfg.Channel.Workdir
	if cwd == "" {
		cwd = s.paths.Workdir()
	}
	s.skills = skill.Discover(cfg, s.paths.Home, cwd)
	s.skills.RegisterTools(s.reg, cfg, cwd)

	reg2 := provider.NewRegistry(cfg)
	s.runner = provider.NewRunner(cfg, reg2, st)
	s.comp = compact.NewManager(cfg, st, s.runner)
	s.comp.Emit = func(ev compact.Event) {
		s.fan.emit("", "", agent.Event{
			Type: ev.Type, Method: ev.Method, Reason: ev.Reason,
			Tokens: ev.TokensAfter, Notice: ev.Text,
		})
	}

	skillRefs := make([]prompt.SkillRef, 0, 16)
	for _, sk := range s.skills.List() {
		skillRefs = append(skillRefs, prompt.SkillRef{Name: sk.Name, Description: sk.Description})
	}

	s.jobs = job.New(cfg, st)
	s.jobs.OnFinish = s.onJobFinish
	s.jobs.OnUpdate = s.onJobUpdate
	s.jobs.Run = s.runJob

	s.ag = &agent.Agent{
		Config:   cfg,
		Store:    st,
		Runner:   s.runner,
		Tools:    s.reg,
		Compact:  s.comp,
		Sink:     s.onAgentEvent,
		Skills:   skillRefs,
		SkillDir: s.skills.Dir,
		Boundary: s.boundary,
		Jobs:     s.jobs,
		EnqueueJob: func(j *store.Job) (string, error) {
			return s.jobs.Enqueue(j)
		},
	}
	return s, nil
}

// runJob executes one queued job with its own agent run.
func (s *Server) runJob(ctx context.Context, j *store.Job) error {
	opt := job.ContinueOptions(j)
	if j.Tools != "" && j.Tools != "[]" {
		var names []string
		if err := json.Unmarshal([]byte(j.Tools), &names); err == nil {
			opt.Tools = names
		}
	}
	return s.ag.Run(ctx, j.SessionID, opt)
}

// Store exposes the session store.
func (s *Server) Store() *store.Store { return s.st }

// onAgentEvent forwards run events to subscribers.
func (s *Server) onAgentEvent(ev agent.Event) {
	s.mu.Lock()
	if ev.Type == agent.EvUsage || ev.Type == agent.EvRunStart {
		s.lastRequest[ev.SessionID] = time.Now()
	}
	s.mu.Unlock()
	s.fan.emit(ev.RunID, ev.SessionID, ev)
}

// Run starts the server and blocks until the context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Shutdown and the `daemon.shutdown` RPC cancel this context; it must be
	// the one every run and worker derives from.
	s.mu.Lock()
	s.ctx = ctx
	s.cancel = cancel
	s.mu.Unlock()

	ln, err := Listen(s.paths.Socket(s.cfg.Daemon.Socket))
	if err != nil {
		return err
	}
	s.ln = ln
	s.sockPath = s.paths.Socket(s.cfg.Daemon.Socket)
	if err := WritePID(s.paths.PID(), os.Getpid()); err != nil {
		return err
	}
	s.ipcSrv = ipc.NewServer(ln)
	s.ipcSrv.Handler = s.handleIPC
	s.ipcSrv.OnClose = func(c *ipc.Conn) {
		s.mu.Lock()
		subs := s.connSubs[c.ID()]
		delete(s.connSubs, c.ID())
		s.mu.Unlock()
		for _, sub := range subs {
			s.fan.remove(sub)
		}
	}

	if n, err := s.jobs.Start(ctx); err != nil {
		return err
	} else if n > 0 {
		s.logf("recovered %d interrupted jobs", n)
	}

	if !s.noChannels {
		if err := s.startChannels(ctx); err != nil {
			if s.requireChannel {
				return err
			}
			s.logf("channels: %v", err)
		}
	}

	go s.ipcSrv.Serve(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.schedulerLoop(ctx)
	}()

	<-ctx.Done()
	s.Shutdown()
	return nil
}

// Shutdown cancels runs, stops jobs and closes channels.
func (s *Server) Shutdown() {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return
	}
	s.shuttingDown = true
	runs := make([]*runRecord, 0, len(s.runs))
	for _, r := range s.runs {
		runs = append(runs, r)
	}
	s.mu.Unlock()

	for _, r := range runs {
		if r.cancel != nil {
			r.cancel()
		}
	}
	s.jobs.Stop()
	for _, ad := range s.channels {
		_ = ad.Close()
	}
	if s.browser != nil {
		s.browser.Close()
	}
	s.cancel()
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.wg.Wait()
	_ = s.st.Close()
	RemovePID(s.paths.PID())
	_ = os.Remove(s.sockPath)
	if s.log != nil {
		s.log.Close()
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.log != nil {
		s.log.Printf(format, args...)
	}
}
