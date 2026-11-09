package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ImAlexBlock/pig/internal/internalurl"
)

const readSchema = `{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "Path or internal URI, with optional :N-M or :raw selector."},
    "offset": {"type": "integer", "description": "1-based first line to return."},
    "limit":  {"type": "integer", "description": "Maximum number of lines to return."}
  },
  "required": ["path"],
  "additionalProperties": false
}`

type readTool struct{}

func (readTool) Name() string { return "read" }
func (readTool) Description() string {
	return "Read a file, directory listing or internal URI (artifact://, skill://, session://, file://). Supports :N-M, :N-, :-N and :raw selectors."
}
func (readTool) Schema() json.RawMessage { return json.RawMessage(readSchema) }
func (readTool) ReadOnly() bool          { return true }

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (readTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a readArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("read: invalid arguments: " + err.Error()), nil
	}
	if strings.TrimSpace(a.Path) == "" {
		return Error("read: path is required"), nil
	}
	doc, base, err := resolveDoc(env, a.Path)
	if err != nil {
		return Error(err.Error()), nil
	}
	switch doc.Kind {
	case internalurl.KindDir:
		return Result{Text: doc.Text, Display: base}, nil
	case internalurl.KindImage:
		if env.ModelSupportsImages {
			return Result{
				Text:    fmt.Sprintf("image %s (%d bytes)", base, doc.Bytes),
				Images:  []Image{{MediaType: doc.Mime, Data: doc.Data}},
				Display: base,
			}, nil
		}
		return Result{Text: fmt.Sprintf("image %s (%d bytes); this model cannot view images", base, doc.Bytes), Display: base}, nil
	case internalurl.KindBinary:
		ref := ""
		if env.Store != nil && doc.Data != nil {
			if art, aerr := env.Store.PutArtifact(env.SessionID, "application/octet-stream", doc.Data); aerr == nil {
				ref = " [artifact://" + art.ID + "]"
			}
		}
		return Result{Text: fmt.Sprintf("binary file (%d bytes)%s", doc.Bytes, ref), Useless: true, Display: base}, nil
	}

	start := doc.Start
	if start < 1 {
		start = a.Offset
	}
	if start < 1 {
		start = 1
	}
	end := doc.End
	if end < 1 && a.Limit > 0 {
		end = start + a.Limit - 1
	}
	sel, first := internalurl.SelectLines(doc.Text, start, end)
	if sel == "" && doc.Text != "" {
		return Result{Text: fmt.Sprintf("read: no lines at offset %d", start), Display: base}, nil
	}
	if limitLines := a.Limit; limitLines > 0 {
		if lines := strings.Split(sel, "\n"); len(lines) > limitLines {
			sel = strings.Join(lines[:limitLines], "\n")
		}
	}
	if len(sel) > 51200 {
		sel = sel[:51200]
	}
	if base != "" {
		env.ReadFiles.Mark(base)
	}
	return Result{Text: internalurl.RenderLinePrefixed(sel, first, doc.Raw), Display: base}, nil
}

// resolveDoc opens an internal URI or filesystem path, applying selectors.
func resolveDoc(env *Env, path string) (*internalurl.Doc, string, error) {
	base, raw, start, end := internalurl.SplitSelector(strings.TrimSpace(path))
	if internalurl.IsInternal(base) {
		if env.Resolver == nil {
			return nil, "", fmt.Errorf("read: no resolver configured")
		}
		doc, err := env.Resolver.Open(path)
		if err != nil {
			return nil, "", fmt.Errorf("read: %v", err)
		}
		return doc, base, nil
	}
	abs := base
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(env.CWD, abs)
	}
	abs = filepath.Clean(abs)
	doc, err := internalurl.ReadFileDoc("", abs, raw, start, end)
	if err != nil {
		return nil, abs, fmt.Errorf("%v", err)
	}
	return doc, abs, nil
}

// protectedPath reports whether a path must be changed through pig commands.
func protectedPath(env *Env, abs string) (string, bool) {
	cfgPath := ""
	if env.Config != nil {
		cfgPath = env.Config.Path
	}
	if cfgPath == "" {
		return "", false
	}
	cfgPath = filepath.Clean(cfgPath)
	if abs == cfgPath {
		return `protected path: use "pig config set"`, true
	}
	authPath := filepath.Join(filepath.Dir(cfgPath), "auth.env")
	if abs == filepath.Clean(authPath) {
		return `protected path: use "pig auth"`, true
	}
	return "", false
}

func absPath(env *Env, path string) string {
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(env.CWD, p)
	}
	return filepath.Clean(p)
}
