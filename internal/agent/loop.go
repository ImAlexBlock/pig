package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alexblock/pig/internal/compact"
	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/internalurl"
	"github.com/alexblock/pig/internal/prompt"
	"github.com/alexblock/pig/internal/provider"
	"github.com/alexblock/pig/internal/store"
	"github.com/alexblock/pig/internal/tool"
)

// Hard limits from the plan.
const (
	MaxToolRounds      = 500
	ToolResultHeadKeep = 8192
	ToolResultTailKeep = 2048
)

// BoundaryInfo is passed to the daemon hook at every safe boundary.
type BoundaryInfo struct {
	SessionID  string
	RunID      string
	Elapsed    time.Duration
	ToolRounds int
	QueueLen   int
}

// Agent executes sessions against a model chain.
type Agent struct {
	Config   *config.Config
	Store    *store.Store
	Runner   *provider.Runner
	Tools    *tool.Registry
	Compact  *compact.Manager
	Sink     Sink
	Skills   []prompt.SkillRef
	SkillDir func(name string) (string, bool)

	// Hooks provided by the daemon.
	Boundary   func(ctx context.Context, info BoundaryInfo) (steering string, stopReason string)
	EnqueueJob func(j *store.Job) (string, error)
	Jobs       tool.JobManager
	OriginOf   func(sessionID string) (channel, chat string)
}

// Options configures one run.
type Options struct {
	RunID         string
	Prompt        string
	Model         string
	Depth         int
	Tools         []string
	Subagent      bool
	Task          string
	SubContext    string
	OriginChannel string
	OriginChat    string
}

type runState struct {
	sess        *store.Session
	runID       string
	role        string
	env         *tool.Env
	started     time.Time
	toolRounds  int
	turns       int
	text        strings.Builder
	thinking    strings.Builder
	lastUsage   provider.Usage
	cost        float64
	opt         Options
	noticesSent map[string]bool
	systemText  string
	callSigs    []string
	stopReason  string
	softWarned  bool
}

func (a *Agent) emit(ev Event) {
	if a.Sink != nil {
		a.Sink(ev)
	}
}

func (a *Agent) notice(st *runState, text string) {
	a.emit(Event{Type: EvNotice, RunID: st.runID, SessionID: st.sess.ID, Level: "info", Notice: text})
}

// memoryFacts returns remembered facts for the stable prompt prefix.
func (a *Agent) memoryFacts() []string {
	if a.Store == nil {
		return nil
	}
	facts, err := a.Store.Facts(tool.InjectFacts)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		value := f.Value
		if len(value) > 300 {
			value = value[:300] + "…"
		}
		out = append(out, f.Key+": "+value)
	}
	return out
}

func (a *Agent) appendUser(sessionID, text string) error {
	content := store.NewMessageContent(store.MessageContent{Text: text})
	_, err := a.Store.AppendEntry(sessionID, store.KindMessage, "user", content, compact.EstimateTokens(text))
	return err
}

func (a *Agent) appendNotice(sessionID, text string) error {
	content := store.NewNoticeContent(store.NoticeContent{Level: "info", Text: text})
	_, err := a.Store.AppendEntry(sessionID, store.KindNotice, "", content, 0)
	return err
}

// enabledTools returns the tool names this run may use.
func (a *Agent) enabledTools(opt Options) []string {
	base := a.Config.Tools.Enabled
	if len(opt.Tools) > 0 {
		var out []string
		for _, n := range base {
			for _, want := range opt.Tools {
				if n == want {
					out = append(out, n)
					break
				}
			}
		}
		return out
	}
	if opt.Depth > 0 {
		var out []string
		for _, n := range base {
			if n == "task" {
				continue
			}
			out = append(out, n)
		}
		return out
	}
	return base
}

func (a *Agent) toolDocs(names []string) []prompt.ToolDoc {
	descs := a.Tools.Describe()
	out := make([]prompt.ToolDoc, 0, len(names))
	for _, n := range names {
		out = append(out, prompt.ToolDoc{Name: n, Description: descs[n]})
	}
	return out
}

