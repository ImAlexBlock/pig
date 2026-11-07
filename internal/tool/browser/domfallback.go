package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// DOM fallback mode. The accessibility tree is the primary snapshot source,
// but newer Chrome builds emit AX property names that the pinned protocol
// package cannot decode. In that case pig falls back to a DOM traversal that
// keeps the same `- role "name" [ref=eN]` shape.
const (
	modeAX = "ax"
	modeJS = "js"
)

// jsSelector picks the elements worth exposing to the model.
const jsSelector = "a,button,input,select,textarea,summary,label,[role],[contenteditable=true],h1,h2,h3,h4,img"

const jsSnapshotExpr = `(() => {
  const sel = %q;
  const nodes = Array.from(document.querySelectorAll(sel));
  const out = [];
  nodes.forEach((el, i) => {
    const st = window.getComputedStyle(el);
    if (st.display === 'none' || st.visibility === 'hidden' || st.opacity === '0') return;
    const role = (el.getAttribute('role') || el.tagName.toLowerCase()).toLowerCase();
    const rawName = el.getAttribute('aria-label') || el.getAttribute('title') || el.innerText || el.getAttribute('alt') || '';
    const name = rawName.replace(/\s+/g, ' ').trim().slice(0, 120);
    const rawValue = el.value === undefined || el.value === null ? '' : String(el.value);
    if (!name && !rawValue) return;
    out.push({idx: i, role: role, name: name, value: rawValue.slice(0, 120)});
  });
  return JSON.stringify(out);
})()`

func elemExpr(idx int) string {
	return fmt.Sprintf("(Array.from(document.querySelectorAll(%q))[%d])", jsSelector, idx)
}

func (t *Tool) evalString(ctx context.Context, expr string) (string, error) {
	var out string
	err := t.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		res, exc, err := runtime.Evaluate(expr).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exc != nil {
			return fmt.Errorf("%s", exc.Text)
		}
		if len(res.Value) == 0 {
			return nil
		}
		var s string
		if err := json.Unmarshal(res.Value, &s); err != nil {
			return nil
		}
		out = s
		return nil
	}))
	return out, err
}

// jsSnapshot renders the DOM fallback snapshot.
func (t *Tool) jsSnapshot(ctx context.Context) (string, error) {
	raw, err := t.evalString(ctx, fmt.Sprintf(jsSnapshotExpr, jsSelector))
	if err != nil {
		return "", err
	}
	var items []struct {
		Idx   int    `json:"idx"`
		Role  string `json:"role"`
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return "", fmt.Errorf("snapshot decode: %v", err)
	}
	refs := map[string]int{}
	var sb strings.Builder
	for i, it := range items {
		ref := fmt.Sprintf("e%d", i+1)
		refs[ref] = it.Idx
		fmt.Fprintf(&sb, "- %s %q [ref=%s]", it.Role, it.Name, ref)
		if it.Value != "" {
			fmt.Fprintf(&sb, " value=%q", it.Value)
		}
		sb.WriteString("\n")
		if max := t.cfg.Browser.MaxSnapshotChars; max > 0 && sb.Len() > max {
			sb.WriteString("…\n")
			break
		}
	}
	t.mu.Lock()
	t.mode = modeJS
	t.jsRefs = refs
	t.mu.Unlock()
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		out = "(empty snapshot)"
	}
	return out, nil
}

func (t *Tool) jsClick(ctx context.Context, ref string) error {
	t.mu.Lock()
	idx, ok := t.jsRefs[ref]
	mode := t.mode
	t.mu.Unlock()
	if mode != modeJS || !ok {
		return fmt.Errorf("unknown ref %s (run snapshot first)", ref)
	}
	expr := fmt.Sprintf(`(() => { const el = %s; if (!el) return 'missing'; el.scrollIntoView({block:'center'}); el.click(); return 'ok'; })()`, elemExpr(idx))
	res, err := t.evalString(ctx, expr)
	if err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("element %s is gone; run snapshot again", ref)
	}
	return nil
}

func (t *Tool) jsType(ctx context.Context, ref, text string, submit bool) error {
	t.mu.Lock()
	idx, ok := t.jsRefs[ref]
	mode := t.mode
	t.mu.Unlock()
	if mode != modeJS || !ok {
		return fmt.Errorf("unknown ref %s (run snapshot first)", ref)
	}
	val, _ := json.Marshal(text)
	expr := fmt.Sprintf(`(() => { const el = %s; if (!el) return 'missing';
  el.focus();
  const v = %s;
  if (el.isContentEditable) { el.textContent = v; } else { el.value = v; }
  el.dispatchEvent(new Event('input', {bubbles: true}));
  el.dispatchEvent(new Event('change', {bubbles: true}));
  return 'ok'; })()`, elemExpr(idx), string(val))
	res, err := t.evalString(ctx, expr)
	if err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("element %s is gone; run snapshot again", ref)
	}
	if submit {
		sub := fmt.Sprintf(`(() => { const el = %s; if (!el) return 'missing';
  const form = el.form || el.closest('form');
  if (form) { if (form.requestSubmit) { form.requestSubmit(); return 'ok'; } form.submit(); return 'ok'; }
  el.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', keyCode:13, bubbles:true}));
  return 'ok'; })()`, elemExpr(idx))
		if _, err := t.evalString(ctx, sub); err != nil {
			return err
		}
	}
	return nil
}
