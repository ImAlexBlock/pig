package compact

import (
	"fmt"

	"github.com/ImAlexBlock/pig/internal/store"
)

// Shake constants: only tool results larger than ShakeMinEntryTokens are
// candidates, and a candidate must save at least ShakeMinSavings tokens.
const (
	ShakeMinEntryTokens = 4096
	ShakeMinSavings     = 2000
	ShakeProtectedTail  = 20000
)

// shakeCandidate elides large old tool results. The newest
// ShakeProtectedTail tokens are always kept.
func (m *Manager) shakeCandidate(prepared Prepared, tokensBefore int) (*candidate, error) {
	protected := ShakeProtectedTail
	keep := m.cfg.Compaction.KeepRecentTokens
	if keep > protected {
		protected = keep
	}
	// Walk backwards over the retained window to find the protection boundary.
	cut := len(prepared.Recent)
	acc := 0
	for i := len(prepared.Recent) - 1; i >= 0; i-- {
		acc += EntryTokens(prepared.Recent[i])
		cut = i
		if acc >= protected {
			break
		}
	}
	candidates := append([]*store.Entry{}, prepared.Summarize...)
	candidates = append(candidates, prepared.Recent[:cut]...)

	var edits []elideEdit
	saved := 0
	for _, e := range candidates {
		if e.Kind != store.KindToolResult {
			continue
		}
		r := e.ToolResult()
		before := EntryTokens(e)
		if before < ShakeMinEntryTokens {
			continue
		}
		placeholder := "[elided]"
		if r.Artifact != "" {
			placeholder = fmt.Sprintf("[elided; artifact://%s]", r.Artifact)
		}
		after := EstimateTokens(placeholder)
		if before-after <= 0 {
			continue
		}
		saved += before - after
		edits = append(edits, elideEdit{entry: e, text: placeholder})
	}
	if len(edits) == 0 || saved < ShakeMinSavings {
		return nil, nil
	}
	tokensAfter := tokensBefore - saved
	if tokensAfter < 0 {
		tokensAfter = 0
	}
	return &candidate{
		method:       "shake",
		firstKeptSeq: -1,
		tokensAfter:  tokensAfter,
		elide:        edits,
	}, nil
}
