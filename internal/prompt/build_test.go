package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/store"
)

func buildTest(t *testing.T, cwd string, cfg *config.Config) string {
	t.Helper()
	in := Input{
		Config:  cfg,
		Session: &store.Session{ID: "ses_test", CWD: cwd, Model: "mock/mock"},
		Tools:   []ToolDoc{{Name: "bash", Description: "run a command"}, {Name: "read", Description: "read a file"}},
		Skills:  []SkillRef{{Name: "demo", Description: "demo skill"}},
		Facts:   []string{"prefers Chinese"},
	}
	out, _ := Build(in)
	return out
}

func TestBuildVoice(t *testing.T) {
	dir := t.TempDir()
	out := buildTest(t, dir, nil)
	for _, want := range []string{"# Voice", "# How to reason out loud", "# Engineering", "未验证", "Answer first, evidence next"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(out, "{{") {
		t.Error("unreplaced placeholder")
	}
}

// The prompt cache keys on the prefix: nothing that varies per session may
// appear before the context-files section.
func TestBuildPrefixIsStable(t *testing.T) {
	a := buildTest(t, t.TempDir(), nil)
	b := buildTest(t, t.TempDir(), nil)
	cut := func(s string) string {
		if i := strings.Index(s, "# Context files"); i >= 0 {
			return s[:i]
		}
		return s
	}
	if cut(a) != cut(b) {
		t.Fatal("prompt prefix varies between sessions")
	}
	if !strings.HasSuffix(strings.TrimSpace(a), "# Context files") &&
		!strings.Contains(a[strings.Index(a, "# Context files"):], "(none)") {
		t.Fatal("context files are not the last section")
	}
}

// AGENTS.md from the session directory reaches the context section.
func TestBuildIncludesContextFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# project rules\nrun go test ./...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := buildTest(t, dir, nil)
	if !strings.Contains(out, "run go test ./...") {
		t.Fatal("context file missing")
	}
	if strings.Index(out, "# Context files") > strings.Index(out, "run go test ./...") {
		t.Fatal("context file before its section")
	}
}

func TestBuildSubagent(t *testing.T) {
	in := Input{Depth: 1, Task: "do the thing", Tools: []ToolDoc{{Name: "bash", Description: "run"}}}
	out, _ := Build(in)
	if !strings.Contains(out, "do the thing") || !strings.Contains(out, "subagent") {
		t.Fatalf("bad subagent prompt: %q", out)
	}
	if strings.Contains(out, "{{") {
		t.Error("unreplaced placeholder")
	}
}
