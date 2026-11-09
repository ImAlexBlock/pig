// Package compact implements context projection, compaction and the idle
// compaction scheduler.
package compact

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ImAlexBlock/pig/internal/provider"
	"github.com/ImAlexBlock/pig/internal/store"
)

// ImageTokens is the token budget charged for one archived image frame.
const DefaultImageTokens = 1100

// EstimateTokens estimates tokens for a string: ASCII/4 plus one token per
// non-ASCII rune, plus a 4-token message overhead.
func EstimateTokens(s string) int {
	ascii, wide := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			wide++
		}
	}
	return ascii/4 + wide + 4
}

// EntryTokens estimates the tokens an entry contributes to a request.
func EntryTokens(e *store.Entry) int {
	switch e.Kind {
	case store.KindMessage:
		m := e.Message()
		return EstimateTokens(m.Text) + EstimateTokens(m.Reasoning)
	case store.KindToolCall:
		c := e.ToolCall()
		return EstimateTokens(c.Name) + EstimateTokens(string(c.Args))
	case store.KindToolResult:
		r := e.ToolResult()
		return EstimateTokens(r.Text)
	case store.KindJobResult:
		j := e.JobResult()
		return EstimateTokens(j.Text)
	}
	return EstimateTokens(e.Content)
}

// ProjectOptions configures projection.
type ProjectOptions struct {
	ImageInput bool
	LoadFrame  func(artifactID string) (mime string, data []byte, ok bool)
}

// Project builds the wire messages for a session prefix.
func Project(cmp *store.Compaction, entries []*store.Entry, opt ProjectOptions) []provider.Message {
	var msgs []provider.Message
	if cmp != nil && (cmp.Summary != "" || cmp.Frames != "") {
		blocks := []provider.Block{}
		if cmp.Summary != "" {
			blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: cmp.Summary})
		}
		for _, f := range store.ParseFrames(cmp.Frames) {
			if opt.ImageInput && opt.LoadFrame != nil {
				if mime, data, ok := opt.LoadFrame(f.ArtifactID); ok {
					blocks = append(blocks, provider.Block{
						Type:  provider.BlockImage,
						Image: &provider.Image{MediaType: mime, Data: data},
					})
					continue
				}
			}
			blocks = append(blocks, provider.Block{
				Type: provider.BlockText,
				Text: "[image archive: artifact://" + f.ArtifactID + "]",
			})
		}
		if len(blocks) > 0 {
			msgs = append(msgs, provider.Message{Role: "user", Blocks: blocks})
		}
	}

	appendAssistant := func(blocks []provider.Block, raw json.RawMessage) {
		msgs = append(msgs, provider.Message{Role: "assistant", Blocks: blocks, Raw: raw})
	}
	lastAssistant := func() *provider.Message {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "assistant" {
				return &msgs[i]
			}
		}
		return nil
	}

	attachBlocks := func(m store.MessageContent) []provider.Block {
		var extra []provider.Block
		for _, id := range m.Attachments {
			if opt.ImageInput && opt.LoadFrame != nil {
				if mime, data, ok := opt.LoadFrame(id); ok && strings.HasPrefix(mime, "image/") {
					extra = append(extra, provider.Block{Type: provider.BlockImage, Image: &provider.Image{MediaType: mime, Data: data}})
					continue
				}
			}
			extra = append(extra, provider.Block{Type: provider.BlockText, Text: "[attachment artifact://" + id + "]"})
		}
		return extra
	}

	for _, e := range entries {
		switch e.Kind {
		case store.KindMessage:
			m := e.Message()
			if e.Role == "assistant" {
				blocks := []provider.Block{}
				if m.Text != "" {
					blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: m.Text})
				}
				appendAssistant(blocks, m.Raw)
				continue
			}
			blocks := []provider.Block{}
			if m.Text != "" {
				blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: m.Text})
			}
			blocks = append(blocks, attachBlocks(m)...)
			if len(blocks) == 0 {
				continue
			}
			msgs = append(msgs, provider.Message{Role: "user", Blocks: blocks})
		case store.KindToolCall:
			c := e.ToolCall()
			args := c.Args
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			block := provider.Block{Type: provider.BlockToolUse, ToolID: c.ID, ToolName: c.Name, Args: args}
			if a := lastAssistant(); a != nil && len(a.Raw) == 0 {
				a.Blocks = append(a.Blocks, block)
				continue
			}
			appendAssistant([]provider.Block{block}, nil)
		case store.KindToolResult:
			r := e.ToolResult()
			msgs = append(msgs, provider.Message{Role: "user", Blocks: []provider.Block{{
				Type:    provider.BlockToolResult,
				ToolID:  r.CallID,
				Text:    r.Text,
				IsError: r.IsError,
			}}})
		case store.KindJobResult:
			j := e.JobResult()
			text := fmt.Sprintf("[background job %s %s] %s", j.JobID, j.Status, j.Text)
			msgs = append(msgs, provider.Message{Role: "user", Blocks: []provider.Block{{
				Type: provider.BlockText, Text: text,
			}}})
		}
	}
	return provider.MergeMessages(msgs)
}

