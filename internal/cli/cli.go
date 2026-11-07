// Package cli implements the pig command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/daemon"
	"github.com/alexblock/pig/internal/ipc"
	"github.com/alexblock/pig/internal/store"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitUsage   = 2
	ExitConfig  = 3
	ExitStorage = 4
	ExitDaemon  = 5
)

// App carries per-invocation state.
type App struct {
	Paths    config.Paths
	Config   *config.Config
	JSON     bool
	Mock     bool
	NoDaemon bool
	CWD      string

	Out io.Writer
	Err io.Writer
	In  io.Reader

	inproc       *daemon.Server
	inprocCancel context.CancelFunc
	inprocDone   chan struct{}
}

// usageError marks a usage failure.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// Run executes the CLI and returns the process exit code.
func Run(args []string) int {
	app := &App{Out: os.Stdout, Err: os.Stderr, In: os.Stdin}
	code, err := app.run(args)
	if err != nil {
		fmt.Fprintln(app.Err, "pig: "+err.Error())
		var ue *usageError
		switch {
		case errors.As(err, &ue):
			return ExitUsage
		}
		return code
	}
	return code
}

func (a *App) run(args []string) (int, error) {
	paths, err := config.Resolve()
	if err != nil {
		return ExitConfig, err
	}
	a.Paths = paths
	cwd, _ := os.Getwd()
	a.CWD = cwd

	rest, err := a.parseGlobals(args)
	if err != nil {
		return ExitUsage, err
	}
	if len(rest) == 0 {
		return a.cmdTUI(nil)
	}
	cmd, tail := rest[0], rest[1:]
	switch cmd {
	case "run":
		return a.cmdRun(tail)
	case "tui":
		return a.cmdTUI(tail)
	case "serve":
		return a.cmdServe(tail)
	case "daemon":
		return a.cmdDaemon(tail)
	case "config":
		return a.cmdConfig(tail)
	case "auth":
		return a.cmdAuth(tail)
	case "models":
		return a.cmdModels(tail)
	case "sessions":
		return a.cmdSessions(tail)
	case "jobs":
		return a.cmdJobs(tail)
	case "agents":
		return a.cmdAgents(tail)
	case "show":
		return a.cmdShow(tail)
	case "rtk":
		return a.cmdRTK(tail)
	case "skills":
		return a.cmdSkills(tail)
	case "doctor":
		return a.cmdDoctor(tail)
	case "debug":
		return a.cmdDebug(tail)
	case "docs":
		return a.cmdDocs(tail)
	case "version":
		fmt.Fprintln(a.Out, daemon.Version)
		return ExitOK, nil
	case "help", "-h", "--help":
		fmt.Fprint(a.Out, helpText)
		return ExitOK, nil
	}
	return ExitUsage, usagef("unknown command %q", cmd)
}

func (a *App) parseGlobals(args []string) ([]string, error) {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			a.JSON = true
		case "--mock":
			a.Mock = true
		case "--no-daemon":
			a.NoDaemon = true
		case "--":
			out = append(out, args[i+1:]...)
			i = len(args)
		default:
			out = append(out, args[i])
		}
	}
	if v := os.Getenv("PIG_NO_DAEMON"); v == "1" {
		a.NoDaemon = true
	}
	return out, nil
}

// loadConfig loads the config, honouring --mock.
func (a *App) loadConfig() error {
	path := a.Paths.Config()
	cfg, err := config.Load(path)
	if err == nil {
		if a.Mock {
			forceMock(cfg)
		}
		a.Config = cfg
		for _, u := range cfg.Unknown {
			fmt.Fprintf(a.Err, "pig: config: unknown key %s\n", u)
		}
		_, _ = config.ApplyAuth(a.Paths.Auth())
		return nil
	}
	if !errors.Is(err, config.ErrNotFound) {
		return err
	}
	if a.Mock {
		a.Config = config.MockConfig(path)
		a.NoDaemon = true
		return nil
	}
	return fmt.Errorf("%w: %s", config.ErrNotFound, path)
}

// forceMock makes --mock authoritative on a real configuration.
func forceMock(cfg *config.Config) {
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.Provider{}
	}
	cfg.Providers["mock"] = config.Provider{Kind: "mock", BaseURL: "mock://"}
	if _, ok := cfg.Model("mock/mock"); !ok {
		cfg.Models = append(cfg.Models, config.Model{
			Ref: "mock/mock", Provider: "mock", Model: "mock",
			ContextWindow: 128000, MaxOutput: 4096, Input: []string{"text"},
		})
	}
	prepend := func(chain []string) []string {
		for _, r := range chain {
			if r == "mock/mock" {
				return chain
			}
		}
		return append([]string{"mock/mock"}, chain...)
	}
	cfg.Roles.Main = prepend(cfg.Roles.Main)
	cfg.Roles.Compact = prepend(cfg.Roles.Compact)
	cfg.Roles.Subagent = prepend(cfg.Roles.Subagent)
}

