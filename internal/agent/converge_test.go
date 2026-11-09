package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/prompt"
	"github.com/ImAlexBlock/pig/internal/provider"
	"github.com/ImAlexBlock/pig/internal/store"
)

func convergeAgent() *Agent {
	return &Agent{Config: config.Default()}
}

func bashCall(cmd string) provider.Block {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return provider.Block{Type: provider.BlockToolUse, ToolID: "c1", ToolName: "bash", Args: args}
}

func TestRepeatCallsSteerThenStall(t *testing.T) {
	a := convergeAgent()
	st := &runState{}
	call := []provider.Block{bashCall("ls")}

	if steer, stalled := a.trackCalls(st, call); steer != "" || stalled {
		t.Fatalf("first call steered: %q %v", steer, stalled)
	}
	steer, stalled := a.trackCalls(st, call)
	if stalled || !strings.Contains(steer, "identical tool call") {
		t.Fatalf("second call: steer=%q stalled=%v", steer, stalled)
	}
	for i := 2; i < MaxRepeatCalls-1; i++ {
		if _, stalled := a.trackCalls(st, call); stalled {
			t.Fatalf("stalled early at repeat %d", i+1)
		}
	}
	if _, stalled := a.trackCalls(st, call); !stalled {
		t.Fatalf("repeat %d did not stall", MaxRepeatCalls)
	}
}

func TestDifferentCallsDoNotStall(t *testing.T) {
	a := convergeAgent()
	st := &runState{}
	for _, cmd := range []string{"ls", "pwd", "date", "whoami", "id"} {
		if _, stalled := a.trackCalls(st, []provider.Block{bashCall(cmd)}); stalled {
			t.Fatalf("stalled on %q", cmd)
		}
	}
}

func TestSoftRoundSteeringOnce(t *testing.T) {
	a := convergeAgent()
	st := &runState{toolRounds: SoftToolRounds - 1}
	if got := a.softRoundSteering(st); got != "" {
		t.Fatalf("steered under the soft budget: %q", got)
	}
	st.toolRounds = SoftToolRounds
	first := a.softRoundSteering(st)
	if !strings.Contains(first, "Wrap up") {
		t.Fatalf("soft steering = %q", first)
	}
	if again := a.softRoundSteering(st); again != "" {
		t.Fatalf("soft steering repeated: %q", again)
	}
}

func TestPromptExcludesVolatileChecklist(t *testing.T) {
	cfg := config.Default()
	out, _ := prompt.Build(prompt.Input{
		Config:  cfg,
		Session: &store.Session{CWD: "/tmp"},
		Tools:   []prompt.ToolDoc{{Name: "bash", Description: "run"}},
		Facts:   []string{"timezone: UTC"},
	})
	if strings.Contains(out, "Checklist") {
		t.Fatal("system prompt still carries a volatile checklist")
	}
	if !strings.Contains(out, "Remembered facts") || !strings.Contains(out, "timezone: UTC") {
		t.Fatalf("facts missing from prompt:\n%s", out)
	}
	// Same inputs must render identically: the prefix cache depends on it.
	again, _ := prompt.Build(prompt.Input{
		Config:  cfg,
		Session: &store.Session{CWD: "/tmp"},
		Tools:   []prompt.ToolDoc{{Name: "bash", Description: "run"}},
		Facts:   []string{"timezone: UTC"},
	})
	if out != again {
		t.Fatal("prompt rendering is not deterministic")
	}
}
