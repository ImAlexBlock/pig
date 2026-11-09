package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ImAlexBlock/pig/internal/store"
)

const todoSchema = `{
  "type": "object",
  "properties": {
    "phases": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "items": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "text":   {"type": "string"},
                "status": {"type": "string", "enum": ["pending", "active", "done", "blocked"]}
              },
              "required": ["text"]
            }
          }
        },
        "required": ["name", "items"]
      }
    }
  },
  "required": ["phases"],
  "additionalProperties": false
}`

type todoTool struct{}

func (todoTool) Name() string { return "todo" }
func (todoTool) Description() string {
	return "Replace the session checklist. Use one phase per work stage; statuses are pending, active, done, blocked."
}
func (todoTool) Schema() json.RawMessage { return json.RawMessage(todoSchema) }
func (todoTool) ReadOnly() bool          { return false }

type todoArgs struct {
	Phases []store.TodoPhase `json:"phases"`
}

func (todoTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a todoArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("todo: invalid arguments: " + err.Error()), nil
	}
	total := 0
	for pi := range a.Phases {
		for ii := range a.Phases[pi].Items {
			it := &a.Phases[pi].Items[ii]
			switch it.Status {
			case "pending", "active", "done", "blocked":
			case "":
				it.Status = "pending"
			default:
				return Error(fmt.Sprintf("todo: unknown status %q", it.Status)), nil
			}
			total++
		}
	}
	if env.Store != nil {
		content := store.NewTodoContent(store.TodoContent{Phases: a.Phases})
		if _, err := env.Store.AppendEntry(env.SessionID, store.KindTodo, "", content, 0); err != nil {
			return Error(fmt.Sprintf("todo: %v", err)), nil
		}
	}
	if env.EmitTodo != nil {
		if err := env.EmitTodo(a.Phases); err != nil {
			return Error(fmt.Sprintf("todo: %v", err)), nil
		}
	}
	var lines []string
	for _, ph := range a.Phases {
		done := 0
		for _, it := range ph.Items {
			if it.Status == "done" {
				done++
			}
		}
		lines = append(lines, fmt.Sprintf("%s %d/%d", ph.Name, done, len(ph.Items)))
	}
	return Result{
		Text:    fmt.Sprintf("todo updated (%d items): %s", total, strings.Join(lines, "; ")),
		Display: fmt.Sprintf("todo %d items", total),
	}, nil
}
