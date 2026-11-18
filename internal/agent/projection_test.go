package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ImAlexBlock/pig/internal/compact"
	"github.com/ImAlexBlock/pig/internal/provider"
	"github.com/ImAlexBlock/pig/internal/store"
)

func projectionAgent(t *testing.T) (*Agent, string) {
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
	return &Agent{Store: st}, sess.ID
}

// fullProjection rebuilds the messages the way the loop used to: read every
// entry and project the compaction window in one pass.
func fullProjection(t *testing.T, st *store.Store, sessionID string, opt compact.ProjectOptions) []provider.Message {
	t.Helper()
	entries, err := st.AllEntries(sessionID)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	cmp, _ := st.LatestCompaction(sessionID)
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
	return compact.Project(cmp, window, opt)
}

func assertProjectionEqual(t *testing.T, step int, got, want []provider.Message) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("step %d: incremental projection differs from full rebuild\n got: %#v\nwant: %#v", step, got, want)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("step %d: marshal incremental: %v", step, err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("step %d: marshal full: %v", step, err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("step %d: incremental projection is not byte-identical\n got: %s\nwant: %s", step, gotJSON, wantJSON)
	}
}

func TestProjectedMessagesIncrementalMatchesFullRebuild(t *testing.T) {
	a, sessionID := projectionAgent(t)
	opt := compact.ProjectOptions{
		ImageInput: true,
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
	}
	st := a.Store
	image, err := st.PutArtifact(sessionID, "image/png", []byte("not-a-real-png"))
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	appendEntry := func(kind, role, content string) *store.Entry {
		t.Helper()
		e, err := st.AppendEntry(sessionID, kind, role, content, 1)
		if err != nil {
			t.Fatalf("AppendEntry(%s): %v", kind, err)
		}
		return e
	}

	run := &runState{}
	checks := 0
	var userSeqs []int
	check := func() {
		checks++
		got, err := a.projectedMessages(run, sessionID, opt)
		if err != nil {
			t.Fatalf("step %d: projectedMessages: %v", checks, err)
		}
		assertProjectionEqual(t, checks, got, fullProjection(t, st, sessionID, opt))
	}

	// ~48 mixed entries: user/assistant text, tool calls (with and without
	// args), tool results, notices, a job result and an image attachment.
	for i := range 18 {
		switch i % 4 {
		case 0:
			e := appendEntry(store.KindMessage, "user", store.NewMessageContent(store.MessageContent{
				Text: "user turn " + string(rune('a'+i)),
			}))
			userSeqs = append(userSeqs, e.Seq)
			check()
		case 1:
			raw := json.RawMessage(nil)
			if i%8 == 1 {
				raw = json.RawMessage(`{"content":[{"type":"text","text":"assistant turn"}]}`)
			}
			appendEntry(store.KindMessage, "assistant", store.NewMessageContent(store.MessageContent{
				Text: "assistant turn " + string(rune('a'+i)), Raw: raw,
			}))
			check()
			if i%8 == 1 {
				// Batch boundary between a raw assistant message and its call.
				appendEntry(store.KindToolCall, "", store.NewToolCallContent(store.ToolCallContent{
					ID: "call_" + string(rune('a'+i)), Name: "bash",
					Args: json.RawMessage(`{"command":"ls"}`),
				}))
				check()
			} else {
				appendEntry(store.KindToolCall, "", store.NewToolCallContent(store.ToolCallContent{
					ID: "call_" + string(rune('a'+i)), Name: "read",
				}))
				check()
			}
			appendEntry(store.KindToolResult, "", store.NewToolResultContent(store.ToolResultContent{
				CallID: "call_" + string(rune('a'+i)), Name: "bash",
				Text: "tool output " + string(rune('a'+i)), IsError: i%8 == 5,
			}))
			check()
		case 2:
			e := appendEntry(store.KindMessage, "user", store.NewMessageContent(store.MessageContent{
				Text:        "look at this",
				Attachments: []string{image.ID},
			}))
			userSeqs = append(userSeqs, e.Seq)
			check()
			appendEntry(store.KindNotice, "", store.NewNoticeContent(store.NoticeContent{
				Level: "info", Text: "notice " + string(rune('a'+i)),
			}))
			check()
		case 3:
			appendEntry(store.KindToolResult, "", store.NewToolResultContent(store.ToolResultContent{
				CallID: "call_" + string(rune('a'+i)), Name: "bash",
				Text: "late result " + string(rune('a'+i)),
			}))
			check()
			appendEntry(store.KindJobResult, "", store.NewJobResultContent(store.JobResultContent{
				JobID: "job_" + string(rune('a'+i)), Status: "done", Text: "job output",
			}))
			check()
			appendEntry(store.KindTodo, "", store.NewTodoContent(store.TodoContent{
				Phases: []store.TodoPhase{{Name: "phase", Items: []store.TodoItem{{Text: "item", Status: "open"}}}},
			}))
			check()
		}
	}
	if checks < 40 {
		t.Fatalf("only %d projection steps; want at least 40", checks)
	}

	// A compaction appears mid-stream: its identity must force a full rebuild,
	// and later appends must go back to incremental projection.
	keep := userSeqs[len(userSeqs)-2]
	appendEntry(store.KindCompaction, "", store.NewCompactionContent(store.CompactionContent{CompactionID: "cmp_test"}))
	cmp := &store.Compaction{
		ID:           "cmp_test",
		SessionID:    sessionID,
		Method:       "snapcompact",
		Summary:      "earlier context archived",
		FirstKeptSeq: keep,
		Frames:       "[]",
	}
	if err := st.AppendCompaction(cmp); err != nil {
		t.Fatalf("AppendCompaction: %v", err)
	}
	check()
	if got, want := run.cmpID, "cmp_test"; got != want {
		t.Fatalf("cmpID = %q, want %q", got, want)
	}
	if got, want := run.cmpKeepSeq, keep; got != want {
		t.Fatalf("cmpKeepSeq = %d, want %d", got, want)
	}

	for i := range 3 {
		callID := fmt.Sprintf("post_%d", i)
		appendEntry(store.KindMessage, "user", store.NewMessageContent(store.MessageContent{Text: "after compaction"}))
		check()
		appendEntry(store.KindMessage, "assistant", store.NewMessageContent(store.MessageContent{Text: "reply"}))
		check()
		appendEntry(store.KindToolCall, "", store.NewToolCallContent(store.ToolCallContent{ID: callID, Name: "bash"}))
		check()
		appendEntry(store.KindToolResult, "", store.NewToolResultContent(store.ToolResultContent{CallID: callID, Name: "bash", Text: "ok"}))
		check()
	}
}

