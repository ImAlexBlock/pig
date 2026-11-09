package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/provider"
	"github.com/ImAlexBlock/pig/internal/store"
)

// Event is a compaction lifecycle notification.
type Event struct {
	Type         string `json:"type"`
	Method       string `json:"method,omitempty"`
	Reason       string `json:"reason,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	TokensAfter  int    `json:"tokens_after,omitempty"`
	Text         string `json:"text,omitempty"`
}

// Manager runs compaction strategies.
type Manager struct {
	cfg    *config.Config
	store  *store.Store
	runner *provider.Runner
	Emit   func(Event)
}

// NewManager returns a compaction manager. runner may be nil (LLM strategies
// are then unavailable).
func NewManager(cfg *config.Config, st *store.Store, runner *provider.Runner) *Manager {
	return &Manager{cfg: cfg, store: st, runner: runner, Emit: func(Event) {}}
}

// Request describes one compaction attempt.
type Request struct {
	SessionID           string
	Reason              string
	Instructions        string
	ModelSupportsImages bool
	SystemText          string
	ToolsTokens         int
}

// Result reports what compaction did.
type Result struct {
	Compacted    bool
	Method       string
	TokensBefore int
	TokensAfter  int
	FirstKeptSeq int
	Summary      string
	Frames       []store.Frame
}

type elideEdit struct {
	entry *store.Entry
	text  string
}

type candidate struct {
	method       string
	summary      string
	frames       []store.Frame
	firstKeptSeq int
	tokensAfter  int
	elide        []elideEdit
	sourceText   string
	pngs         [][]byte
}

func (m *Manager) window(sessionID string) (int, bool) {
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return 0, false
	}
	model, ok := m.cfg.Model(sess.Model)
	if !ok {
		return 0, false
	}
	return model.ContextWindow, true
}

func (m *Manager) methods() []string {
	if len(m.cfg.Compaction.Methods) == 0 {
		return []string{"shake", "llm"}
	}
	return m.cfg.Compaction.Methods
}

// Compact runs the configured strategies and commits the first candidate that
// fits the budget.
func (m *Manager) Compact(ctx context.Context, req Request) (Result, error) {
	window, ok := m.window(req.SessionID)
	if !ok || window <= 0 {
		return Result{}, nil
	}
	entries, err := m.store.AllEntries(req.SessionID)
	if err != nil {
		return Result{}, err
	}
	prev, _ := m.store.LatestCompaction(req.SessionID)
	startSeq := 0
	prevSummary := ""
	if prev != nil {
		startSeq = prev.FirstKeptSeq
		prevSummary = prev.Summary
	}
	windowEntries := make([]*store.Entry, 0, len(entries))
	for _, e := range entries {
		if e.Seq >= startSeq {
			windowEntries = append(windowEntries, e)
		}
	}
	prepared := Prepare(windowEntries, m.cfg.Compaction.KeepRecentTokens, prevSummary)
	if len(prepared.Summarize) == 0 {
		return Result{Compacted: false}, nil
	}

	// tokens_before measures the prompt as it is today: the whole retained
	// window with the previous summary. Candidates are measured with the
	// summarised prefix replaced, which is what compaction changes.
	fullProjection := Project(prev, windowEntries, ProjectOptions{})
	tokensBefore := ProjectTokens(fullProjection, m.cfg.Snapcompact.ImageTokens) +
		EstimateTokens(req.SystemText) + req.ToolsTokens
	budget := int(m.cfg.Compaction.Threshold * m.cfg.Compaction.Target * float64(window))

	m.Emit(Event{Type: "compact_start", Reason: req.Reason, TokensBefore: tokensBefore})

	var best, last *candidate
	for _, method := range m.methods() {
		var cand *candidate
		var cerr error
		switch method {
		case "snapcompact":
			if !req.ModelSupportsImages {
				continue
			}
			cand, cerr = m.snapcompactCandidate(ctx, req, prepared, tokensBefore, budget)
		case "shake":
			cand, cerr = m.shakeCandidate(prepared, tokensBefore)
		case "llm":
			cand, cerr = m.llmCandidate(ctx, req, prepared, tokensBefore)
		}
		if cerr != nil {
			m.Emit(Event{Type: "notice", Text: fmt.Sprintf("compaction %s failed: %v", method, cerr)})
			continue
		}
		if cand == nil {
			continue
		}
		last = cand
		if cand.tokensAfter <= budget {
			best = cand
			break
		}
	}

	chosen := best
	if chosen == nil {
		chosen = last
	}
	if chosen == nil {
		m.Emit(Event{Type: "compact_end", Reason: req.Reason, TokensBefore: tokensBefore, TokensAfter: tokensBefore})
		return Result{Compacted: false, TokensBefore: tokensBefore, TokensAfter: tokensBefore}, nil
	}
	if best == nil {
		m.Emit(Event{Type: "notice", Text: "compaction could not free enough context"})
	}
	if err := m.commit(req.SessionID, tokensBefore, chosen); err != nil {
		return Result{}, err
	}
	res := Result{
		Compacted:    true,
		Method:       chosen.method,
		TokensBefore: tokensBefore,
		TokensAfter:  chosen.tokensAfter,
		FirstKeptSeq: chosen.firstKeptSeq,
		Summary:      chosen.summary,
		Frames:       chosen.frames,
	}
	m.Emit(Event{
		Type: "compact_end", Method: chosen.method, Reason: req.Reason,
		TokensBefore: tokensBefore, TokensAfter: chosen.tokensAfter,
	})
	if chosen.tokensAfter > window {
		m.Emit(Event{Type: "notice", Text: "compaction could not free enough context"})
	}
	return res, nil
}

func (m *Manager) commit(sessionID string, tokensBefore int, c *candidate) error {
	if c.method == "shake" {
		for _, ed := range c.elide {
			content := ed.entry.ToolResult()
			content.Text = ed.text
			if err := m.store.UpdateEntryContent(ed.entry.ID, store.NewToolResultContent(content), EstimateTokens(ed.text)); err != nil {
				return err
			}
		}
		return m.store.AppendCompaction(&store.Compaction{
			SessionID:    sessionID,
			Method:       "shake",
			Summary:      "",
			FirstKeptSeq: -1,
			Frames:       "[]",
			TokensBefore: tokensBefore,
			TokensAfter:  c.tokensAfter,
		})
	}
	if c.method == "snapcompact" {
		frames, err := m.persistFrames(sessionID, c.pngs)
		if err != nil {
			return err
		}
		c.frames = frames
	}
	cmpID := store.NewCompactionID()
	entry, err := m.store.AppendEntry(sessionID, store.KindCompaction, "", store.NewCompactionContent(store.CompactionContent{CompactionID: cmpID}), 0)
	if err != nil {
		return err
	}
	return m.store.AppendCompaction(&store.Compaction{
		ID:           cmpID,
		SessionID:    sessionID,
		EntryID:      entry.ID,
		Method:       c.method,
		Summary:      c.summary,
		FirstKeptSeq: c.firstKeptSeq,
		SourceText:   c.sourceText,
		Frames:       store.EncodeFrames(c.frames),
		TokensBefore: tokensBefore,
		TokensAfter:  c.tokensAfter,
	})
}

// SummaryHeadings are the fixed section titles used in summaries.
var SummaryHeadings = []string{"## Goal", "## Done", "## Open", "## Files", "## Errors"}

// RenderFiles renders a <files> block.
func RenderFiles(files []string) string {
	if len(files) == 0 {
		return ""
	}
	const maxFiles = 20
	var sb strings.Builder
	sb.WriteString("<files>\n")
	for i, f := range files {
		if i >= maxFiles {
			sb.WriteString(fmt.Sprintf("[…%d files elided…]\n", len(files)-maxFiles))
			break
		}
		sb.WriteString(f + "\n")
	}
	sb.WriteString("</files>")
	return sb.String()
}

// FilesFromEntries lists touched files with their access mode.
func FilesFromEntries(entries []*store.Entry) []string {
	mode := map[string]string{}
	var order []string
	set := func(path, m string) {
		if path == "" {
			return
		}
		cur, ok := mode[path]
		if !ok {
			order = append(order, path)
			mode[path] = m
			return
		}
		if cur != m {
			mode[path] = "RW"
		}
	}
	for _, e := range entries {
		if e.Kind == store.KindToolCall {
			c := e.ToolCall()
			switch c.Name {
			case "read":
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(c.Args, &args); err == nil {
					set(args.Path, "Read")
				}
			case "write", "edit":
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(c.Args, &args); err == nil {
					set(args.Path, "Write")
				}
			}
		}
	}
	out := make([]string, 0, len(order))
	for _, p := range order {
		out = append(out, fmt.Sprintf("%s (%s)", p, mode[p]))
	}
	return out
}