func (a *Agent) toolSpecs(names []string) []provider.ToolSpec {
	specs := a.Tools.Specs(names)
	out := make([]provider.ToolSpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, provider.ToolSpec{Name: s.Name, Description: s.Description, Schema: s.Schema})
	}
	return out
}

// rebuildReadSet replays read/write/edit calls to restore the read tracking.

func (a *Agent) Run(ctx context.Context, sessionID string, opt Options) error {
	sess, err := a.Store.GetSession(sessionID)
	if err != nil {
		return err
	}
	if opt.Model != "" && opt.Model != sess.Model {
		if _, ok := a.Config.Model(opt.Model); !ok {
			return fmt.Errorf("run: unknown model %q", opt.Model)
		}
		if err := a.Store.SetSessionModel(sessionID, opt.Model); err != nil {
			return err
		}
		sess.Model = opt.Model
	}
	runID := opt.RunID
	if runID == "" {
		runID = store.NewRunID()
	}
	role := "main"
	if sess.Depth > 0 {
		role = "subagent"
	}
	opt.Depth = sess.Depth
	names := a.enabledTools(opt)

	readSet := a.rebuildReadSet(sessionID, sess.CWD)
	env := &tool.Env{
		SessionID:           sessionID,
		RunID:               runID,
		CWD:                 sess.CWD,
		Depth:               sess.Depth,
		Store:               a.Store,
		Config:              a.Config,
		Resolver:            &internalurl.Resolver{Store: a.Store, SessionID: sessionID, SkillDir: a.SkillDir},
		ModelSupportsImages: a.modelSupportsImages(sess.Model),
		ReadFiles:           readSet,
		Secrets:             config.Secrets(a.Config.Path, a.Config),
		OriginChannel:       opt.OriginChannel,
		OriginChat:          opt.OriginChat,
		MaxOutputBytes:      a.Config.Tools.MaxOutputBytes,
		BashTimeout:         time.Duration(a.Config.Tools.BashTimeoutSeconds) * time.Second,
		Jobs:                a.Jobs,
	}
	env.Notify = func(level, text string) {
		a.emit(Event{Type: EvNotice, RunID: runID, SessionID: sessionID, Level: level, Notice: text})
	}
	env.EmitTodo = func(phases []store.TodoPhase) error {
		a.emit(Event{Type: EvTodo, RunID: runID, SessionID: sessionID, Phases: phases})
		return nil
	}
	env.RunSubagent = a.spawnHook()

	st := &runState{
		sess:        sess,
		runID:       runID,
		role:        role,
		env:         env,
		started:     time.Now(),
		opt:         opt,
		noticesSent: map[string]bool{},
	}

	// The system prompt is assembled once per run: it feeds the provider
	// prefix cache, so per-turn churn is expensive. Volatile state (todos,
	// tool results) travels in messages instead.
	sysText, sysNotices := prompt.Build(prompt.Input{
		Config:     a.Config,
		Session:    sess,
		Skills:     a.Skills,
		Tools:      a.toolDocs(names),
		Facts:      a.memoryFacts(),
		Extra:      a.Config.Agent.SystemPrompt,
		Depth:      sess.Depth,
		Task:       opt.Task,
		SubContext: opt.SubContext,
	})
	st.systemText = sysText
	for _, n := range sysNotices {
		a.notice(st, n)
	}

	if opt.Prompt != "" {
		if err := a.appendUser(sessionID, opt.Prompt); err != nil {
			return err
		}
	}

	a.emit(Event{
		Type: EvRunStart, RunID: runID, SessionID: sessionID, Model: sess.Model,
		Text: firstLine(opt.Prompt),
	})

	overflowRetries := 0
	lengthRetries := 0
	stopReason := "end"

	for {
		if err := ctx.Err(); err != nil {
			_ = a.appendNotice(sessionID, "aborted")
			a.notice(st, "aborted")
			stopReason = "canceled"
			break
		}
		if st.toolRounds > MaxToolRounds {
			_ = a.appendNotice(sessionID, "tool round limit reached")
			a.notice(st, "tool round limit reached")
			stopReason = "limit"
			break
		}
		if a.Config.Agent.MaxTurns > 0 && st.turns >= a.Config.Agent.MaxTurns {
			_ = a.appendNotice(sessionID, "turn limit reached")
			a.notice(st, "turn limit reached")
			stopReason = "limit"
			break
		}

		model, ok := a.Config.Model(sess.Model)
		if !ok {
			return fmt.Errorf("run: model %q is not configured", sess.Model)
		}

		entries, err := a.Store.AllEntries(sessionID)
		if err != nil {
			return err
		}
		cmp, _ := a.Store.LatestCompaction(sessionID)
		startSeq := 0
		if cmp != nil {
			startSeq = cmp.FirstKeptSeq
		}
		window := make([]*store.Entry, 0, len(entries))
		for _, e := range entries {
			if e.Seq >= startSeq {
				window = append(window, e)
			}
		}
		systemText := st.systemText

		msgs := compact.Project(cmp, window, compact.ProjectOptions{
			ImageInput: model.SupportsImages(),
			LoadFrame: func(id string) (string, []byte, bool) {
				art, err := a.Store.GetArtifact(id)
				if err != nil {
					return "", nil, false
				}
				_, data, err := a.Store.ReadArtifact(id)
				if err != nil {
					return "", nil, false
				}
				return art.Mime, data, true
			},
		})
		specs := a.toolSpecs(names)
		req := provider.Request{
			System:     []provider.Block{{Type: provider.BlockText, Text: systemText}},
			Messages:   msgs,
			Tools:      specs,
			MaxTokens:  model.MaxOutput,
			SessionID:  sessionID,
			ImageInput: model.SupportsImages(),
		}

		// Mid-turn compaction at the safe boundary.
		if a.Config.Compaction.MidTurn {
			est := compact.ContextTokens(msgs, st.lastUsage.Input, a.Config.Snapcompact.ImageTokens) +
				compact.EstimateTokens(systemText) + toolsTokens(specs)
			if est > int(a.Config.Compaction.Threshold*float64(model.ContextWindow)) {
				if a.runCompaction(ctx, st, systemText, specs, model) {
					st.lastUsage.Input = 0
					continue
				}
			}
		}

		st.text.Reset()
		st.thinking.Reset()
		chain := a.Runner.Chain(sess.Model, role)
		res, outcome, serr := a.Runner.Stream(ctx, role, chain, req, func(ev provider.Event) error {
			switch ev.Type {
			case provider.EvTextStart:
				if ev.Reset {
					st.text.Reset()
				}
			case provider.EvTextDelta:
				st.text.WriteString(ev.Text)
				a.emit(Event{Type: EvTextDelta, RunID: st.runID, SessionID: sessionID, Text: ev.Text})
			case provider.EvThinkingDelta:
				st.thinking.WriteString(ev.Text)
				a.emit(Event{Type: EvThinkingDelta, RunID: st.runID, SessionID: sessionID, Text: ev.Text})
			}
			return nil
		})
		for _, n := range outcome.Notices {
			a.notice(st, n)
		}
		if serr != nil {
			pe, isPE := provider.AsProviderError(serr)
			if isPE && pe.Class == provider.ClassCanceled {
				_ = a.appendNotice(sessionID, "aborted")
				a.notice(st, "aborted")
				stopReason = "canceled"
				break
			}
			if isPE && pe.Class == provider.ClassContextOverflow && overflowRetries < 1 {
				overflowRetries++
				a.notice(st, "context overflow: compacting")
				a.runCompaction(ctx, st, systemText, specs, model)
				st.lastUsage.Input = 0
				continue
			}
			a.emit(Event{Type: EvError, RunID: st.runID, SessionID: sessionID, Error: serr.Error()})
			stopReason = "error"
			break
		}
		st.turns++
		st.lastUsage = res.Usage
		st.cost += modelCost(model, res.Usage)
		a.emit(Event{
			Type: EvUsage, RunID: st.runID, SessionID: sessionID,
			Usage: &res.Usage, CostUSD: modelCost(model, res.Usage),
		})

		// Persist the assistant turn.
		text := st.text.String()
		calls := provider.ToolCalls(res.Message)
		if text != "" || len(calls) > 0 {
			content := store.NewMessageContent(store.MessageContent{
				Text:      text,
				Reasoning: st.thinking.String(),
				Provider:  outcome.Provider,
				Model:     outcome.Model,
				Raw:       res.Raw,
			})
			if _, err := a.Store.AppendEntry(sessionID, store.KindMessage, "assistant", content, compact.EstimateTokens(text)); err != nil {
				return err
			}
			for _, c := range calls {
				args := c.Args
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				tc := store.NewToolCallContent(store.ToolCallContent{ID: c.ToolID, Name: c.ToolName, Args: args})
				if _, err := a.Store.AppendEntry(sessionID, store.KindToolCall, "", tc, 0); err != nil {
					return err
				}
			}
		}

		if len(calls) > 0 {
			a.runToolBatch(ctx, st, calls)
			st.toolRounds++

			// Safe boundary: steering, detach or convergence control.
			var steering, stop string
			if a.Boundary != nil {
				steering, stop = a.Boundary(ctx, BoundaryInfo{
					SessionID:  sessionID,
					RunID:      st.runID,
					Elapsed:    time.Since(st.started),
					ToolRounds: st.toolRounds,
				})
			}
			if stop != "" {
				stopReason = stop
				break
			}
			convSteer, stalled := a.trackCalls(st, calls)
			if stalled {
				_ = a.appendNotice(sessionID, "stopped: the same tool call repeated")
				a.notice(st, "stopped: the same tool call repeated")
				stopReason = "stalled"
				break
			}
			if softSteer := a.softRoundSteering(st); softSteer != "" {
				convSteer = strings.TrimSpace(convSteer + "\n" + softSteer)
			}
			if convSteer != "" {
				if steering != "" {
					steering += "\n"
				}
				steering += convSteer
			}
			if steering != "" {
				if err := a.appendUser(sessionID, steering); err != nil {
					return err
				}
			}
			// Post-turn compaction.
			if est := compact.ContextTokens(msgs, st.lastUsage.Input, a.Config.Snapcompact.ImageTokens); est > int(a.Config.Compaction.Threshold*float64(model.ContextWindow)) {
				a.runCompaction(ctx, st, systemText, specs, model)
			}
			continue
		}

		if res.Stop == provider.StopLength && lengthRetries < 1 {
			lengthRetries++
			a.notice(st, "output truncated: compacting and retrying")
			a.runCompaction(ctx, st, systemText, specs, model)
			st.lastUsage.Input = 0
			continue
		}
		break
	}

	elapsed := time.Since(st.started)
	if stopReason == "detached" {
		a.emit(Event{Type: EvRunDetached, RunID: st.runID, SessionID: sessionID})
	}
	a.emit(Event{
		Type: EvRunEnd, RunID: st.runID, SessionID: sessionID, Stop: stopReason,
		ElapsedMS: elapsed.Milliseconds(), Usage: &st.lastUsage, CostUSD: modelCostForTotal(st.cost),
		Tokens: compact.ContextTokens(nil, st.lastUsage.Input, a.Config.Snapcompact.ImageTokens),
	})
	return nil
}

func modelCostForTotal(c float64) float64 { return c }

func modelCost(m config.Model, u provider.Usage) float64 {
	return float64(u.Input)/1e6*m.Cost.Input +
		float64(u.Output)/1e6*m.Cost.Output +
		float64(u.CacheRead)/1e6*m.Cost.CacheRead +
		float64(u.CacheWrite)/1e6*m.Cost.CacheWrite
}

func (a *Agent) modelSupportsImages(ref string) bool {
	m, ok := a.Config.Model(ref)
	return ok && m.SupportsImages()
}

// runCompaction compacts the session; it reports whether anything changed.
func (a *Agent) runCompaction(ctx context.Context, st *runState, systemText string, specs []provider.ToolSpec, model config.Model) bool {
	if a.Compact == nil {
		return false
	}
	res, err := a.Compact.Compact(ctx, compact.Request{
		SessionID:           st.sess.ID,
		Reason:              "mid_turn",
		ModelSupportsImages: model.SupportsImages(),
		SystemText:          systemText,
		ToolsTokens:         toolsTokens(specs),
	})
	if err != nil {
		a.notice(st, "compaction failed: "+err.Error())
		return false
	}
	return res.Compacted
}
