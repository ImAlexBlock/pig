package agent

import (
	"github.com/alexblock/pig/internal/provider"
	"github.com/alexblock/pig/internal/store"
)

// Run event types shared by the TUI, the CLI client and channels.
const (
	EvRunStart      = "run_start"
	EvTextDelta     = "text_delta"
	EvThinkingDelta = "thinking_delta"
	EvToolStart     = "tool_start"
	EvToolEnd       = "tool_end"
	EvNotice        = "notice"
	EvTodo          = "todo"
	EvUsage         = "usage"
	EvCompactStart  = "compact_start"
	EvCompactEnd    = "compact_end"
	EvJobUpdate     = "job_update"
	EvRunDetached   = "run_detached"
	EvRunEnd        = "run_end"
	EvError         = "error"
)

// Event is one session event.
type Event struct {
	Type      string            `json:"type"`
	RunID     string            `json:"run_id,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	Text      string            `json:"text,omitempty"`
	ToolID    string            `json:"tool_id,omitempty"`
	ToolName  string            `json:"tool_name,omitempty"`
	Args      string            `json:"args,omitempty"`
	Result    string            `json:"result,omitempty"`
	IsError   bool              `json:"is_error,omitempty"`
	Display   string            `json:"display,omitempty"`
	Level     string            `json:"level,omitempty"`
	Notice    string            `json:"notice,omitempty"`
	Phases    []store.TodoPhase `json:"phases,omitempty"`
	Usage     *provider.Usage   `json:"usage,omitempty"`
	CostUSD   float64           `json:"cost_usd,omitempty"`
	Job       *store.Job        `json:"job,omitempty"`
	Model     string            `json:"model,omitempty"`
	Method    string            `json:"method,omitempty"`
	Stop      string            `json:"stop,omitempty"`
	Tokens    int               `json:"tokens,omitempty"`
	ElapsedMS int64             `json:"elapsed_ms,omitempty"`
	Error     string            `json:"error,omitempty"`
	Reason    string            `json:"reason,omitempty"`
}

// Sink receives run events. Implementations must be safe for sequential use
// from one run goroutine.
type Sink func(Event)