// ProjectTokens estimates the token size of projected messages.
func ProjectTokens(msgs []provider.Message, imageTokens int) int {
	total := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			switch b.Type {
			case provider.BlockText, provider.BlockThinking:
				total += EstimateTokens(b.Text)
			case provider.BlockImage:
				if imageTokens <= 0 {
					imageTokens = DefaultImageTokens
				}
				total += imageTokens
			case provider.BlockToolResult:
				total += EstimateTokens(b.Text)
			case provider.BlockToolUse:
				total += EstimateTokens(b.ToolName) + EstimateTokens(string(b.Args))
			}
		}
	}
	return total
}

// ContextTokens returns the larger of the estimate and the provider-reported
// input token count.
func ContextTokens(msgs []provider.Message, usageInput, imageTokens int) int {
	est := ProjectTokens(msgs, imageTokens)
	if usageInput > est {
		return usageInput
	}
	return est
}

// Prepared is the split of entries into summarised and retained parts.
type Prepared struct {
	Summarize       []*store.Entry
	Recent          []*store.Entry
	FirstKeptSeq    int
	PreviousSummary string
}

// dropped reports kinds that never reach the model.
func dropped(kind string) bool {
	switch kind {
	case store.KindCompaction, store.KindNotice, store.KindTodo:
		return true
	}
	return false
}

func isUserBoundary(e *store.Entry) bool {
	return e.Kind == store.KindMessage && e.Role == "user"
}

// Prepare splits entries at a safe cut point that keeps roughly keepRecent
// tokens, snaps to a user message boundary and never splits a tool exchange.
func Prepare(entries []*store.Entry, keepRecent int, previousSummary string) Prepared {
	filtered := make([]*store.Entry, 0, len(entries))
	for _, e := range entries {
		if dropped(e.Kind) {
			continue
		}
		filtered = append(filtered, e)
	}
	out := Prepared{PreviousSummary: previousSummary}
	if len(filtered) == 0 {
		out.FirstKeptSeq = 1
		return out
	}
	if keepRecent < 0 {
		keepRecent = 0
	}
	total := 0
	idx := len(filtered)
	for i := len(filtered) - 1; i >= 0; i-- {
		total += EntryTokens(filtered[i])
		idx = i
		if total >= keepRecent {
			break
		}
	}
	// Snap back to a user message so the retained window starts on a turn.
	for idx > 0 && !isUserBoundary(filtered[idx]) {
		idx--
	}
	if !isUserBoundary(filtered[idx]) {
		// No user message at all; keep everything.
		out.FirstKeptSeq = filtered[0].Seq
		out.Recent = filtered
		return out
	}
	out.FirstKeptSeq = filtered[idx].Seq
	out.Summarize = filtered[:idx]
	out.Recent = filtered[idx:]
	return out
}

// RenderSummarizeText renders entries as plain text for summarisation.
func RenderSummarizeText(entries []*store.Entry, toolResultMaxChars, toolArgMaxChars int) string {
	if toolResultMaxChars <= 0 {
		toolResultMaxChars = 2000
	}
	if toolArgMaxChars <= 0 {
		toolArgMaxChars = 2000
	}
	var sb strings.Builder
	for _, e := range entries {
		switch e.Kind {
		case store.KindMessage:
			m := e.Message()
			role := e.Role
			if role == "" {
				role = "assistant"
			}
			sb.WriteString("[" + role + "] " + m.Text + "\n")
		case store.KindToolCall:
			c := e.ToolCall()
			sb.WriteString("[tool " + c.Name + "] " + clip(string(c.Args), toolArgMaxChars) + "\n")
		case store.KindToolResult:
			r := e.ToolResult()
			sb.WriteString("[result " + r.Name + "] " + clip(r.Text, toolResultMaxChars) + "\n")
		case store.KindJobResult:
			j := e.JobResult()
			sb.WriteString("[job " + j.JobID + " " + j.Status + "] " + clip(j.Text, toolResultMaxChars) + "\n")
		}
	}
	return sb.String()
}

func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	head := s[:n*3/5]
	return head + "…"
}

// Todos returns the newest todo list persisted in a session.
func Todos(entries []*store.Entry) []store.TodoPhase {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == store.KindTodo {
			return entries[i].Todo().Phases
		}
	}
	return nil
}
