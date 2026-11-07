package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const writeSchema = `{
  "type": "object",
  "properties": {
    "path":    {"type": "string", "description": "File path to write."},
    "content": {"type": "string", "description": "Full file content."},
    "force":   {"type": "boolean", "description": "Overwrite without reading first."}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

type writeTool struct{}

func (writeTool) Name() string { return "write" }
func (writeTool) Description() string {
	return "Create or overwrite a file with the given content. Requires reading an existing file first unless force is set."
}
func (writeTool) Schema() json.RawMessage { return json.RawMessage(writeSchema) }
func (writeTool) ReadOnly() bool          { return false }

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Force   bool   `json:"force"`
}

func (writeTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a writeArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("write: invalid arguments: " + err.Error()), nil
	}
	if strings.TrimSpace(a.Path) == "" {
		return Error("write: path is required"), nil
	}
	abs := absPath(env, a.Path)
	if msg, ok := protectedPath(env, abs); ok {
		return Error(msg), nil
	}
	old, err := os.ReadFile(abs)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return Error(fmt.Sprintf("write: %v", err)), nil
	}
	if existed && !a.Force && !env.ReadFiles.Has(abs) {
		return Error("read the file before overwriting"), nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Error(fmt.Sprintf("write: %v", err)), nil
	}
	if err := os.WriteFile(abs, []byte(a.Content), 0o644); err != nil {
		return Error(fmt.Sprintf("write: %v", err)), nil
	}
	env.ReadFiles.Mark(abs)
	added, removed := diffStat(strings.Split(string(old), "\n"), strings.Split(a.Content, "\n"))
	return Result{
		Text:    fmt.Sprintf("wrote %s (%d bytes, +%d -%d)", abs, len(a.Content), added, removed),
		Display: abs,
	}, nil
}

// diffStat counts added and removed lines with a common prefix/suffix trim.
func diffStat(oldLines, newLines []string) (added, removed int) {
	if len(oldLines) == 1 && oldLines[0] == "" {
		oldLines = nil
	}
	if len(newLines) == 1 && newLines[0] == "" {
		newLines = nil
	}
	pre := 0
	for pre < len(oldLines) && pre < len(newLines) && oldLines[pre] == newLines[pre] {
		pre++
	}
	suf := 0
	for suf < len(oldLines)-pre && suf < len(newLines)-pre &&
		oldLines[len(oldLines)-1-suf] == newLines[len(newLines)-1-suf] {
		suf++
	}
	return len(newLines) - pre - suf, len(oldLines) - pre - suf
}
