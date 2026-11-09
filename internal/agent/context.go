package agent

import (
	"encoding/json"
	"strings"

	"github.com/ImAlexBlock/pig/internal/compact"
	"github.com/ImAlexBlock/pig/internal/internalurl"
	"github.com/ImAlexBlock/pig/internal/provider"
	"github.com/ImAlexBlock/pig/internal/store"
	"github.com/ImAlexBlock/pig/internal/tool"
)

func (a *Agent) rebuildReadSet(sessionID string, cwd string) *tool.ReadSet {
	rs := tool.NewReadSet()
	entries, err := a.Store.AllEntries(sessionID)
	if err != nil {
		return rs
	}
	for _, e := range entries {
		if e.Kind != store.KindToolCall {
			continue
		}
		c := e.ToolCall()
		switch c.Name {
		case "read", "write", "edit":
		default:
			continue
		}
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(c.Args, &args); err != nil || args.Path == "" {
			continue
		}
		if internalurl.IsInternal(args.Path) {
			continue
		}
		p := args.Path
		if !strings.HasPrefix(p, "/") {
			p = cwd + "/" + p
		}
		rs.Mark(cleanPath(p))
	}
	return rs
}

func cleanPath(p string) string {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// Run executes one agent turn loop. When opt.Prompt is empty the session
// history already contains the pending user message.

func toolsTokens(specs []provider.ToolSpec) int {
	total := 0
	for _, s := range specs {
		total += compact.EstimateTokens(s.Name) + compact.EstimateTokens(s.Description) + compact.EstimateTokens(string(s.Schema))
	}
	return total
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