func TestProjectedMessagesCompactionOnlyBatchAdvancesSeq(t *testing.T) {
	a, sessionID := projectionAgent(t)
	st := a.Store
	run := &runState{}
	opt := compact.ProjectOptions{}

	if _, err := st.AppendEntry(sessionID, store.KindMessage, "user", store.NewMessageContent(store.MessageContent{Text: "hi"}), 1); err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if _, err := a.projectedMessages(run, sessionID, opt); err != nil {
		t.Fatalf("projectedMessages: %v", err)
	}
	e, err := st.AppendEntry(sessionID, store.KindCompaction, "", store.NewCompactionContent(store.CompactionContent{CompactionID: "c1"}), 0)
	if err != nil {
		t.Fatalf("AppendEntry compaction: %v", err)
	}
	// The compaction row shares the identity of the entry, so the window is
	// unchanged and only the new entry is projected (to nothing).
	if err := st.AppendCompaction(&store.Compaction{
		ID: "c1", SessionID: sessionID, EntryID: e.ID, Method: "shake",
		FirstKeptSeq: -1, Frames: "[]",
	}); err != nil {
		t.Fatalf("AppendCompaction: %v", err)
	}
	got, err := a.projectedMessages(run, sessionID, opt)
	if err != nil {
		t.Fatalf("projectedMessages: %v", err)
	}
	if run.projectedSeq != e.Seq {
		t.Fatalf("projectedSeq = %d, want %d", run.projectedSeq, e.Seq)
	}
	assertProjectionEqual(t, 1, got, fullProjection(t, st, sessionID, opt))
}

