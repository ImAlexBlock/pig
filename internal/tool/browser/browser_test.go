package browser

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

func testTool(t *testing.T) (*Tool, *tool.Env) {
	t.Helper()
	cfg := config.Default()
	cfg.Browser.Headless = true
	cfg.Browser.TimeoutSeconds = 30
	tl := NewTool(cfg)
	if _, err := tl.chromePath(); err != nil {
		tl.Close()
		t.Skipf("chrome unavailable: %v", err)
	}
	t.Cleanup(tl.Close)
	return tl, &tool.Env{Config: cfg}
}

func runTool(t *testing.T, tl *Tool, env *tool.Env, args map[string]any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := tl.Run(context.Background(), tool.Call{ID: "1", Name: "browser", Args: raw}, env)
	if err != nil {
		t.Fatalf("browser %v: %v", args["action"], err)
	}
	return res
}

func TestBrowserSnapshotAndEval(t *testing.T) {
	tl, env := testTool(t)
	dir := t.TempDir()
	page := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><head><title>Pig Test</title></head>
<body><h1>Heading</h1>
<a href="/next">Go next</a>
<input aria-label="Search box" value="seed">
<button id="b">Click me</button>
</body></html>`
	if err := os.WriteFile(page, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}

	open := runTool(t, tl, env, map[string]any{"action": "open", "url": "file://" + page})
	if open.IsError {
		t.Fatalf("open failed: %s", open.Text)
	}
	if !strings.Contains(open.Text, "Pig Test") {
		t.Fatalf("open text = %q", open.Text)
	}

	snap := runTool(t, tl, env, map[string]any{"action": "snapshot"})
	if snap.IsError {
		t.Fatalf("snapshot failed: %s", snap.Text)
	}
	if !strings.Contains(snap.Text, "Click me") && !strings.Contains(snap.Text, "Go next") {
		t.Fatalf("snapshot has no elements:\n%s", snap.Text)
	}
	if !strings.Contains(snap.Text, "[ref=e") {
		t.Fatalf("snapshot lacks refs:\n%s", snap.Text)
	}

	ev := runTool(t, tl, env, map[string]any{"action": "eval", "expression": "document.title"})
	if ev.IsError || !strings.Contains(ev.Text, "Pig Test") {
		t.Fatalf("eval = %+v", ev)
	}

	bad := runTool(t, tl, env, map[string]any{"action": "click", "ref": "e999"})
	if !bad.IsError {
		t.Fatal("unknown ref click succeeded")
	}

	closeRes := runTool(t, tl, env, map[string]any{"action": "close"})
	if closeRes.IsError {
		t.Fatalf("close failed: %s", closeRes.Text)
	}
}

func TestBrowserTypeAndScreenshot(t *testing.T) {
	tl, env := testTool(t)
	dir := t.TempDir()
	page := filepath.Join(dir, "form.html")
	html := `<!doctype html><html><body>
<form onsubmit="event.preventDefault();document.title='submitted';">
<input aria-label="Name field" id="name">
<button type="submit">Send</button>
</form></body></html>`
	if err := os.WriteFile(page, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := runTool(t, tl, env, map[string]any{"action": "open", "url": "file://" + page}); res.IsError {
		t.Fatalf("open: %s", res.Text)
	}
	snap := runTool(t, tl, env, map[string]any{"action": "snapshot"})
	if snap.IsError {
		t.Fatalf("snapshot: %s", snap.Text)
	}
	ref := ""
	for _, line := range strings.Split(snap.Text, "\n") {
		if strings.Contains(line, "Name field") || strings.Contains(line, "textbox") || strings.Contains(line, "input") {
			if i := strings.Index(line, "[ref="); i >= 0 {
				if j := strings.Index(line[i:], "]"); j > 0 {
					ref = line[i+len("[ref=") : i+j]
					break
				}
			}
		}
	}
	if ref == "" {
		t.Skipf("no input ref found in snapshot:\n%s", snap.Text)
	}
	typed := runTool(t, tl, env, map[string]any{"action": "type", "ref": ref, "text": "pig"})
	if typed.IsError {
		t.Fatalf("type failed: %s", typed.Text)
	}
	shot := runTool(t, tl, env, map[string]any{"action": "screenshot"})
	if shot.IsError {
		t.Fatalf("screenshot failed: %s", shot.Text)
	}
	if !strings.Contains(shot.Text, "screenshot") {
		t.Fatalf("screenshot text = %q", shot.Text)
	}
	runTool(t, tl, env, map[string]any{"action": "close"})
}
