package snapcompact

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alexblock/pig/internal/store"
)

// SerializeOptions configures transcript flattening.
type SerializeOptions struct {
	ToolResultMaxChars int
	ToolArgMaxChars    int
	PreviousSummary    string
}

// Serialize flattens entries into the text that gets rendered.
func Serialize(entries []*store.Entry, opt SerializeOptions) string {
	if opt.ToolResultMaxChars <= 0 {
		opt.ToolResultMaxChars = 2000
	}
	if opt.ToolArgMaxChars <= 0 {
		opt.ToolArgMaxChars = 2000
	}
	var sb strings.Builder
	if opt.PreviousSummary != "" {
		sb.WriteString("[previous summary]\n")
		sb.WriteString(opt.PreviousSummary)
		sb.WriteString("\n\n")
	}
	for _, e := range entries {
		switch e.Kind {
		case store.KindMessage:
			m := e.Message()
			role := e.Role
			if role == "" {
				role = "assistant"
			}
			sb.WriteString("[" + role + "] ")
			sb.WriteString(collapse(m.Text))
			sb.WriteString("\n")
		case store.KindToolCall:
			c := e.ToolCall()
			sb.WriteString("[tool " + c.Name + "] ")
			sb.WriteString(formatArgs(c.Args, opt.ToolArgMaxChars))
			sb.WriteString("\n")
		case store.KindToolResult:
			r := e.ToolResult()
			label := "[result " + r.Name + "] "
			if r.IsError {
				label = "[error " + r.Name + "] "
			}
			sb.WriteString(label)
			sb.WriteString(headTail(collapse(r.Text), opt.ToolResultMaxChars))
			sb.WriteString("\n")
		case store.KindJobResult:
			j := e.JobResult()
			sb.WriteString("[job " + j.JobID + " " + j.Status + "] ")
			sb.WriteString(headTail(collapse(j.Text), opt.ToolResultMaxChars))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func collapse(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func headTail(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	head := n * 6 / 10
	tail := n - head
	return s[:head] + " … " + s[len(s)-tail:]
}

func formatArgs(raw json.RawMessage, max int) string {
	if len(raw) == 0 {
		return "{}"
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return headTail(collapse(string(raw)), max)
	}
	var parts []string
	for k, v := range m {
		val := fmt.Sprintf("%v", v)
		if len(val) > 500 {
			val = val[:500] + "…"
		}
		parts = append(parts, k+"="+val)
		if len(parts) >= 8 {
			break
		}
	}
	out := strings.Join(parts, ", ")
	if len(out) > max {
		out = out[:max] + "…"
	}
	return out
}
