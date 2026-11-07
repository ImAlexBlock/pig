package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alexblock/pig/internal/compact"
	"github.com/alexblock/pig/internal/store"
	"github.com/alexblock/pig/internal/tool"
)

func (a *Agent) spawnHook() func(ctx context.Context, req tool.SubagentRequest) (tool.SubagentResult, error) {
	return func(ctx context.Context, req tool.SubagentRequest) (tool.SubagentResult, error) {
		return a.Spawn(ctx, req)
	}
}

// subagentTools intersects the requested tools with the configured allowlists.
func (a *Agent) subagentTools(requested []string) []string {
	want := requested
	if len(want) == 0 {
		want = a.Config.Subagents.DefaultTools
	}
	var out []string
	for _, n := range a.Config.Tools.Enabled {
		if n == "task" {
			continue
		}
		for _, w := range want {
			if n == w {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// Spawn creates a child session and runs it, or enqueues it as a job.
func (a *Agent) Spawn(ctx context.Context, req tool.SubagentRequest) (tool.SubagentResult, error) {
	if req.CallerDepth+1 > a.Config.Subagents.MaxDepth {
		return tool.SubagentResult{}, fmt.Errorf("subagent: depth limit reached")
	}
	model := req.Model
	if model == "" {
		chain := a.Runner.Chain("", "subagent")
		if len(chain) > 0 {
			model = chain[0]
		}
	}
	if model == "" {
		return tool.SubagentResult{}, fmt.Errorf("subagent: no model configured")
	}
	allowed := a.subagentTools(req.Tools)

	promptText := req.Prompt
	if c := strings.TrimSpace(req.Context); c != "" {
		promptText = "Context:\n" + c + "\n\n" + req.Prompt
	}
	child, err := a.Store.CreateSession(store.SessionOptions{
		CWD:      req.CallerCWD,
		Model:    model,
		ParentID: req.CallerSessionID,
		Depth:    req.CallerDepth + 1,
		Title:    req.Name,
	})
	if err != nil {
		return tool.SubagentResult{}, err
	}
	if _, err := a.Store.AppendEntry(child.ID, store.KindMessage, "user",
		store.NewMessageContent(store.MessageContent{Text: promptText}),
		compact.EstimateTokens(promptText)); err != nil {
		return tool.SubagentResult{}, err
	}

	if req.Background {
		if !a.Config.Background.Enabled {
			return tool.SubagentResult{}, fmt.Errorf("background: disabled")
		}
		if a.EnqueueJob == nil {
			return tool.SubagentResult{}, fmt.Errorf("background: unavailable")
		}
		toolsJSON, _ := json.Marshal(allowed)
		job := &store.Job{
			Kind:            "task",
			SessionID:       child.ID,
			ParentSessionID: req.CallerSessionID,
			Prompt:          promptText,
			OriginChannel:   req.OriginChannel,
			OriginChat:      req.OriginChat,
			Model:           model,
			Tools:           string(toolsJSON),
			Priority:        2,
			DeliverResult:   true,
		}
		id, err := a.EnqueueJob(job)
		if err != nil {
			return tool.SubagentResult{SessionID: child.ID}, err
		}
		return tool.SubagentResult{JobID: id, SessionID: child.ID}, nil
	}

	// Foreground: run the child inline on a copy with a forwarding sink so the
	// parent run shows progress.
	sub := *a
	parentRunID := req.ParentRunID
	parentSession := req.CallerSessionID
	childShort := shortID(child.ID)
	sub.Sink = func(ev Event) {
		if a.Sink == nil {
			return
		}
		switch ev.Type {
		case EvToolStart:
			a.Sink(Event{Type: EvNotice, RunID: parentRunID, SessionID: parentSession, Level: "info",
				Notice: fmt.Sprintf("subagent %s: %s", childShort, ev.ToolName)})
		case EvToolEnd:
			if ev.IsError {
				a.Sink(Event{Type: EvNotice, RunID: parentRunID, SessionID: parentSession, Level: "warn",
					Notice: fmt.Sprintf("subagent %s: %s failed", childShort, ev.ToolName)})
			}
		case EvNotice:
			a.Sink(Event{Type: EvNotice, RunID: parentRunID, SessionID: parentSession, Level: ev.Level,
				Notice: fmt.Sprintf("subagent %s: %s", childShort, ev.Notice)})
		case EvError:
			a.Sink(Event{Type: EvNotice, RunID: parentRunID, SessionID: parentSession, Level: "error",
				Notice: fmt.Sprintf("subagent %s: %s", childShort, ev.Error)})
		case EvTodo:
			a.Sink(ev)
		}
	}
	err = sub.Run(ctx, child.ID, Options{
		RunID:         store.NewRunID(),
		Tools:         allowed,
		Subagent:      true,
		Task:          req.Prompt,
		SubContext:    req.Context,
		OriginChannel: req.OriginChannel,
		OriginChat:    req.OriginChat,
	})
	if err != nil {
		return tool.SubagentResult{SessionID: child.ID}, err
	}
	text := a.lastAssistantText(child.ID)
	return tool.SubagentResult{Text: a.capSubagentText(child.ID, text), SessionID: child.ID}, nil
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// lastAssistantText returns the newest assistant message of a session.
func (a *Agent) lastAssistantText(sessionID string) string {
	entries, err := a.Store.AllEntries(sessionID)
	if err != nil {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == store.KindMessage && entries[i].Role == "assistant" {
			return entries[i].Message().Text
		}
	}
	return ""
}

// capSubagentText truncates a result and spills the full text to an artifact.
func (a *Agent) capSubagentText(sessionID, text string) string {
	limit := a.Config.Subagents.ResultMaxChar
	if limit <= 0 || len(text) <= limit {
		return text
	}
	art, err := a.Store.PutArtifact(sessionID, "text/plain", []byte(text))
	if err != nil {
		return safeSlice([]byte(text), limit, true)
	}
	head := safeSlice([]byte(text), limit, true)
	return head + fmt.Sprintf("\n[truncated: %d chars · artifact://%s]", len(text), art.ID)
}
