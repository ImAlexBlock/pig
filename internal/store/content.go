package store

import "encoding/json"

// MessageContent is the body of a message entry.
type MessageContent struct {
	Text      string          `json:"text"`
	Reasoning string          `json:"reasoning,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
	// Attachments lists artifact ids shown to the model alongside Text
	// (inbound chat images, screenshots produced by tools).
	Attachments []string `json:"attachments,omitempty"`
}

// ToolCallContent is the body of a tool_call entry.
type ToolCallContent struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolResultContent is the body of a tool_result entry.
type ToolResultContent struct {
	CallID   string `json:"call_id"`
	Name     string `json:"name"`
	Text     string `json:"text"`
	Artifact string `json:"artifact,omitempty"`
	IsError  bool   `json:"is_error,omitempty"`
	Useless  bool   `json:"useless,omitempty"`
}

// NoticeContent is the body of a notice entry.
type NoticeContent struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// TodoItem is one checklist item.
type TodoItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// TodoPhase groups checklist items.
type TodoPhase struct {
	Name  string     `json:"name"`
	Items []TodoItem `json:"items"`
}

// TodoContent is the body of a todo entry.
type TodoContent struct {
	Phases []TodoPhase `json:"phases"`
}

// JobResultContent is the body of a job_result entry.
type JobResultContent struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

// CompactionContent is the body of a compaction entry.
type CompactionContent struct {
	CompactionID string `json:"compaction_id"`
}

// Frame is one archived snapcompact image.
type Frame struct {
	ArtifactID string `json:"artifact_id"`
	Bytes      int    `json:"bytes"`
}

func decode[T any](s string) T {
	var v T
	_ = json.Unmarshal([]byte(s), &v)
	return v
}

func encode[T any](v T) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Message decodes an entry as a message body.
func (e *Entry) Message() MessageContent { return decode[MessageContent](e.Content) }

// ToolCall decodes an entry as a tool call.
func (e *Entry) ToolCall() ToolCallContent { return decode[ToolCallContent](e.Content) }

// ToolResult decodes an entry as a tool result.
func (e *Entry) ToolResult() ToolResultContent { return decode[ToolResultContent](e.Content) }

// Notice decodes an entry as a notice.
func (e *Entry) Notice() NoticeContent { return decode[NoticeContent](e.Content) }

// Todo decodes an entry as a todo list.
func (e *Entry) Todo() TodoContent { return decode[TodoContent](e.Content) }

// JobResult decodes an entry as a job result.
func (e *Entry) JobResult() JobResultContent { return decode[JobResultContent](e.Content) }

// Compaction decodes an entry as a compaction marker.
func (e *Entry) Compaction() CompactionContent { return decode[CompactionContent](e.Content) }

// NewMessageContent encodes a message body.
func NewMessageContent(m MessageContent) string { return encode(m) }

// NewToolCallContent encodes a tool call body.
func NewToolCallContent(c ToolCallContent) string { return encode(c) }

// NewToolResultContent encodes a tool result body.
func NewToolResultContent(r ToolResultContent) string { return encode(r) }

// NewNoticeContent encodes a notice body.
func NewNoticeContent(n NoticeContent) string { return encode(n) }

// NewTodoContent encodes a todo body.
func NewTodoContent(t TodoContent) string { return encode(t) }

// NewJobResultContent encodes a job result body.
func NewJobResultContent(j JobResultContent) string { return encode(j) }

// NewCompactionContent encodes a compaction marker body.
func NewCompactionContent(c CompactionContent) string { return encode(c) }

// ParseFrames decodes a compactions.frames JSON array.
func ParseFrames(s string) []Frame {
	if s == "" {
		return nil
	}
	return decode[[]Frame](s)
}

// EncodeFrames encodes frames for compactions.frames.
func EncodeFrames(f []Frame) string { return encode(f) }
