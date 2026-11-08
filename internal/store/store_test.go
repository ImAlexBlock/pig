package store

import (
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "pig.db"), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSchemaVersion(t *testing.T) {
	st := openTest(t)
	var v int
	if err := st.DB().QueryRow(`SELECT version FROM schema_meta`).Scan(&v); err != nil {
		t.Fatalf("schema_meta: %v", err)
	}
	if v != SchemaVersion {
		t.Fatalf("version = %d", v)
	}
}

func TestEntrySequence(t *testing.T) {
	st := openTest(t)
	sess, err := st.CreateSession(SessionOptions{CWD: "/tmp", Model: "mock/mock"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := range 5 {
		e, err := st.AppendEntry(sess.ID, KindMessage, "user", NewMessageContent(MessageContent{Text: "m"}), 1)
		if err != nil {
			t.Fatalf("AppendEntry: %v", err)
		}
		if e.Seq != i+1 {
			t.Fatalf("seq = %d, want %d", e.Seq, i+1)
		}
	}
	tail, err := st.TailEntries(sess.ID, 2)
	if err != nil {
		t.Fatalf("TailEntries: %v", err)
	}
	if len(tail) != 2 || tail[0].Seq != 4 || tail[1].Seq != 5 {
		t.Fatalf("tail = %+v", tail)
	}
	max, _ := st.MaxSeq(sess.ID)
	if max != 5 {
		t.Fatalf("MaxSeq = %d", max)
	}
}

func TestForkSession(t *testing.T) {
	st := openTest(t)
	sess, err := st.CreateSession(SessionOptions{CWD: "/tmp", Model: "mock/mock"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := range 4 {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		if _, err := st.AppendEntry(sess.ID, KindMessage, role, NewMessageContent(MessageContent{Text: "m"}), 1); err != nil {
			t.Fatalf("AppendEntry: %v", err)
		}
	}
	child, err := st.ForkSession(sess.ID, 3)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if child.ParentID != sess.ID || child.ForkedFromSeq != 3 || child.Depth != sess.Depth {
		t.Fatalf("child = %+v", child)
	}
	entries, err := st.AllEntries(child.ID)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("copied %d entries, want 3", len(entries))
	}
	if entries[0].ID == "" || entries[0].Seq != 1 {
		t.Fatalf("copied entry = %+v", entries[0])
	}
	if entries[0].ID == "" {
		t.Fatal("copied entry kept an empty id")
	}
	// The child must be independent: new entries start after the copied ones.
	e, err := st.AppendEntry(child.ID, KindMessage, "user", NewMessageContent(MessageContent{Text: "next"}), 1)
	if err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}
	if e.Seq != 4 {
		t.Fatalf("new seq = %d, want 4", e.Seq)
	}
	parent, _ := st.AllEntries(sess.ID)
	if len(parent) != 4 {
		t.Fatalf("parent entries changed: %d", len(parent))
	}
}

func TestArtifacts(t *testing.T) {
	st := openTest(t)
	a, err := st.PutArtifact("ses_x", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	meta, data, err := st.ReadArtifact(a.ID)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(data) != "hello" || meta.Mime != "text/plain" || meta.Bytes != 5 {
		t.Fatalf("meta = %+v data = %q", meta, data)
	}
	if _, _, err := st.ReadArtifact("art_missing"); err == nil {
		t.Fatal("missing artifact returned data")
	}
}

func TestJobsLifecycle(t *testing.T) {
	st := openTest(t)
	sess, _ := st.CreateSession(SessionOptions{CWD: "/tmp", Model: "mock/mock"})
	j := &Job{Kind: "task", SessionID: sess.ID, Prompt: "p", Priority: 1}
	if err := st.CreateJob(j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if j.ID == "" || j.Status != JobQueued {
		t.Fatalf("job = %+v", j)
	}
	queued, running, err := st.QueueDepth()
	if err != nil || queued != 1 || running != 0 {
		t.Fatalf("depth = %d/%d err=%v", queued, running, err)
	}
	if err := st.SetJobStatus(j.ID, JobRunning); err != nil {
		t.Fatalf("SetJobStatus: %v", err)
	}
	n, err := st.RequeueInterrupted()
	if err != nil || n != 1 {
		t.Fatalf("RequeueInterrupted = %d, %v", n, err)
	}
	got, err := st.GetJob(j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != JobInterrupted {
		t.Fatalf("status = %s", got.Status)
	}
	if err := st.RetryJob(j.ID); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	got, _ = st.GetJob(j.ID)
	if got.Status != JobQueued {
		t.Fatalf("retried status = %s", got.Status)
	}
	if err := st.FinishJob(j.ID, JobDone, "result", ""); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	got, _ = st.GetJob(j.ID)
	if got.Status != JobDone || got.Result != "result" || got.EndedAt == 0 {
		t.Fatalf("finished job = %+v", got)
	}
	if err := st.RetryJob(j.ID); err == nil {
		t.Fatal("retry of a done job succeeded")
	}
}

func TestUsageAndRTK(t *testing.T) {
	st := openTest(t)
	if err := st.RecordUsage(&Usage{SessionID: "ses_x", Provider: "p", Model: "m", Input: 10, Output: 5, CostUSD: 0.25}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	totals, err := st.SessionUsage("ses_x")
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if totals.Input != 10 || totals.Output != 5 || totals.Requests != 1 {
		t.Fatalf("totals = %+v", totals)
	}
	if err := st.RecordRTK("ses_x", "git status", "git-status", 1000, 200); err != nil {
		t.Fatalf("RecordRTK: %v", err)
	}
	g, err := st.RTKGain()
	if err != nil {
		t.Fatalf("RTKGain: %v", err)
	}
	if g.Calls != 1 || g.SavedBytes != 800 {
		t.Fatalf("gain = %+v", g)
	}
}

func TestChannelState(t *testing.T) {
	st := openTest(t)
	cs := &ChannelState{Channel: "telegram", ChatID: "1", SessionID: "ses_x", Paired: true}
	if err := st.SetChannelState(cs); err != nil {
		t.Fatalf("SetChannelState: %v", err)
	}
	got, err := st.GetChannelState("telegram", "1")
	if err != nil {
		t.Fatalf("GetChannelState: %v", err)
	}
	if !got.Paired || got.SessionID != "ses_x" {
		t.Fatalf("state = %+v", got)
	}
	cs.SessionID = "ses_y"
	if err := st.SetChannelState(cs); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = st.GetChannelState("telegram", "1")
	if got.SessionID != "ses_y" {
		t.Fatalf("upsert failed: %+v", got)
	}
	if _, err := st.GetChannelState("telegram", "missing"); err == nil {
		t.Fatal("missing binding returned state")
	}
}

// A version 1 database gains the channel_state.lang column on open and keeps
// its existing rows.
func TestMigrateV1ToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pig.db")
	st, err := Open(path, dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE channel_state DROP COLUMN lang`); err != nil {
		t.Fatalf("drop lang: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE schema_meta SET version=1`); err != nil {
		t.Fatalf("version: %v", err)
	}
	if _, err := st.DB().Exec(`INSERT INTO channel_state(channel,chat_id,session_id,paired,updated_at)
		VALUES ('qq','u:1','s1',1,5)`); err != nil {
		t.Fatalf("insert v1 row: %v", err)
	}
	st.Close()

	up, err := Open(path, dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.DB().QueryRow(`SELECT version FROM schema_meta`).Scan(&v); err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != SchemaVersion {
		t.Fatalf("version = %d, want %d", v, SchemaVersion)
	}
	cs, err := up.GetChannelState("qq", "u:1")
	if err != nil {
		t.Fatalf("GetChannelState: %v", err)
	}
	if !cs.Paired || cs.SessionID != "s1" || cs.Lang != "" {
		t.Fatalf("row = %+v", cs)
	}
	if err := up.SetChannelState(&ChannelState{Channel: "qq", ChatID: "u:1", SessionID: "s1", Paired: true, Lang: "zh-Hant"}); err != nil {
		t.Fatalf("SetChannelState: %v", err)
	}
	again, err := up.GetChannelState("qq", "u:1")
	if err != nil || again.Lang != "zh-Hant" {
		t.Fatalf("lang = %q, %v", again.Lang, err)
	}
}
