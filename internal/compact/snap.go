package compact

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alexblock/pig/internal/compact/snapcompact"
	"github.com/alexblock/pig/internal/store"
)

// SnapSummarizeTimeout bounds the optional LLM summary that labels the archive.
const SnapSummarizeTimeout = 30 * time.Second

func (m *Manager) snapcompactCandidate(ctx context.Context, req Request, prepared Prepared, tokensBefore, budget int) (*candidate, error) {
	text := snapcompact.Serialize(prepared.Summarize, snapcompact.SerializeOptions{
		ToolResultMaxChars: m.cfg.Snapcompact.ToolResultMaxChars,
		ToolArgMaxChars:    m.cfg.Snapcompact.ToolArgMaxChars,
		PreviousSummary:    prepared.PreviousSummary,
	})
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	rendered, err := snapcompact.Render(text, snapcompact.Options{
		FrameWidth:     m.cfg.Snapcompact.FrameWidth,
		MaxFrameHeight: m.cfg.Snapcompact.MaxFrameHeight,
	})
	if err != nil {
		return nil, err
	}

	imageTokens := m.cfg.Snapcompact.ImageTokens
	if imageTokens <= 0 {
		imageTokens = DefaultImageTokens
	}
	recentTokens := ProjectTokens(Project(nil, prepared.Recent, ProjectOptions{}), imageTokens)
	room := budget - EstimateTokens(req.SystemText) - req.ToolsTokens - recentTokens

	maxFrames := m.cfg.Snapcompact.MaxFrames
	if maxFrames < 1 {
		maxFrames = 40
	}
	want := 1
	if room > 0 {
		want = room / imageTokens
	}
	if want > maxFrames {
		want = maxFrames
	}
	if want < 1 {
		want = 1
	}
	maxImages := m.cfg.Snapcompact.MaxImagesPerRequest
	if maxImages > 0 && want > maxImages {
		want = maxImages
	}
	// Keep the newest frames when the budget is tight.
	if want > len(rendered) {
		want = len(rendered)
	}
	selected := rendered[len(rendered)-want:]
	if maxBytes := m.cfg.Snapcompact.MaxRequestImageBytes; maxBytes > 0 {
		total := 0
		start := len(selected)
		for i := len(selected) - 1; i >= 0; i-- {
			total += len(selected[i].PNG)
			if total > maxBytes {
				break
			}
			start = i
		}
		selected = selected[start:]
		if len(selected) == 0 {
			selected = rendered[len(rendered)-1:]
		}
	}

	summary := fmt.Sprintf("Earlier context archived as %d image frames.", len(selected))
	if m.cfg.Snapcompact.Summarize && m.runner != nil {
		sctx, cancel := context.WithTimeout(ctx, SnapSummarizeTimeout)
		out, serr := m.summarizeEntries(sctx, Request{
			SessionID:    req.SessionID,
			Reason:       req.Reason,
			Instructions: "Keep it under 1024 tokens.",
		}, prepared)
		cancel()
		if serr == nil && strings.TrimSpace(out) != "" {
			summary = out
		}
	}
	if files := RenderFiles(FilesFromEntries(prepared.Summarize)); files != "" {
		summary += "\n\n" + files
	}

	pngs := make([][]byte, 0, len(selected))
	for _, f := range selected {
		pngs = append(pngs, f.PNG)
	}
	tokensAfter := EstimateTokens(req.SystemText) + req.ToolsTokens +
		EstimateTokens(summary) + len(pngs)*imageTokens + recentTokens
	return &candidate{
		method:       "snapcompact",
		summary:      summary,
		firstKeptSeq: prepared.FirstKeptSeq,
		tokensAfter:  tokensAfter,
		sourceText:   text,
		pngs:         pngs,
	}, nil
}

// persistFrames uploads rendered PNGs and returns the frame descriptors.
func (m *Manager) persistFrames(sessionID string, pngs [][]byte) ([]store.Frame, error) {
	frames := make([]store.Frame, 0, len(pngs))
	for _, p := range pngs {
		a, err := m.store.PutArtifact(sessionID, "image/png", p)
		if err != nil {
			return nil, err
		}
		frames = append(frames, store.Frame{ArtifactID: a.ID, Bytes: len(p)})
	}
	return frames, nil
}
