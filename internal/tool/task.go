package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

const taskSchema = `{
  "type": "object",
  "properties": {
    "prompt":     {"type": "string", "description": "Task for the subagent."},
    "name":       {"type": "string", "description": "Short label."},
    "model":      {"type": "string", "description": "Model ref override."},
    "tools":      {"type": "array", "items": {"type": "string"}, "description": "Tool allowlist."},
    "background": {"type": "boolean", "description": "Run as a background job and return immediately."},
    "context":    {"type": "string", "description": "Extra context prepended to the prompt."}
  },
  "required": ["prompt"],
  "additionalProperties": false
}`

type taskTool struct{}

func (taskTool) Name() string { return "task" }
func (taskTool) Description() string {
	return "Delegate an independent subtask to a subagent with its own session. Use background=true for long work."
}
func (taskTool) Schema() json.RawMessage { return json.RawMessage(taskSchema) }
func (taskTool) ReadOnly() bool          { return false }

type taskArgs struct {
	Prompt     string   `json:"prompt"`
	Name       string   `json:"name"`
	Model      string   `json:"model"`
	Tools      []string `json:"tools"`
	Background bool     `json:"background"`
	Context    string   `json:"context"`
}

func (taskTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a taskArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("task: invalid arguments: " + err.Error()), nil
	}
	if a.Prompt == "" {
		return Error("task: prompt is required"), nil
	}
	if env.RunSubagent == nil {
		return Error("task: subagents are unavailable"), nil
	}
	res, err := env.RunSubagent(ctx, SubagentRequest{
		ParentRunID:         env.RunID,
		Prompt:              a.Prompt,
		Name:                a.Name,
		Model:               a.Model,
		Tools:               a.Tools,
		Background:          a.Background,
		Context:             a.Context,
		CallerSessionID:     env.SessionID,
		CallerCWD:           env.CWD,
		CallerDepth:         env.Depth,
		OriginChannel:       env.OriginChannel,
		OriginChat:          env.OriginChat,
		ModelSupportsImages: env.ModelSupportsImages,
	})
	if err != nil {
		return Error(fmt.Sprintf("task: %v", err)), nil
	}
	if res.JobID != "" {
		return Result{
			Text:    fmt.Sprintf("job started: %s", res.JobID),
			Display: "task → " + res.JobID,
		}, nil
	}
	return Result{
		Text:    res.Text,
		Display: "task " + summarizeCommand(a.Prompt),
	}, nil
}
