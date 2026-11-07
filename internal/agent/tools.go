package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/provider"
	"github.com/alexblock/pig/internal/store"
	"github.com/alexblock/pig/internal/tool"
)

// runToolBatch executes one tool batch and persists every result.
func (a *Agent) runToolBatch(ctx context.Context, st *runState, calls []provider.Block) {
	for _, c := range calls {
		args := string(c.Args)
		a.emit(Event{
			Type: EvToolStart, RunID: st.runID, SessionID: st.sess.ID,
			ToolID: c.ToolID, ToolName: c.ToolName, Args: clipDisplay(args, 400),
		})
		res := a.invoke(ctx, st, c)
		a.emit(Event{
			Type: EvToolEnd, RunID: st.runID, SessionID: st.sess.ID,
			ToolID: c.ToolID, ToolName: c.ToolName,
			Result: clipDisplay(res.Text, 2000), IsError: res.IsError, Display: res.Display,
		})
		content := store.NewToolResultContent(store.ToolResultContent{
			CallID:   c.ToolID,
			Name:     c.ToolName,
			Text:     res.Text,
			Artifact: res.Artifact,
			IsError:  res.IsError,
			Useless:  res.Useless,
		})
		if _, err := a.Store.AppendEntry(st.sess.ID, store.KindToolResult, "", content, 0); err != nil {
			a.notice(st, "failed to persist tool result: "+err.Error())
			return
		}
	}
}

// invoke runs one tool call with panic containment.
func (a *Agent) invoke(ctx context.Context, st *runState, c provider.Block) (res tool.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tool.Error(fmt.Sprintf("tool panicked: %v", r))
		}
	}()
	t, ok := a.Tools.Get(c.ToolName)
	if !ok {
		return tool.Error("unknown tool " + c.ToolName)
	}
	args := c.Args
	if len(args) > 0 {
		var probe any
		if err := json.Unmarshal(args, &probe); err != nil {
			return tool.Error("invalid tool arguments: " + err.Error())
		}
	}
	call := tool.Call{ID: c.ToolID, Name: c.ToolName, Args: args}
	out, err := t.Run(ctx, call, st.env)
	if err != nil {
		return tool.Error(err.Error())
	}
	return a.truncate(st, out)
}

// truncate redacts secrets and spills oversized results to an artifact.
func (a *Agent) truncate(st *runState, res tool.Result) tool.Result {
	if len(st.env.Secrets) > 0 {
		res.Text = config.Redact(res.Text, st.env.Secrets)
	}
	limit := a.Config.Tools.MaxOutputBytes
	if limit <= 0 {
		limit = 65536
	}
	lines := strings.Count(res.Text, "\n") + 1
	if len(res.Text) <= limit && lines <= 2000 {
		return res
	}
	raw := []byte(res.Text)
	art, err := a.Store.PutArtifact(st.sess.ID, "text/plain", raw)
	if err != nil {
		return res
	}
	head := safeSlice(raw, ToolResultHeadKeep, true)
	tail := safeSlice(raw, ToolResultTailKeep, false)
	res.Artifact = art.ID
	res.Text = fmt.Sprintf("%s\n[truncated: %d bytes · artifact://%s]\n%s", head, len(raw), art.ID, tail)
	return res
}

// safeSlice returns a UTF-8 aligned prefix (fromHead) or suffix.
func safeSlice(b []byte, n int, fromHead bool) string {
	if n <= 0 {
		return ""
	}
	if len(b) <= n {
		return string(b)
	}
	if fromHead {
		cut := b[:n]
		for len(cut) > 0 && !utf8.Valid(cut) {
			cut = cut[:len(cut)-1]
		}
		return string(cut)
	}
	cut := b[len(b)-n:]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[1:]
	}
	return string(cut)
}

func clipDisplay(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return safeSlice([]byte(s), n, true) + "…"
}
