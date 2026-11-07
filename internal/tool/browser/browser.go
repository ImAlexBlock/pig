// Package browser exposes a chromedp-driven browser tool.
package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/tool"
)

const browserSchema = `{
  "type": "object",
  "properties": {
    "action":     {"type": "string", "enum": ["open", "snapshot", "click", "type", "eval", "screenshot", "close"]},
    "url":        {"type": "string", "description": "URL for open."},
    "ref":        {"type": "string", "description": "Element ref from snapshot, e.g. e12."},
    "text":       {"type": "string", "description": "Text for type."},
    "submit":     {"type": "boolean", "description": "Press Enter after typing."},
    "expression": {"type": "string", "description": "JavaScript expression for eval."}
  },
  "required": ["action"],
  "additionalProperties": false
}`

// Tool is the browser tool.
type Tool struct {
	cfg *config.Config

	mu     sync.Mutex
	tabCtx context.Context
	cancel context.CancelFunc
	alloc  context.CancelFunc
	refs   map[string]cdp.BackendNodeID
	mode   string
	jsRefs map[string]int
}

// NewTool returns a browser tool bound to cfg.
func NewTool(cfg *config.Config) *Tool {
	return &Tool{cfg: cfg, refs: map[string]cdp.BackendNodeID{}, mode: modeAX, jsRefs: map[string]int{}}
}

func (t *Tool) Name() string { return "browser" }
func (t *Tool) Description() string {
	return "Drive headless Chrome: open, snapshot the accessibility tree, click, type, eval, screenshot."
}
func (t *Tool) Schema() json.RawMessage {
	return json.RawMessage(browserSchema)
}
func (t *Tool) ReadOnly() bool { return false }

func (t *Tool) timeout() time.Duration {
	d := time.Duration(t.cfg.Browser.TimeoutSeconds) * time.Second
	if d <= 0 {
		d = 30 * time.Second
	}
	return d
}

// chromePath resolves the Chrome binary.
func (t *Tool) chromePath() (string, error) {
	if p := t.cfg.Browser.ChromePath; p != "" {
		return p, nil
	}
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p, nil
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("chrome not found; set browser.chrome_path")
}

func (t *Tool) ensure() (context.Context, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tabCtx != nil {
		return t.tabCtx, nil
	}
	path, err := t.chromePath()
	if err != nil {
		return nil, err
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(path), chromedp.Flag("headless", t.cfg.Browser.Headless))
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	tabCtx, tabCancel := chromedp.NewContext(allocCtx)
	// Run with the tab context itself: a derived cancelable context would
	// close the browser when it expires.
	err = chromedp.Run(tabCtx)
	if err != nil {
		allocCancel()
		tabCancel()
		return nil, fmt.Errorf("browser: %v", err)
	}
	t.tabCtx = tabCtx
	t.cancel = tabCancel
	t.alloc = allocCancel
	return t.tabCtx, nil
}

// Close shuts the browser down.
func (t *Tool) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if t.alloc != nil {
		t.alloc()
		t.alloc = nil
	}
	t.tabCtx = nil
	t.refs = map[string]cdp.BackendNodeID{}
}

func (t *Tool) run(ctx context.Context, actions ...chromedp.Action) error {
	tabCtx, err := t.ensure()
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(tabCtx, actions...) }()
	select {
	case err := <-done:
		return err
	case <-time.After(t.timeout()):
		t.Close()
		return fmt.Errorf("timeout after %s", t.timeout())
	case <-ctx.Done():
		return ctx.Err()
	}
}

// strValue decodes an accessibility value that is a JSON scalar.
func strValue(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err == nil {
		return s
	}
	var anyVal any
	if err := json.Unmarshal(v.Value, &anyVal); err == nil {
		return fmt.Sprintf("%v", anyVal)
	}
	return string(v.Value)
}

