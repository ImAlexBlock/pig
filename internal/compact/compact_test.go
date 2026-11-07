package compact

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/provider"
	"github.com/alexblock/pig/internal/store"
)

func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "pig.db"), dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	sess, err := st.CreateSession(store.SessionOptions{CWD: dir, Model: "mock/mock"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return st, sess.ID
}

func appendMsg(t *testing.T, st *store.Store, sessionID, role, text string) *store.Entry {
	t.Helper()
	e, err := st.AppendEntry(sessionID, store.KindMessage, role,
		store.NewMessageContent(store.MessageContent{Text: text}), EstimateTokens(text))
	if err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	return e
}

func seedConversation(t *testing.T, st *store.Store, sessionID string) {
	t.Helper()
	big := strings.Repeat("0123456789abcdef", 5000) // ~80000 chars ≈ 20000 tokens
	for i := range 6 {
		appendMsg(t, st, sessionID, "user", "task step with detail "+strings.Repeat("x", 200))
		callID := "call_" + string(rune('a'+i))
		if _, err := st.AppendEntry(sessionID, store.KindToolCall, "",
			store.NewToolCallContent(store.ToolCallContent{
				ID: callID, Name: "bash", Args: json.RawMessage(`{"command":"ls"}`),
			}), 0); err != nil {
			t.Fatalf("tool call: %v", err)
		}
		if _, err := st.AppendEntry(sessionID, store.KindToolResult, "",
			store.NewToolResultContent(store.ToolResultContent{
				CallID: callID, Name: "bash", Text: big,
			}), EstimateTokens(big)); err != nil {
			t.Fatalf("tool result: %v", err)
		}
		appendMsg(t, st, sessionID, "assistant", "assistant reply "+strings.Repeat("y", 300))
	}
}

func newManager(t *testing.T, st *store.Store, cfg *config.Config) *Manager {
	t.Helper()
	reg := provider.NewRegistry(cfg)
	runner := provider.NewRunner(cfg, reg, st)
	m := NewManager(cfg, st, runner)
	m.Emit = func(Event) {}
	return m
}

func TestPrepareCutPoint(t *testing.T) {
	st, sessionID := newStore(t)
	seedConversation(t, st, sessionID)
	entries, err := st.AllEntries(sessionID)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	prepared := Prepare(entries, 200, "")
	if len(prepared.Summarize) == 0 {
		t.Fatal("nothing to summarise; fixture too small")
	}
	if len(prepared.Recent) == 0 {
		t.Fatal("no recent window")
	}
	first := prepared.Recent[0]
	if first.Kind != store.KindMessage || first.Role != "user" {
		t.Fatalf("first kept entry is %s/%s, want message/user", first.Kind, first.Role)
	}
	if first.Seq != prepared.FirstKeptSeq {
		t.Fatalf("FirstKeptSeq = %d, entry seq = %d", prepared.FirstKeptSeq, first.Seq)
	}
	// A tool call must never be separated from its result.
	results := map[string]bool{}
	for _, e := range prepared.Recent {
		if e.Kind == store.KindToolResult {
			results[e.ToolResult().CallID] = true
		}
	}
	for _, e := range prepared.Recent {
		if e.Kind != store.KindToolCall {
			continue
		}
		id := e.ToolCall().ID
		if !results[id] {
			t.Fatalf("recent window has orphan tool_call %s", id)
		}
	}
	// The summarised prefix must not end with an unanswered tool call.
	open := map[string]bool{}
	for _, e := range prepared.Summarize {
		switch e.Kind {
		case store.KindToolCall:
			open[e.ToolCall().ID] = true
		case store.KindToolResult:
			delete(open, e.ToolResult().CallID)
		}
	}
	if len(open) != 0 {
		t.Fatalf("summarised prefix has %d unanswered tool calls", len(open))
	}
}

func TestCompactLLM(t *testing.T) {
	st, sessionID := newStore(t)
	seedConversation(t, st, sessionID)
	cfg := config.MockConfig(filepath.Join(t.TempDir(), "config.toml"))
	cfg.Compaction.Methods = []string{"llm"}
	cfg.Compaction.KeepRecentTokens = 200
	m := newManager(t, st, cfg)

	res, err := m.Compact(context.Background(), Request{SessionID: sessionID, Reason: "manual"})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !res.Compacted {
		t.Fatal("expected compaction")
	}
	if res.Method != "llm" {
		t.Fatalf("method = %q, want llm", res.Method)
	}
	if strings.TrimSpace(res.Summary) != "mock summary" {
		t.Fatalf("summary = %q, want mock summary", res.Summary)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Fatalf("tokens_after %d >= tokens_before %d", res.TokensAfter, res.TokensBefore)
	}
	cmp, err := st.LatestCompaction(sessionID)
	if err != nil {
		t.Fatalf("LatestCompaction: %v", err)
	}
	if cmp.Method != "llm" || cmp.Summary == "" {
		t.Fatalf("stored compaction = %+v", cmp)
	}
	if cmp.FirstKeptSeq <= 0 {
		t.Fatalf("first_kept_seq = %d", cmp.FirstKeptSeq)
	}
	entries, _ := st.AllEntries(sessionID)
	var marker bool
	for _, e := range entries {
		if e.Kind == store.KindCompaction {
			marker = true
		}
	}
	if !marker {
		t.Fatal("no compaction marker entry")
	}
}

func TestCompactSnapcompact(t *testing.T) {
	st, sessionID := newStore(t)
	seedConversation(t, st, sessionID)
	cfg := config.MockConfig(filepath.Join(t.TempDir(), "config.toml"))
	cfg.Models[0].Input = []string{"text", "image"}
	cfg.Compaction.Methods = []string{"snapcompact"}
	cfg.Compaction.KeepRecentTokens = 200
	cfg.Snapcompact.Summarize = false
	m := newManager(t, st, cfg)

	res, err := m.Compact(context.Background(), Request{
		SessionID: sessionID, Reason: "manual", ModelSupportsImages: true,
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !res.Compacted || res.Method != "snapcompact" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Frames) == 0 {
		t.Fatal("no frames")
	}
	for _, f := range res.Frames {
		art, data, err := st.ReadArtifact(f.ArtifactID)
		if err != nil {
			t.Fatalf("ReadArtifact: %v", err)
		}
		if art.Mime != "image/png" {
			t.Fatalf("mime = %q", art.Mime)
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Fatalf("frame %s is not a PNG: %v", art.ID, err)
		}
	}
	msgs := Project(&store.Compaction{Summary: res.Summary, Frames: store.EncodeFrames(res.Frames)},
		nil, ProjectOptions{ImageInput: true, LoadFrame: func(id string) (string, []byte, bool) {
			a, d, err := st.ReadArtifact(id)
			if err != nil {
				return "", nil, false
			}
			return a.Mime, d, true
		}})
	var images int
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == provider.BlockImage {
				images++
			}
		}
	}
	if images == 0 {
		t.Fatal("projection has no image blocks")
	}
}

