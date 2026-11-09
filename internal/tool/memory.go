package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ImAlexBlock/pig/internal/store"
)

const memorySchema = `{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["add", "list", "recall", "remove"]},
    "key":    {"type": "string", "description": "Short label, e.g. 'timezone'."},
    "value":  {"type": "string", "description": "Fact to remember (max 2000 chars)."},
    "query":  {"type": "string", "description": "Substring to recall."}
  },
  "required": ["action"],
  "additionalProperties": false
}`

// Limits for the memory tool. Built in: memory has no configuration surface.
const (
	MaxFactKey   = 200
	MaxFactValue = 2000
	MaxFactList  = 50
	MaxFactStore = 200
	InjectFacts  = 20
)

type memoryTool struct{}

func (memoryTool) Name() string { return "memory" }
func (memoryTool) Description() string {
	return "Remember durable facts across sessions: add {key,value}, recall by substring, list recent, remove."
}
func (memoryTool) Schema() json.RawMessage { return json.RawMessage(memorySchema) }
func (memoryTool) ReadOnly() bool          { return false }

type memoryArgs struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Value  string `json:"value"`
	Query  string `json:"query"`
}

func (memoryTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a memoryArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("memory: invalid arguments: " + err.Error()), nil
	}
	if env.Store == nil {
		return Error("memory: store unavailable"), nil
	}
	switch a.Action {
	case "add":
		if strings.TrimSpace(a.Key) == "" {
			return Error("memory: key is required"), nil
		}
		if len(a.Key) > MaxFactKey {
			return Error(fmt.Sprintf("memory: key too long (max %d)", MaxFactKey)), nil
		}
		if strings.TrimSpace(a.Value) == "" {
			return Error("memory: value is required"), nil
		}
		if len(a.Value) > MaxFactValue {
			return Error(fmt.Sprintf("memory: value too long (max %d)", MaxFactValue)), nil
		}
		f, err := env.Store.PutFact(a.Key, a.Value)
		if err != nil {
			return Error(fmt.Sprintf("memory: %v", err)), nil
		}
		_ = env.Store.TrimFacts(MaxFactStore)
		return Result{Text: fmt.Sprintf("remembered %s: %s", f.Key, clip(f.Value, 200)), Display: "memory+" + f.Key}, nil
	case "recall":
		if strings.TrimSpace(a.Query) == "" {
			return Error("memory: query is required"), nil
		}
		facts, err := env.Store.SearchFacts(a.Query, MaxFactList)
		if err != nil {
			return Error(fmt.Sprintf("memory: %v", err)), nil
		}
		return factsResult(facts), nil
	case "list":
		facts, err := env.Store.Facts(MaxFactList)
		if err != nil {
			return Error(fmt.Sprintf("memory: %v", err)), nil
		}
		return factsResult(facts), nil
	case "remove":
		if strings.TrimSpace(a.Key) == "" {
			return Error("memory: key is required"), nil
		}
		if err := env.Store.DeleteFact(a.Key); err != nil {
			return Error(fmt.Sprintf("memory: %v", err)), nil
		}
		return Result{Text: "forgot " + a.Key, Display: "memory-" + a.Key}, nil
	}
	return Error(fmt.Sprintf("memory: unknown action %q", a.Action)), nil
}

func factsResult(facts []*store.Fact) Result {
	if len(facts) == 0 {
		return Result{Text: "no facts", Useless: true, Display: "memory"}
	}
	var sb strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&sb, "- %s: %s\n", f.Key, clip(f.Value, 300))
	}
	return Result{Text: strings.TrimRight(sb.String(), "\n"), Display: fmt.Sprintf("memory %d", len(facts))}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
