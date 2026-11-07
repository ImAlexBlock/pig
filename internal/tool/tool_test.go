package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/internalurl"
	"github.com/alexblock/pig/internal/store"
)

func testEnv(t *testing.T) (*Env, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.MockConfig(filepath.Join(dir, "config.toml"))
	st, err := store.Open(filepath.Join(dir, "pig.db"), dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	sess, err := st.CreateSession(store.SessionOptions{CWD: dir, Model: "mock/mock"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	env := &Env{
		SessionID:      sess.ID,
		CWD:            dir,
		Store:          st,
		Config:         cfg,
		Resolver:       &internalurl.Resolver{Store: st, SessionID: sess.ID},
		ReadFiles:      NewReadSet(),
		MaxOutputBytes: 65536,
		BashTimeout:    30 * time.Second,
	}
	return env, dir
}

func call(t *testing.T, tool Tool, args any, env *Env) Result {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := tool.Run(context.Background(), Call{ID: "c1", Name: tool.Name(), Args: raw}, env)
	if err != nil {
		t.Fatalf("%s: %v", tool.Name(), err)
	}
	return res
}

func TestReadNumberedAndRaw(t *testing.T) {
	env, dir := testEnv(t)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := call(t, readTool{}, map[string]any{"path": "a.txt"}, env)
	if res.IsError {
		t.Fatalf("read failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "     1| one") {
		t.Fatalf("line numbers missing:\n%s", res.Text)
	}
	raw := call(t, readTool{}, map[string]any{"path": "a.txt:raw"}, env)
	if strings.Contains(raw.Text, "| one") {
		t.Fatalf(":raw kept line numbers:\n%s", raw.Text)
	}
	part := call(t, readTool{}, map[string]any{"path": "a.txt:2-2"}, env)
	if !strings.Contains(part.Text, "     2| two") || strings.Contains(part.Text, "three") {
		t.Fatalf("selector failed:\n%s", part.Text)
	}
	missing := call(t, readTool{}, map[string]any{"path": "nope.txt"}, env)
	if !missing.IsError || !strings.Contains(missing.Text, "file not found") {
		t.Fatalf("missing file result = %+v", missing)
	}
}

func TestReadDirectory(t *testing.T) {
	env, dir := testEnv(t)
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := call(t, readTool{}, map[string]any{"path": "."}, env)
	if !strings.Contains(res.Text, "x.go") || !strings.Contains(res.Text, "sub/") {
		t.Fatalf("listing = %q", res.Text)
	}
}

func TestWriteRequiresRead(t *testing.T) {
	env, dir := testEnv(t)
	path := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked := call(t, writeTool{}, map[string]any{"path": "b.txt", "content": "new\n"}, env)
	if !blocked.IsError || !strings.Contains(blocked.Text, "read the file") {
		t.Fatalf("unread overwrite allowed: %+v", blocked)
	}
	call(t, readTool{}, map[string]any{"path": "b.txt"}, env)
	ok := call(t, writeTool{}, map[string]any{"path": "b.txt", "content": "new\n"}, env)
	if ok.IsError {
		t.Fatalf("write failed: %s", ok.Text)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new\n" {
		t.Fatalf("content = %q", got)
	}
	nested := call(t, writeTool{}, map[string]any{"path": "deep/dir/c.txt", "content": "hi"}, env)
	if nested.IsError {
		t.Fatalf("nested write failed: %s", nested.Text)
	}
	if _, err := os.Stat(filepath.Join(dir, "deep", "dir", "c.txt")); err != nil {
		t.Fatal("nested file missing")
	}
}

func TestWriteProtectsConfig(t *testing.T) {
	env, _ := testEnv(t)
	res := call(t, writeTool{}, map[string]any{"path": env.Config.Path, "content": "x"}, env)
	if !res.IsError || !strings.Contains(res.Text, `use "pig config set"`) {
		t.Fatalf("config write not protected: %+v", res)
	}
	auth := filepath.Join(filepath.Dir(env.Config.Path), "auth.env")
	res = call(t, writeTool{}, map[string]any{"path": auth, "content": "x"}, env)
	if !res.IsError || !strings.Contains(res.Text, `use "pig auth"`) {
		t.Fatalf("auth write not protected: %+v", res)
	}
}

func TestEditUniquenessAndWhitespace(t *testing.T) {
	env, dir := testEnv(t)
	path := filepath.Join(dir, "e.txt")
	if err := os.WriteFile(path, []byte("alpha   \nbeta\nalpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := call(t, editTool{}, map[string]any{"path": "e.txt", "old_string": "beta", "new_string": "B"}, env); !res.IsError {
		t.Fatalf("edit without reading succeeded: %+v", res)
	}
	call(t, readTool{}, map[string]any{"path": "e.txt"}, env)

	notUnique := call(t, editTool{}, map[string]any{"path": "e.txt", "old_string": "alpha", "new_string": "A"}, env)
	if !notUnique.IsError || !strings.Contains(notUnique.Text, "not unique") {
		t.Fatalf("ambiguous edit result = %+v", notUnique)
	}

	// The file has trailing spaces after "alpha"; matching on the trimmed form
	// must still find it and replace the original slice.
	fuzzy := call(t, editTool{}, map[string]any{"path": "e.txt", "old_string": "alpha\nbeta", "new_string": "ONE"}, env)
	if fuzzy.IsError {
		t.Fatalf("fuzzy edit failed: %s", fuzzy.Text)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "ONE\nalpha\n" {
		t.Fatalf("content = %q", got)
	}

	miss := call(t, editTool{}, map[string]any{"path": "e.txt", "old_string": "zzz", "new_string": "y"}, env)
	if !miss.IsError || !strings.Contains(miss.Text, "not found") {
		t.Fatalf("missing old_string result = %+v", miss)
	}
}

func TestBashExitCodeAndRTK(t *testing.T) {
	env, dir := testEnv(t)
	res := call(t, bashTool{}, map[string]any{"command": "echo hi"}, env)
	if res.IsError {
		t.Fatalf("bash failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "hi") || !strings.Contains(res.Text, "[exit 0]") {
		t.Fatalf("bash output = %q", res.Text)
	}
	bad := call(t, bashTool{}, map[string]any{"command": "exit 3"}, env)
	if !bad.IsError || !strings.Contains(bad.Text, "[exit 3]") {
		t.Fatalf("failing bash = %+v", bad)
	}
	// RTK rewrites simple git status calls.
	gitDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	init := call(t, bashTool{}, map[string]any{"command": "git init -q . && echo a > a.txt", "cwd": gitDir}, env)
	if init.IsError {
		t.Skipf("git unavailable: %s", init.Text)
	}
	status := call(t, bashTool{}, map[string]any{"command": "git status", "cwd": gitDir}, env)
	if !strings.Contains(status.Text, "??") {
		t.Fatalf("rtk-filtered status = %q", status.Text)
	}
}

func TestBashTimeout(t *testing.T) {
	env, _ := testEnv(t)
	start := time.Now()
	res := call(t, bashTool{}, map[string]any{"command": "sleep 5", "timeout_seconds": 1}, env)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if !strings.Contains(res.Text, "[timed out") {
		t.Fatalf("timeout note missing: %q", res.Text)
	}
}

func TestTodoPersists(t *testing.T) {
	env, _ := testEnv(t)
	var got []store.TodoPhase
	env.EmitTodo = func(phases []store.TodoPhase) error {
		got = phases
		return nil
	}
	res := call(t, todoTool{}, map[string]any{"phases": []map[string]any{
		{"name": "phase 1", "items": []map[string]any{
			{"text": "step one", "status": "active"},
			{"text": "step two"},
		}},
	}}, env)
	if res.IsError {
		t.Fatalf("todo failed: %s", res.Text)
	}
	if len(got) != 1 || got[0].Items[1].Status != "pending" {
		t.Fatalf("emitted todo = %+v", got)
	}
	entries, _ := env.Store.AllEntries(env.SessionID)
	last := entries[len(entries)-1]
	if last.Kind != store.KindTodo {
		t.Fatalf("last entry kind = %s", last.Kind)
	}
	bad := call(t, todoTool{}, map[string]any{"phases": []map[string]any{
		{"name": "p", "items": []map[string]any{{"text": "x", "status": "weird"}}},
	}}, env)
	if !bad.IsError {
		t.Fatal("invalid status accepted")
	}
}

func TestRegistrySpecs(t *testing.T) {
	reg := Builtin()
	names := reg.Names()
	want := map[string]bool{"read": true, "write": true, "edit": true, "bash": true, "todo": true, "task": true, "job": true}
	for _, n := range names {
		delete(want, n)
	}
	if len(want) != 0 {
		t.Fatalf("missing builtin tools: %v", want)
	}
	for _, n := range []string{"read", "bash", "edit"} {
		tl, ok := reg.Get(n)
		if !ok {
			t.Fatalf("%s missing", n)
		}
		var schema map[string]any
		if err := json.Unmarshal(tl.Schema(), &schema); err != nil {
			t.Fatalf("%s schema is not JSON: %v", n, err)
		}
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %v", n, schema["type"])
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s schema allows extra properties", n)
		}
	}
}

func TestMemoryTool(t *testing.T) {
	env, _ := testEnv(t)
	add := call(t, memoryTool{}, map[string]any{"action": "add", "key": "timezone", "value": "Asia/Shanghai"}, env)
	if add.IsError || !strings.Contains(add.Text, "remembered timezone") {
		t.Fatalf("add = %+v", add)
	}
	// Upsert keeps one row.
	call(t, memoryTool{}, map[string]any{"action": "add", "key": "timezone", "value": "UTC"}, env)
	n, err := env.Store.CountFacts()
	if err != nil || n != 1 {
		t.Fatalf("facts = %d, %v", n, err)
	}
	recall := call(t, memoryTool{}, map[string]any{"action": "recall", "query": "utc"}, env)
	if !strings.Contains(recall.Text, "timezone: UTC") {
		t.Fatalf("recall = %q", recall.Text)
	}
	list := call(t, memoryTool{}, map[string]any{"action": "list"}, env)
	if !strings.Contains(list.Text, "timezone") {
		t.Fatalf("list = %q", list.Text)
	}
	if res := call(t, memoryTool{}, map[string]any{"action": "remove", "key": "timezone"}, env); res.IsError {
		t.Fatalf("remove failed: %s", res.Text)
	}
	if _, err := env.Store.GetFact("timezone"); err == nil {
		t.Fatal("fact survived remove")
	}
	if res := call(t, memoryTool{}, map[string]any{"action": "add", "key": "", "value": "x"}, env); !res.IsError {
		t.Fatal("empty key accepted")
	}
	if res := call(t, memoryTool{}, map[string]any{"action": "add", "key": "k", "value": strings.Repeat("x", MaxFactValue+1)}, env); !res.IsError {
		t.Fatal("oversized value accepted")
	}
}

func TestMemoryStoreCap(t *testing.T) {
	env, _ := testEnv(t)
	for i := 0; i < MaxFactStore+5; i++ {
		key := fmt.Sprintf("k%03d", i)
		if res := call(t, memoryTool{}, map[string]any{"action": "add", "key": key, "value": "v"}, env); res.IsError {
			t.Fatalf("add %s: %s", key, res.Text)
		}
	}
	n, err := env.Store.CountFacts()
	if err != nil {
		t.Fatal(err)
	}
	if n > MaxFactStore {
		t.Fatalf("facts = %d, want <= %d", n, MaxFactStore)
	}
}