// requireConfig loads config and maps missing config to exit 3.
func (a *App) requireConfig() error {
	if err := a.loadConfig(); err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return err
		}
		return err
	}
	return nil
}

// openStore opens the database directly (read-only paths).
func (a *App) openStore() (*store.Store, error) {
	return store.Open(a.Paths.DB(), a.Paths.Home)
}

// client connects to the daemon, starting it when needed.
func (a *App) client(ctx context.Context) (*ipc.Client, error) {
	if a.Config == nil {
		if err := a.requireConfig(); err != nil {
			return nil, err
		}
	}
	socket := a.Paths.Socket(a.Config.Daemon.Socket)
	if a.NoDaemon {
		if err := a.ensureInProcess(ctx); err != nil {
			return nil, err
		}
	} else if err := daemon.Ensure(ctx, a.Config, a.Paths); err != nil {
		return nil, err
	}
	c, err := ipc.Dial(ctx, socket)
	if err != nil {
		return nil, fmt.Errorf("daemon: %v", err)
	}
	return c, nil
}

func (a *App) ensureInProcess(ctx context.Context) error {
	if a.inproc != nil {
		return nil
	}
	// Never clobber a live daemon's socket.
	if socketAlive(a.Paths.Socket(a.Config.Daemon.Socket)) {
		return nil
	}
	if err := a.Paths.Ensure(); err != nil {
		return err
	}
	logger, _ := daemon.NewLogger(a.Paths.Log(), a.Config.Daemon.LogMaxBytes)
	srv, err := daemon.New(daemon.Options{
		Config: a.Config, Paths: a.Paths, Logger: logger, NoChannels: true,
	})
	if err != nil {
		return err
	}
	sctx, cancel := context.WithCancel(context.Background())
	a.inproc = srv
	a.inprocCancel = cancel
	done := make(chan struct{})
	a.inprocDone = done
	go func() {
		defer close(done)
		_ = srv.Run(sctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	socket := a.Paths.Socket(a.Config.Daemon.Socket)
	for time.Now().Before(deadline) {
		if c, err := ipc.Dial(context.Background(), socket); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon: failed to start in-process")
}

// socketAlive reports whether a daemon is listening on the socket.
func socketAlive(path string) bool {
	c, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// closeInProcess shuts down an in-process daemon and waits for it to clean up.
func (a *App) closeInProcess() {
	if a.inprocCancel != nil {
		a.inprocCancel()
		a.inprocCancel = nil
	}
	if a.inprocDone != nil {
		select {
		case <-a.inprocDone:
		case <-time.After(15 * time.Second):
		}
		a.inprocDone = nil
	}
	a.inproc = nil
}

// printJSON writes v as indented JSON.
func (a *App) printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(a.Err, "pig: json: %v\n", err)
		return
	}
	fmt.Fprintln(a.Out, string(b))
}

func (a *App) printNDJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintln(a.Out, string(b))
}

// call performs one RPC and decodes the result.
func (a *App) call(ctx context.Context, c *ipc.Client, method string, params, out any) error {
	raw, err := c.Call(ctx, method, params)
	if err != nil {
		return err
	}
	if out != nil {
		if err := ipc.Decode(raw, out); err != nil {
			return fmt.Errorf("%s: %v", method, err)
		}
	}
	return nil
}

func absPath(p string) string {
	if p == "" {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

const helpText = `pig — personal assistant in a single binary

usage: pig <command> [flags]

commands:
  run [prompt]        send a prompt (stdin when omitted)
  tui                 open the terminal UI (default)
  serve               run the daemon in the foreground with channels
  daemon <action>     start | stop | status | reload | logs | run
  config <action>     get | set | unset | validate | init
  auth <action>       set | list | rm
  models <action>     add | list | rm
  sessions <action>   list | show | rm | export
  jobs <action>       list | show | wait | cancel | retry | logs
  agents <action>     list | show
  show <uri>          read an internal URI
  rtk <action>        preview | gain
  skills <action>     list
  doctor [--strict]   check the environment
  debug <action>      render-snap
  docs [topic]        print documentation
  version             print the version

global flags:
  --json       machine-readable output (NDJSON for streaming commands)
  --mock       use the mock provider
  --no-daemon  run the daemon in this process
`
