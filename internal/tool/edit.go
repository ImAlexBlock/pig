package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const editSchema = `{
  "type": "object",
  "properties": {
    "path":        {"type": "string", "description": "File to edit."},
    "old_string":  {"type": "string", "description": "Exact text to replace."},
    "new_string":  {"type": "string", "description": "Replacement text."},
    "replace_all": {"type": "boolean", "description": "Replace every occurrence."}
  },
  "required": ["path", "old_string", "new_string"],
  "additionalProperties": false
}`

type editTool struct{}

func (editTool) Name() string { return "edit" }
func (editTool) Description() string {
	return "Replace an exact string in a file. Requires reading the file first in this session."
}
func (editTool) Schema() json.RawMessage { return json.RawMessage(editSchema) }
func (editTool) ReadOnly() bool          { return false }

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (editTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a editArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("edit: invalid arguments: " + err.Error()), nil
	}
	if strings.TrimSpace(a.Path) == "" {
		return Error("edit: path is required"), nil
	}
	if a.OldString == "" {
		return Error("edit: old_string is empty"), nil
	}
	abs := absPath(env, a.Path)
	if msg, ok := protectedPath(env, abs); ok {
		return Error(msg), nil
	}
	if !env.ReadFiles.Has(abs) {
		return Error("read the file before editing"), nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return Error("file not found: " + abs), nil
		}
		return Error(fmt.Sprintf("edit: %v", err)), nil
	}
	content := string(data)
	matches := findMatches(content, a.OldString)
	if len(matches) == 0 {
		return Error("old_string not found in " + abs), nil
	}
	if len(matches) > 1 && !a.ReplaceAll {
		return Error(fmt.Sprintf("old_string is not unique (%d matches)", len(matches))), nil
	}
	if !a.ReplaceAll {
		matches = matches[:1]
	}
	var sb strings.Builder
	prev := 0
	for _, m := range matches {
		sb.WriteString(content[prev:m[0]])
		sb.WriteString(a.NewString)
		prev = m[1]
	}
	sb.WriteString(content[prev:])
	out := sb.String()
	if err := os.WriteFile(abs, []byte(out), 0o644); err != nil {
		return Error(fmt.Sprintf("edit: %v", err)), nil
	}
	env.ReadFiles.Mark(abs)
	return Result{
		Text:    fmt.Sprintf("edited %s: %d replacement(s)", abs, len(matches)),
		Display: abs,
	}, nil
}

// findMatches returns byte ranges of old inside content. When an exact search
// fails it retries with trailing whitespace stripped from every line.
func findMatches(content, old string) [][2]int {
	if out := scanExact(content, old); len(out) > 0 {
		return out
	}
	normContent, off := normalizeWithMap(content)
	normOld, _ := normalizeWithMap(old)
	if strings.TrimSpace(normOld) == "" {
		return nil
	}
	var out [][2]int
	start := 0
	for {
		i := strings.Index(normContent[start:], normOld)
		if i < 0 {
			break
		}
		p := start + i
		s := off[p]
		e := off[p+len(normOld)]
		if s <= e && e <= len(content) {
			out = append(out, [2]int{s, e})
		}
		start = p + len(normOld)
		if start >= len(normContent) {
			break
		}
	}
	return out
}

func scanExact(content, old string) [][2]int {
	var out [][2]int
	start := 0
	for {
		i := strings.Index(content[start:], old)
		if i < 0 {
			return out
		}
		p := start + i
		out = append(out, [2]int{p, p + len(old)})
		start = p + len(old)
		if start >= len(content) {
			return out
		}
	}
}

// normalizeWithMap strips trailing whitespace per line and returns the
// normalized text plus a byte-offset map back into the original.
func normalizeWithMap(s string) (string, []int) {
	var sb strings.Builder
	off := make([]int, 0, len(s)+1)
	i := 0
	for {
		if i > len(s) {
			break
		}
		j := strings.IndexByte(s[i:], '\n')
		var line string
		hasNL := j >= 0
		if hasNL {
			line = s[i : i+j]
		} else {
			line = s[i:]
		}
		trimmed := strings.TrimRight(line, " \t\r")
		for k := range len(trimmed) {
			sb.WriteByte(trimmed[k])
			off = append(off, i+k)
		}
		if hasNL {
			sb.WriteByte('\n')
			off = append(off, i+j)
			i = i + j + 1
			continue
		}
		break
	}
	off = append(off, len(s))
	for len(off) < sb.Len()+1 {
		off = append(off, len(s))
	}
	return sb.String(), off
}
