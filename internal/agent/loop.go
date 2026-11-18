package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ImAlexBlock/pig/internal/compact"
	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/internalurl"
	"github.com/ImAlexBlock/pig/internal/prompt"
	"github.com/ImAlexBlock/pig/internal/provider"
	"github.com/ImAlexBlock/pig/internal/store"
	"github.com/ImAlexBlock/pig/internal/tool"
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
	// Attachments are artifact ids already stored for this prompt.
	Attachments []string
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

	// Incremental projection cache. msgs holds the projected entries up to the
	// last closed tool exchange; pending holds the entries that may still pair
	// with future ones and is re-projected each turn. projectedSeq is the
	// highest entry seq already fetched, and cmpID/cmpKeepSeq identify the
	// compaction whose prefix is baked into msgs.
	projected    bool
	msgs         []provider.Message
	pending      []*store.Entry
	projectedSeq int
	cmpID        string
	cmpKeepSeq   int
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
	return a.appendUserWithAttachments(sessionID, text, nil)
}

// appendUserWithAttachments writes a user turn plus attachment artifact ids.
func (a *Agent) appendUserWithAttachments(sessionID, text string, attachments []string) error {
	content := store.NewMessageContent(store.MessageContent{Text: text, Attachments: attachments})
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

// projectedMessages returns the wire messages for the session prefix.
//
// The first turn (or a turn after compaction changed) projects the whole
// window. Later turns reuse the cached messages and project only the entries
// appended since, so the work per turn is proportional to the new entries
// rather than the whole session.
//
// compact.Project repairs dangling tool blocks against the entries it is
// given, and a tool_use can pair with a tool_result that arrives in a later
// batch. So the cache only holds entries up to the last *closed* tool
// exchange; the entries after it (in practice one tool batch) stay in
// st.pending and are re-projected every turn until their exchanges close.
// Because the cached prefix contains no unpaired tool block, the repair inside
// Project is a no-op there and the merged result is byte-identical to a full
// compact.Project over the whole window.
func (a *Agent) projectedMessages(st *runState, sessionID string, opt compact.ProjectOptions) ([]provider.Message, error) {
	cmp, _ := a.Store.LatestCompaction(sessionID)
	cmpID, keepSeq := "", 0
	if cmp != nil {
		cmpID, keepSeq = cmp.ID, cmp.FirstKeptSeq
	}
	if !st.projected || cmpID != st.cmpID || keepSeq != st.cmpKeepSeq {
		entries, err := a.Store.AllEntries(sessionID)
		if err != nil {
			return nil, err
		}
		window := make([]*store.Entry, 0, len(entries))
		for _, e := range entries {
			if e.Seq >= keepSeq {
				window = append(window, e)
			}
		}
		cut := projectCut(window)
		st.msgs = compact.Project(cmp, window[:cut], opt)
		st.pending = append(st.pending[:0], window[cut:]...)
		st.cmpID, st.cmpKeepSeq = cmpID, keepSeq
		st.projected = true
		// Skip the dropped prefix on later turns: EntriesAfter only needs to
		// resume after the newest seq already seen.
		st.projectedSeq = 0
		if n := len(entries); n > 0 {
			st.projectedSeq = entries[n-1].Seq
		}
		return st.projectedView(opt), nil
	}
	fresh, err := a.Store.EntriesAfter(sessionID, st.projectedSeq, 0)
	if err != nil {
		return nil, err
	}
	if n := len(fresh); n > 0 {
		st.projectedSeq = fresh[n-1].Seq
		// Full projection drops entries below the compaction window and
		// renders compaction entries as nothing; mirror that before folding.
		for _, e := range fresh {
			if e.Seq >= keepSeq {
				st.pending = append(st.pending, e)
			}
		}
	}
	if len(st.pending) == 0 {
		return st.msgs, nil
	}
	// Fold the suffix once it closes so it is not re-projected forever.
	if cut := projectCut(st.pending); cut > 0 {
		st.msgs = append(st.msgs, compact.Project(nil, st.pending[:cut], opt)...)
		st.msgs = provider.MergeMessages(st.msgs)
		st.pending = st.pending[cut:]
	}
	return st.projectedView(opt), nil
}

// projectedView merges the cached prefix with the re-projected open suffix
// without mutating either.
func (st *runState) projectedView(opt compact.ProjectOptions) []provider.Message {
	if len(st.pending) == 0 {
		return st.msgs
	}
	tail := compact.Project(nil, st.pending, opt)
	out := make([]provider.Message, 0, len(st.msgs)+len(tail))
	out = append(out, st.msgs...)
	out = append(out, tail...)
	return provider.MergeMessages(out)
}

// debugProjection records what a turn actually sends when PIG_DEBUG_PROJ is set.
func debugProjection(sessionID string, msgs []provider.Message, cached, pending int, imageInput bool) {
	if os.Getenv("PIG_DEBUG_PROJ") == "" {
		return
	}
	images := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockImage {
				images++
			}
		}
	}
	path := filepath.Join(os.Getenv("PIG_HOME"), "logs", "proj.log")
	if home := os.Getenv("PIG_HOME"); home == "" {
		path = filepath.Join(os.TempDir(), "pig-proj.log")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s session=%s cached=%d pending=%d message_images=%d image_input=%v\n",
		time.Now().Format(time.RFC3339), sessionID, cached, pending, images, imageInput)
	f.Close()
}

// projectCut returns how many leading entries may be folded into the cache.
// It stops at the start of an open tool exchange and always leaves the newest
// assistant message in the suffix: a tool call appended later attaches to that
// message, and a dangling call makes Project clear the message's raw payload,
// so the message must be re-projected together with the call.
func projectCut(entries []*store.Entry) int {
	cut := closedPrefix(entries)
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == store.KindMessage && entries[i].Role == "assistant" {
			if i < cut {
				cut = i
			}
			break
		}
	}
	return cut
}

// closedPrefix returns the length of the longest prefix of entries whose tool
// calls all have their matching tool result inside that prefix. Projecting such
// a prefix is stable: later entries can only pair with tool blocks in the
// returned suffix.
func closedPrefix(entries []*store.Entry) int {
	callAt := map[string]int{}
	resultAt := map[string]int{}
	for i, e := range entries {
		switch e.Kind {
		case store.KindToolCall:
			callAt[e.ToolCall().ID] = i
		case store.KindToolResult:
			resultAt[e.ToolResult().CallID] = i
		}
	}
	cut := len(entries)
	for {
		changed := false
		for id, ci := range callAt {
			if ci >= cut {
				continue
			}
			if ri, ok := resultAt[id]; !ok || ri >= cut {
				cut = ci
				changed = true
			}
		}
		if !changed {
			return cut
		}
	}
}

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
		if err := a.appendUserWithAttachments(sessionID, opt.Prompt, opt.Attachments); err != nil {
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

		msgs, err := a.projectedMessages(st, sessionID, compact.ProjectOptions{
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
		if err != nil {
			return err
		}
		debugProjection(sessionID, msgs, len(st.msgs), len(st.pending), model.SupportsImages())
		systemText := st.systemText
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