// A run can end with an open tool exchange (the call persisted, the result
// not). The next turn must still re-project that suffix rather than reuse a
// cache that silently dropped the call, and a fresh run must rebuild from the
// entries.
func TestProjectedMessagesOpenSuffixAcrossTurns(t *testing.T) {
	a, sessionID := projectionAgent(t)
	st := a.Store
	opt := compact.ProjectOptions{}
	appendEntry := func(kind, role, content string) *store.Entry {
		t.Helper()
		e, err := st.AppendEntry(sessionID, kind, role, content, 1)
		if err != nil {
			t.Fatalf("AppendEntry(%s): %v", kind, err)
		}
		return e
	}
	hasToolUse := func(msgs []provider.Message) bool {
		for _, m := range msgs {
			for _, b := range m.Blocks {
				if b.Type == provider.BlockToolUse {
					return true
				}
			}
		}
		return false
	}

	appendEntry(store.KindMessage, "user", store.NewMessageContent(store.MessageContent{Text: "task"}))
	appendEntry(store.KindMessage, "assistant", store.NewMessageContent(store.MessageContent{
		Text: "running", Raw: json.RawMessage(`{"content":[{"type":"text","text":"running"}]}`),
	}))
	appendEntry(store.KindToolCall, "", store.NewToolCallContent(store.ToolCallContent{
		ID: "open", Name: "bash", Args: json.RawMessage(`{"command":"sleep 1"}`),
	}))

	run := &runState{}
	got, err := a.projectedMessages(run, sessionID, opt)
	if err != nil {
		t.Fatalf("projectedMessages: %v", err)
	}
	assertProjectionEqual(t, 1, got, fullProjection(t, st, sessionID, opt))
	if len(run.pending) == 0 {
		t.Fatal("open suffix was folded into the cache")
	}
	if hasToolUse(got) {
		t.Fatal("dangling tool call reached the request")
	}

	// The result arrives in the next turn: the exchange closes and the view
	// keeps the call again.
	appendEntry(store.KindToolResult, "", store.NewToolResultContent(store.ToolResultContent{
		CallID: "open", Name: "bash", Text: "done",
	}))
	got, err = a.projectedMessages(run, sessionID, opt)
	if err != nil {
		t.Fatalf("projectedMessages: %v", err)
	}
	assertProjectionEqual(t, 2, got, fullProjection(t, st, sessionID, opt))
	if !hasToolUse(got) {
		t.Fatal("closed exchange lost its tool call")
	}

	// A later assistant message lets the closed suffix fold; the cache keeps
	// only the newest assistant message pending.
	newest := appendEntry(store.KindMessage, "assistant", store.NewMessageContent(store.MessageContent{Text: "next"}))
	got, err = a.projectedMessages(run, sessionID, opt)
	if err != nil {
		t.Fatalf("projectedMessages: %v", err)
	}
	assertProjectionEqual(t, 3, got, fullProjection(t, st, sessionID, opt))
	if len(run.pending) != 1 || run.pending[0].Seq != newest.Seq {
		t.Fatalf("pending = %d entries, want just assistant seq %d", len(run.pending), newest.Seq)
	}

	// A run that ends here starts over from the store on the next run.
	fresh := &runState{}
	got, err = a.projectedMessages(fresh, sessionID, opt)
	if err != nil {
		t.Fatalf("projectedMessages fresh: %v", err)
	}
	assertProjectionEqual(t, 4, got, fullProjection(t, st, sessionID, opt))
}
