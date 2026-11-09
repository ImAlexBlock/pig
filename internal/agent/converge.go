package agent

import (
	"fmt"
	"strings"

	"github.com/ImAlexBlock/pig/internal/provider"
)

// Convergence control is built in, not configurable: these bounds exist to
// stop runaway tool loops, and a per-deployment knob only invites drift.
const (
	// MaxRepeatCalls ends a run once a byte-identical tool batch repeats this
	// many times; one repeat only earns a steering notice.
	MaxRepeatCalls = 4
	// SoftToolRounds adds one "wrap up" notice after this many tool batches.
	SoftToolRounds = 30
)

// trackCalls records the signature of a tool batch and reports (a) steering
// text when the batch repeats and (b) whether the run is stalled.
func (a *Agent) trackCalls(st *runState, calls []provider.Block) (string, bool) {
	if len(calls) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, c.ToolName+":"+string(c.Args))
	}
	sig := strings.Join(parts, "|")
	st.callSigs = append(st.callSigs, sig)
	if len(st.callSigs) > 32 {
		st.callSigs = st.callSigs[len(st.callSigs)-32:]
	}
	repeats := 1
	for i := len(st.callSigs) - 2; i >= 0 && st.callSigs[i] == sig; i-- {
		repeats++
	}
	if repeats >= MaxRepeatCalls {
		return "", true
	}
	if repeats >= 2 {
		return fmt.Sprintf("You ran the identical tool call %d times. The result will not change: change approach, or answer with what you have.", repeats), false
	}
	return "", false
}

// softRoundSteering warns once when the run passes its soft tool-round budget.
func (a *Agent) softRoundSteering(st *runState) string {
	if st.softWarned || st.toolRounds < SoftToolRounds {
		return ""
	}
	st.softWarned = true
	return fmt.Sprintf("You have used %d tool rounds. Wrap up: verify the result and answer, or report the blocker.", st.toolRounds)
}