func (t *Tool) Run(ctx context.Context, c tool.Call, env *tool.Env) (tool.Result, error) {
	var a struct {
		Action     string `json:"action"`
		URL        string `json:"url"`
		Ref        string `json:"ref"`
		Text       string `json:"text"`
		Submit     bool   `json:"submit"`
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tool.Error("browser: invalid arguments: " + err.Error()), nil
	}
	switch a.Action {
	case "open":
		if a.URL == "" {
			return tool.Error("browser: url is required"), nil
		}
		var title, loc string
		err := t.run(ctx,
			chromedp.Navigate(a.URL),
			chromedp.ActionFunc(func(ctx context.Context) error {
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					var state string
					if err := chromedp.Evaluate("document.readyState", &state).Do(ctx); err != nil {
						return err
					}
					if state == "complete" {
						return nil
					}
					time.Sleep(100 * time.Millisecond)
				}
				return nil
			}),
			chromedp.Sleep(500*time.Millisecond),
			chromedp.Title(&title),
			chromedp.Location(&loc),
		)
		if err != nil {
			return tool.Error("browser: " + err.Error()), nil
		}
		return tool.Result{Text: fmt.Sprintf("opened %s\n%s", title, loc), Display: loc}, nil
	case "snapshot":
		text, err := t.snapshot(ctx)
		if err != nil {
			return tool.Error("browser: " + err.Error()), nil
		}
		return tool.Result{Text: text, Display: "snapshot"}, nil
	case "click":
		if a.Ref == "" {
			return tool.Error("browser: ref is required"), nil
		}
		if err := t.click(ctx, a.Ref); err != nil {
			return tool.Error("browser: " + err.Error()), nil
		}
		return tool.Result{Text: "clicked " + a.Ref, Display: "click " + a.Ref}, nil
	case "type":
		if a.Ref == "" {
			return tool.Error("browser: ref is required"), nil
		}
		if err := t.typeText(ctx, a.Ref, a.Text, a.Submit); err != nil {
			return tool.Error("browser: " + err.Error()), nil
		}
		return tool.Result{Text: "typed into " + a.Ref, Display: "type " + a.Ref}, nil
	case "eval":
		if a.Expression == "" {
			return tool.Error("browser: expression is required"), nil
		}
		var out any
		err := t.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			res, exc, err := runtime.Evaluate(a.Expression).WithReturnByValue(true).WithAwaitPromise(true).Do(ctx)
			if err != nil {
				return err
			}
			if exc != nil {
				return fmt.Errorf("%s", exc.Text)
			}
			out = res.Value
			return nil
		}))
		if err != nil {
			return tool.Error("browser: " + err.Error()), nil
		}
		b, _ := json.Marshal(out)
		if len(b) > 16384 {
			b = append(b[:16384:16384], []byte("…")...)
		}
		return tool.Result{Text: string(b), Display: "eval"}, nil
	case "screenshot":
		var buf []byte
		if err := t.run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
			return tool.Error("browser: " + err.Error()), nil
		}
		if _, derr := png.Decode(bytes.NewReader(buf)); derr != nil {
			return tool.Error("browser: invalid screenshot"), nil
		}
		res := tool.Result{Text: fmt.Sprintf("screenshot (%d bytes)", len(buf)), Display: "screenshot"}
		if env.ModelSupportsImages {
			res.Images = []tool.Image{{MediaType: "image/png", Data: buf}}
			return res, nil
		}
		if env.Store != nil {
			if art, err := env.Store.PutArtifact(env.SessionID, "image/png", buf); err == nil {
				res.Text = fmt.Sprintf("screenshot (%d bytes) [artifact://%s]", len(buf), art.ID)
				res.Artifact = art.ID
			}
		}
		return res, nil
	case "close":
		t.Close()
		return tool.Result{Text: "browser closed", Useless: true, Display: "close"}, nil
	}
	return tool.Error(fmt.Sprintf("browser: unknown action %q", a.Action)), nil
}

func (t *Tool) snapshot(ctx context.Context) (string, error) {
	var nodes []*accessibility.Node
	err := t.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		out, nerr := accessibility.GetFullAXTree().Do(ctx)
		if nerr != nil {
			return nerr
		}
		nodes = out
		return nil
	}))
	if err != nil {
		// Newer Chrome builds emit AX fields the pinned protocol package
		// cannot decode; fall back to a DOM traversal.
		return t.jsSnapshot(ctx)
	}
	refs := map[string]cdp.BackendNodeID{}
	var sb strings.Builder
	ref := 0
	for _, n := range nodes {
		if n == nil || n.Ignored {
			continue
		}
		role := strValue(n.Role)
		if role == "" || role == "none" {
			continue
		}
		name := strValue(n.Name)
		value := strValue(n.Value)
		if role == "generic" && name == "" && value == "" {
			continue
		}
		ref++
		refID := fmt.Sprintf("e%d", ref)
		if n.BackendDOMNodeID != 0 {
			refs[refID] = n.BackendDOMNodeID
		}
		fmt.Fprintf(&sb, "- %s %q [ref=%s]", role, name, refID)
		if value != "" {
			fmt.Fprintf(&sb, " value=%q", value)
		}
		sb.WriteString("\n")
		if max := t.cfg.Browser.MaxSnapshotChars; max > 0 && sb.Len() > max {
			sb.WriteString("…\n")
			break
		}
	}
	t.mu.Lock()
	t.refs = refs
	t.mode = modeAX
	t.jsRefs = map[string]int{}
	t.mu.Unlock()
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		out = "(empty snapshot)"
	}
	return out, nil
}

func (t *Tool) nodeOf(ref string) (cdp.BackendNodeID, error) {
	t.mu.Lock()
	id, ok := t.refs[ref]
	t.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("unknown ref %s (run snapshot first)", ref)
	}
	return id, nil
}

func (t *Tool) click(ctx context.Context, ref string) error {
	t.mu.Lock()
	mode := t.mode
	t.mu.Unlock()
	if mode == modeJS {
		return t.jsClick(ctx, ref)
	}
	id, err := t.nodeOf(ref)
	if err != nil {
		return err
	}
	return t.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := dom.ScrollIntoViewIfNeeded().WithBackendNodeID(id).Do(ctx); err != nil {
			return err
		}
		box, err := dom.GetBoxModel().WithBackendNodeID(id).Do(ctx)
		if err != nil {
			return err
		}
		content := box.Content
		x := (content[0] + content[2] + content[4] + content[6]) / 8
		y := (content[1] + content[3] + content[5] + content[7]) / 8
		if err := input.DispatchMouseEvent(input.MousePressed, x, y).WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseReleased, x, y).WithButton(input.Left).WithClickCount(1).Do(ctx)
	}))
}

func (t *Tool) typeText(ctx context.Context, ref, text string, submit bool) error {
	t.mu.Lock()
	mode := t.mode
	t.mu.Unlock()
	if mode == modeJS {
		return t.jsType(ctx, ref, text, submit)
	}
	id, err := t.nodeOf(ref)
	if err != nil {
		return err
	}
	return t.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := dom.Focus().WithBackendNodeID(id).Do(ctx); err != nil {
			return err
		}
		if text != "" {
			if err := input.InsertText(text).Do(ctx); err != nil {
				return err
			}
		}
		if submit {
			return chromedp.KeyEvent("\r").Do(ctx)
		}
		return nil
	}))
}
