package compact

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ImAlexBlock/pig/internal/provider"
	"github.com/ImAlexBlock/pig/internal/store"
)

// LLMSummaryTimeout bounds one summarisation request.
const LLMSummaryTimeout = 120 * time.Second

// SummarySystemPrompt is the fixed instruction used for LLM summaries.
const SummarySystemPrompt = `Summarize the conversation below for continuation.

Use exactly these section headings in this order:
## Goal
## Done
## Open
## Files
## Errors

Put the Files section content inside <files></files>, one path per line,
each followed by (Read), (Write) or (RW). Keep the whole summary under 4096
tokens.`

func (m *Manager) llmCandidate(ctx context.Context, req Request, prepared Prepared, tokensBefore int) (*candidate, error) {
	summary, err := m.summarizeEntries(ctx, req, prepared)
	if err != nil {
		return nil, err
	}
	projected := Project(&store.Compaction{Summary: summary}, prepared.Recent, ProjectOptions{
		ImageInput: req.ModelSupportsImages,
	})
	cand := &candidate{
		method:       "llm",
		summary:      summary,
		firstKeptSeq: prepared.FirstKeptSeq,
		sourceText:   RenderSummarizeText(prepared.Summarize, m.cfg.Snapcompact.ToolResultMaxChars, m.cfg.Snapcompact.ToolArgMaxChars),
	}
	cand.tokensAfter = ProjectTokens(projected, m.cfg.Snapcompact.ImageTokens) +
		EstimateTokens(req.SystemText) + req.ToolsTokens
	return cand, nil
}

// summarizeEntries asks the compact role for a structured summary.
func (m *Manager) summarizeEntries(ctx context.Context, req Request, prepared Prepared) (string, error) {
	if m.runner == nil {
		return "", fmt.Errorf("no model available")
	}
	body := RenderSummarizeText(prepared.Summarize, m.cfg.Snapcompact.ToolResultMaxChars, m.cfg.Snapcompact.ToolArgMaxChars)
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("nothing to summarize")
	}
	var user strings.Builder
	if prepared.PreviousSummary != "" {
		user.WriteString("<previous-summary>\n")
		user.WriteString(prepared.PreviousSummary)
		user.WriteString("\n</previous-summary>\n\n")
	}
	user.WriteString(body)

	sys := SummarySystemPrompt
	if req.Instructions != "" {
		sys += "\n\nAdditional instructions: " + req.Instructions
	}

	chain := m.runner.Chain("", "compact")
	rctx, cancel := context.WithTimeout(ctx, LLMSummaryTimeout)
	defer cancel()
	var sb strings.Builder
	res, _, err := m.runner.Stream(rctx, "compact", chain, provider.Request{
		System:    []provider.Block{{Type: provider.BlockText, Text: sys}},
		Messages:  []provider.Message{{Role: "user", Blocks: []provider.Block{{Type: provider.BlockText, Text: user.String()}}}},
		MaxTokens: 4096,
		SessionID: req.SessionID,
	}, func(ev provider.Event) error {
		if ev.Type == provider.EvTextDelta {
			sb.WriteString(ev.Text)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		out = strings.TrimSpace(provider.TextOf(res.Message))
	}
	if out == "" {
		return "", fmt.Errorf("empty summary")
	}
	return out, nil
}
