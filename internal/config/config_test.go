package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestInitWritesReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg, err := Init(path, InitOptions{
		Provider: "mock", Kind: "mock", BaseURL: "mock://",
		Model: "mock", ContextWindow: 128000, MaxOutput: 4096, Input: []string{"text"},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(cfg.Models) != 1 || cfg.Roles.Main[0] != "mock/mock" {
		t.Fatalf("config = %+v", cfg)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o444 {
		t.Fatalf("mode = %o, want 0444", mode)
	}
	// Reloading must round-trip.
	again, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if again.Models[0].Ref != "mock/mock" {
		t.Fatalf("reloaded ref = %q", again.Models[0].Ref)
	}
}

func TestSetTypesAndValidation(t *testing.T) {
	path := writeConfig(t, string(validConfig(t)))
	if _, err := Set(path, "agent.max_turns", "3"); err != nil {
		t.Fatalf("Set int: %v", err)
	}
	v, err := Get(path, "agent.max_turns")
	if err != nil || v != "3" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if _, err := Set(path, "agent.max_turns", "three"); err == nil || !strings.Contains(err.Error(), "expected integer") {
		t.Fatalf("wrong-type error = %v", err)
	}
	if _, err := Set(path, "version", "2"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("version error = %v", err)
	}
	if _, err := Set(path, "compaction.threshold", "2.0"); err == nil {
		t.Fatal("out-of-range threshold accepted")
	}
	if _, err := Set(path, "nope.key", "1"); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := Set(path, "compaction.methods", `["llm","bogus"]`); err == nil {
		t.Fatal("invalid method accepted")
	}
	if _, err := Set(path, "compaction.methods", `["snapcompact","llm"]`); err != nil {
		t.Fatalf("Set array: %v", err)
	}
	v, _ = Get(path, "compaction.methods")
	if !strings.Contains(v, "snapcompact") {
		t.Fatalf("methods = %q", v)
	}
	// A failed set must leave the file untouched.
	before, _ := os.ReadFile(path)
	if _, err := Set(path, "roles.main", `["mock/missing"]`); err == nil {
		t.Fatal("unknown role ref accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed set modified the file")
	}
	// Every write leaves a 0444 config plus a backup.
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o444 {
		t.Fatalf("mode after set = %o", st.Mode().Perm())
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatal("backup missing")
	}
}

func TestUnset(t *testing.T) {
	path := writeConfig(t, string(validConfig(t)))
	if _, err := Set(path, "ui.theme", `"dark"`); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := Unset(path, "ui.theme"); err != nil {
		t.Fatalf("Unset: %v", err)
	}
	if _, err := Get(path, "ui.theme"); err == nil {
		t.Fatal("key still present after unset")
	}
	// Unset falls back to the default value.
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.Theme != "auto" {
		t.Fatalf("theme = %q, want the default", cfg.UI.Theme)
	}
}

func TestUnknownKeysReported(t *testing.T) {
	body := strings.Replace(string(validConfig(t)), "[agent]", "[agent]\nmystery = 1", 1)
	path := writeConfig(t, body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, k := range cfg.Unknown {
		if k == "agent.mystery" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown keys = %v", cfg.Unknown)
	}
}

func TestValidateErrors(t *testing.T) {
	c := Default()
	c.Version = 2
	if err := Validate(c); err == nil {
		t.Fatal("bad version accepted")
	}
	c = Default()
	c.Roles.Main = []string{"nope/x"}
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("roles error = %v", err)
	}
	c = Default()
	c.Background.MaxRunningJobs = 0
	if err := Validate(c); err == nil {
		t.Fatal("zero max_running_jobs accepted")
	}
	c = Default()
	c.Subagents.MaxDepth = 3
	if err := Validate(c); err == nil {
		t.Fatal("depth 3 accepted")
	}
	c = Default()
	c.Tools.Enabled = []string{"nope"}
	if err := Validate(c); err == nil {
		t.Fatal("unknown tool accepted")
	}
}

func TestAddAndRemoveModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := Init(path, InitOptions{
		Provider: "mock", Kind: "mock", BaseURL: "mock://",
		Model: "mock", ContextWindow: 128000, MaxOutput: 4096, Input: []string{"text"},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg, err := LoadOrInit(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers["other"] = Provider{Kind: "openai-chat", BaseURL: "https://example.invalid/v1", APIKeyEnv: "X"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := AddModel(path, ModelSpec{
		Ref: "other/m", Provider: "other", Model: "m", ContextWindow: 8000, MaxOutput: 1000,
		Input: []string{"text"}, Roles: []string{"main", "compact"},
	}); err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	loaded, _ := Load(path)
	if len(loaded.Models) != 2 || loaded.Models[1].Ref != "other/m" {
		t.Fatalf("models = %+v", loaded.Models)
	}
	if len(loaded.Roles.Compact) != 2 || loaded.Roles.Compact[1] != "other/m" {
		t.Fatalf("roles.compact = %v", loaded.Roles.Compact)
	}
	if _, err := RemoveModel(path, "other/m"); err != nil {
		t.Fatalf("RemoveModel: %v", err)
	}
	loaded, _ = Load(path)
	if len(loaded.Models) != 1 || len(loaded.Roles.Compact) != 1 || loaded.Roles.Compact[0] == "other/m" {
		t.Fatalf("after remove: models=%d compact=%v", len(loaded.Models), loaded.Roles.Compact)
	}
}

func TestAuthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.env")
	if err := SetAuth(path, "PIG_TEST_KEY", "sup3rsecret"); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	vals, names, err := ReadAuth(path)
	if err != nil || vals["PIG_TEST_KEY"] != "sup3rsecret" || len(names) != 1 {
		t.Fatalf("ReadAuth = %v %v %v", vals, names, err)
	}
	secrets := Secrets(path, nil)
	redacted := Redact("token=sup3rsecret", secrets)
	if redacted != "token=***" {
		t.Fatalf("redact = %q", redacted)
	}
	if err := RemoveAuth(path, "PIG_TEST_KEY"); err != nil {
		t.Fatalf("RemoveAuth: %v", err)
	}
	if err := RemoveAuth(path, "PIG_TEST_KEY"); err == nil {
		t.Fatal("removing a missing secret succeeded")
	}
}

func TestPaths(t *testing.T) {
	t.Setenv("PIG_HOME", "/tmp/pig-home")
	p, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.Config() != "/tmp/pig-home/config.toml" || p.DB() != "/tmp/pig-home/pig.db" {
		t.Fatalf("paths = %+v", p)
	}
	if p.Socket("") != "/tmp/pig-home/pig.sock" || p.Socket("/run/x.sock") != "/run/x.sock" {
		t.Fatalf("socket = %q", p.Socket(""))
	}
	t.Setenv("PIG_HOME", "relative")
	if _, err := Resolve(); err == nil {
		t.Fatal("relative PIG_HOME accepted")
	}
}

// validConfig returns a validated config body with one mock model.
func validConfig(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := Init(path, InitOptions{
		Provider: "mock", Kind: "mock", BaseURL: "mock://",
		Model: "mock", ContextWindow: 128000, MaxOutput: 4096, Input: []string{"text"},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return b
}
