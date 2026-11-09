package search

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/tool"
)

func fsEnv(t *testing.T) (*Tool, *tool.Env, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"a.py":              "def alpha():\n    return 1  # TODO: doc\n",
		"b.py":              "def beta():\n    return 2\n",
		"sub/c.py":          "def gamma():\n    return 3  # TODO\n",
		"notes.txt":         "TODO: write docs\n",
		".hidden/d.py":      "def hidden(): pass\n",
		"vendor/e.py":       "def vendored(): pass\n",
		"node_modules/f.py": "def module(): pass\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	return NewTool(cfg), &tool.Env{CWD: dir, Config: cfg}, dir
}

func runSearch(t *testing.T, tl *Tool, env *tool.Env, args map[string]any) tool.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := tl.Run(context.Background(), tool.Call{ID: "1", Name: "search", Args: raw}, env)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return res
}

func TestFilesystemSearchContent(t *testing.T) {
	tl, env, _ := fsEnv(t)
	res := runSearch(t, tl, env, map[string]any{"query": "TODO", "path": ".", "file_glob": "**/*.py"})
	if res.IsError {
		t.Fatalf("search failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "a.py:2:") || !strings.Contains(res.Text, "sub/c.py:2:") {
		t.Fatalf("missing matches:\n%s", res.Text)
	}
	for _, unwanted := range []string{"notes.txt", "hidden/d.py", "vendor/", "node_modules/"} {
		if strings.Contains(res.Text, unwanted) {
			t.Fatalf("glob or skip rules leaked %q:\n%s", unwanted, res.Text)
		}
	}
}

func TestFilesystemSearchModeFiles(t *testing.T) {
	tl, env, _ := fsEnv(t)
	res := runSearch(t, tl, env, map[string]any{"query": "def", "file_glob": "*.py", "output_mode": "files"})
	if res.IsError {
		t.Fatalf("files mode failed: %s", res.Text)
	}
	lines := strings.Split(strings.TrimSpace(res.Text), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected several files, got %q", res.Text)
	}
	if strings.Contains(res.Text, "vendor") {
		t.Fatalf("vendor leaked:\n%s", res.Text)
	}
}

func TestFilesystemSearchNoMatch(t *testing.T) {
	tl, env, _ := fsEnv(t)
	res := runSearch(t, tl, env, map[string]any{"query": "zzz-not-there", "file_glob": "**/*.py"})
	if res.IsError || !res.Useless || !strings.Contains(res.Text, "no matches") {
		t.Fatalf("no-match result = %+v", res)
	}
}

func TestFilesystemSearchMissingPath(t *testing.T) {
	tl, env, _ := fsEnv(t)
	res := runSearch(t, tl, env, map[string]any{"query": "x", "path": "does-not-exist"})
	if !res.IsError || !strings.Contains(res.Text, "path not found") {
		t.Fatalf("missing path result = %+v", res)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"**/*.py", "a.py", true},
		{"**/*.py", "sub/deep/c.py", true},
		{"*.py", "a.py", true},
		// ripgrep-style: a bare glob matches the basename at any depth.
		{"*.py", "sub/c.py", true},
		{"*.go", "a.py", false},
		{"sub/*.py", "sub/c.py", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.rel); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}