func TestCompactShake(t *testing.T) {
	st, sessionID := newStore(t)
	seedConversation(t, st, sessionID)
	cfg := config.MockConfig(filepath.Join(t.TempDir(), "config.toml"))
	cfg.Compaction.Methods = []string{"shake"}
	cfg.Compaction.KeepRecentTokens = 10
	m := newManager(t, st, cfg)

	before, _ := st.AllEntries(sessionID)
	bigBefore := 0
	for _, e := range before {
		if e.Kind == store.KindToolResult {
			bigBefore += len(e.ToolResult().Text)
		}
	}
	res, err := m.Compact(context.Background(), Request{SessionID: sessionID, Reason: "manual"})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !res.Compacted || res.Method != "shake" {
		t.Fatalf("result = %+v", res)
	}
	after, _ := st.AllEntries(sessionID)
	bigAfter := 0
	elided := 0
	for _, e := range after {
		if e.Kind == store.KindToolResult {
			text := e.ToolResult().Text
			bigAfter += len(text)
			if strings.HasPrefix(text, "[elided") {
				elided++
			}
		}
	}
	if elided == 0 {
		t.Fatal("no tool result was elided")
	}
	if bigAfter >= bigBefore {
		t.Fatalf("content did not shrink: %d → %d", bigBefore, bigAfter)
	}
}

func TestProjectAttachments(t *testing.T) {
	mk := func(text string, ids []string) *store.Entry {
		return &store.Entry{
			Kind:    store.KindMessage,
			Role:    "user",
			Content: store.NewMessageContent(store.MessageContent{Text: text, Attachments: ids}),
			Seq:     1,
		}
	}
	// Vision model: the attachment becomes an image block.
	msgs := Project(nil, []*store.Entry{mk("look", []string{"art_1"})}, ProjectOptions{
		ImageInput: true,
		LoadFrame: func(id string) (string, []byte, bool) {
			return "image/png", []byte("PNG"), true
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	var images int
	for _, b := range msgs[0].Blocks {
		if b.Type == provider.BlockImage {
			images++
		}
	}
	if images != 1 {
		t.Fatalf("image blocks = %d, want 1 (%+v)", images, msgs[0].Blocks)
	}

	// Text-only model: the attachment degrades to an artifact reference.
	msgs = Project(nil, []*store.Entry{mk("look", []string{"art_1"})}, ProjectOptions{ImageInput: false})
	joined := ""
	for _, b := range msgs[0].Blocks {
		joined += b.Text
	}
	if !strings.Contains(joined, "artifact://art_1") {
		t.Fatalf("placeholder missing: %q", joined)
	}
}
