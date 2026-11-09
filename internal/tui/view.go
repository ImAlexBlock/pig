package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ImAlexBlock/pig/internal/agent"
	"github.com/ImAlexBlock/pig/internal/ipc"
	"github.com/ImAlexBlock/pig/internal/store"
)

var (
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleError  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleTool   = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	styleUser   = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	styleBar    = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	styleAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
)

// callM performs one RPC and decodes the result.
func callM(c *ipc.Client, method string, params, out any) error {
	if c == nil {
		return fmt.Errorf("no daemon connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := c.Call(ctx, method, params)
	if err != nil {
		return err
	}
	if out != nil {
		return ipc.Decode(raw, out)
	}
	return nil
}

// Run starts the Bubble Tea program.
func Run(ctx context.Context, opts Options) error {
	m := New(opts)
	if opts.Client != nil {
		opts.Client.On("run.event", func(raw json.RawMessage) {
			var payload struct {
				RunID string      `json:"run_id"`
				Event agent.Event `json:"event"`
			}
			if err := ipc.Decode(raw, &payload); err != nil {
				return
			}
			select {
			case m.events <- payload.Event:
			default:
			}
		})
		opts.Client.On("run.snapshot", func(raw json.RawMessage) {
			var payload struct {
				Session     *store.Session `json:"session"`
				EntriesTail []*store.Entry `json:"entries_tail"`
			}
			if err := ipc.Decode(raw, &payload); err != nil {
				return
			}
			for _, e := range payload.EntriesTail {
				m.lines = append(m.lines, renderEntry(e))
			}
			m.refreshViewport()
		})
		// Attach to the session so queued replies and job updates arrive.
		var out struct {
			RunID     string `json:"run_id"`
			SessionID string `json:"session_id"`
			Active    bool   `json:"active"`
		}
		if err := callM(opts.Client, "run.attach", map[string]any{"session_id": m.sessionID}, &out); err == nil {
			if out.Active {
				m.runID = out.RunID
				m.streaming = true
			}
		}
	}
	m.connected = opts.Client != nil
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

func (m Model) View() string {
	bar := m.statusBar()
	body := m.viewport.View()
	input := m.input.View()
	footer := styleMuted.Render(commandHints())
	h := m.height
	if h <= 0 {
		h = 24
	}
	return lipgloss.JoinVertical(lipgloss.Left, bar, body, input, footer)
}

func (m Model) statusBar() string {
	model := m.model
	if model == "" {
		model = "(default)"
	}
	session := m.sessionID
	if len(session) > 12 {
		session = session[:12]
	}
	if session == "" {
		session = "new"
	}
	conn := "daemon"
	if !m.connected {
		conn = "offline"
	}
	parts := []string{styleAccent.Render(model), session, conn, fmt.Sprintf("jobs %d", m.jobCount)}
	if m.streaming {
		parts = append(parts, fmt.Sprintf("⏳ %s", m.elapsed.Round(time.Second)))
		if m.phase != "" {
			parts = append(parts, m.phase)
		}
	}
	if m.tokens > 0 {
		parts = append(parts, fmt.Sprintf("%d tok", m.tokens))
	}
	if m.cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", m.cost))
	}
	return styleBar.Width(max(m.width, 20)).Render(strings.Join(parts, " · "))
}

func commandHints() string {
	return "enter send · alt+enter newline · ctrl+c cancel · ctrl+d quit · /help"
}

func helpLines() []string {
	return []string{
		styleMuted.Render("/new        start a new session"),
		styleMuted.Render("/resume <id> resume a session"),
		styleMuted.Render("/model [r]  show or set the model"),
		styleMuted.Render("/compact    compact the context now"),
		styleMuted.Render("/background move the current turn to a job"),
		styleMuted.Render("/jobs       recent background jobs"),
		styleMuted.Render("/agents     subagent sessions"),
		styleMuted.Render("/skills     discovered skills"),
		styleMuted.Render("/config     configuration summary"),
		styleMuted.Render("/status     session status"),
		styleMuted.Render("/stop       cancel the running turn"),
		styleMuted.Render("/export     write the session to a file"),
		styleMuted.Render("/reconnect  reattach to the daemon"),
		styleMuted.Render("/quit       leave the TUI"),
	}
}

// executeCommand runs a slash command against the daemon.
func (m *Model) executeCommand(cmd, rest string) (*Model, tea.Cmd) {
	client := m.opts.Client
	switch cmd {
	case "/model":
		if rest != "" {
			if err := callM(client, "session.set_model", map[string]any{"id": m.sessionID, "model": rest}, nil); err != nil {
				m.appendLine(styleError.Render("✗ " + err.Error()))
				return m, nil
			}
			m.model = rest
			m.appendLine(styleMuted.Render("· model set to " + rest))
			return m, nil
		}
		m.appendLine(styleMuted.Render("· model: " + m.model))
	case "/compact":
		var res struct {
			Method       string `json:"method"`
			TokensBefore int    `json:"tokens_before"`
			TokensAfter  int    `json:"tokens_after"`
			Compacted    bool   `json:"compacted"`
		}
		if err := callM(client, "compact.now", map[string]any{"id": m.sessionID}, &res); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		if !res.Compacted {
			m.appendLine(styleMuted.Render("· nothing to compact"))
			return m, nil
		}
		m.appendLine(styleMuted.Render(fmt.Sprintf("· compacted with %s: %d → %d", res.Method, res.TokensBefore, res.TokensAfter)))
	case "/background":
		if m.runID == "" {
			m.appendLine(styleMuted.Render("· nothing running"))
			return m, nil
		}
		if err := callM(client, "run.detach", map[string]any{"run_id": m.runID}, nil); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		m.appendLine(styleMuted.Render("· will detach at the next safe boundary"))
	case "/stop":
		if m.runID == "" {
			m.appendLine(styleMuted.Render("· nothing running"))
			return m, nil
		}
		if err := callM(client, "run.cancel", map[string]any{"run_id": m.runID}, nil); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		m.appendLine(styleMuted.Render("· cancel requested"))
	case "/jobs":
		var out struct {
			Jobs []*store.Job `json:"jobs"`
		}
		if err := callM(client, "jobs.list", map[string]any{"limit": 15}, &out); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		for _, j := range out.Jobs {
			m.appendLine(styleMuted.Render(fmt.Sprintf("  %s %s %s", j.ID, j.Kind, j.Status)))
		}
	case "/agents":
		var out struct {
			Sessions []*store.Session `json:"sessions"`
		}
		if err := callM(client, "session.list", map[string]any{"limit": 100}, &out); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		found := 0
		for _, s := range out.Sessions {
			if s.ParentID == "" {
				continue
			}
			found++
			m.appendLine(styleMuted.Render(fmt.Sprintf("  %s parent=%s depth=%d", s.ID, s.ParentID, s.Depth)))
		}
		if found == 0 {
			m.appendLine(styleMuted.Render("· no subagent sessions"))
		}
	case "/skills":
		var out struct {
			Skills []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"skills"`
		}
		if err := callM(client, "skills.list", nil, &out); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		for _, s := range out.Skills {
			m.appendLine(styleMuted.Render("  " + s.Name + ": " + s.Description))
		}
	case "/config":
		if m.opts.Config != nil {
			m.appendLine(styleMuted.Render("· config: " + m.opts.Config.Path))
			chain := m.opts.Config.RoleChain("main")
			m.appendLine(styleMuted.Render("· roles.main: " + strings.Join(chain, " → ")))
		}
	case "/status":
		var out struct {
			Session *store.Session `json:"session"`
		}
		if err := callM(client, "session.load", map[string]any{"id": m.sessionID, "tail": 1}, &out); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		if out.Session != nil {
			m.appendLine(styleMuted.Render(fmt.Sprintf("· %s model=%s cwd=%s", out.Session.ID, out.Session.Model, out.Session.CWD)))
		}
	case "/export":
		var out struct {
			Session *store.Session `json:"session"`
			Entries []*store.Entry `json:"entries"`
		}
		if err := callM(client, "session.load", map[string]any{"id": m.sessionID, "tail": 500}, &out); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		path := filepath.Join(os.TempDir(), "pig-"+m.sessionID+".json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		m.appendLine(styleMuted.Render("· wrote " + path))
	case "/resume":
		if rest == "" {
			m.appendLine(styleMuted.Render("· usage: /resume <id>"))
			return m, nil
		}
		var out struct {
			Session *store.Session `json:"session"`
		}
		if err := callM(client, "session.load", map[string]any{"id": rest, "tail": 200}, &out); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		m.sessionID = rest
		m.lines = nil
		m.appendLine(styleMuted.Render("· resumed " + rest))
	case "/reconnect":
		if err := callM(client, "daemon.status", nil, nil); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		m.connected = true
		m.appendLine(styleMuted.Render("· reconnected"))
	}
	return m, nil
}

func renderEntry(e *store.Entry) string {
	switch e.Kind {
	case store.KindMessage:
		m := e.Message()
		if e.Role == "user" {
			return styleUser.Render("› " + m.Text)
		}
		return renderMarkdown(m.Text, 100)
	case store.KindToolCall:
		c := e.ToolCall()
		return styleTool.Render(fmt.Sprintf("→ %s %s", c.Name, linkArgs(string(c.Args))))
	case store.KindToolResult:
		r := e.ToolResult()
		mark := "✓"
		if r.IsError {
			mark = "✗"
		}
		return styleTool.Render(fmt.Sprintf("%s %s · %s", mark, r.Name, firstLine(r.Text, 90)))
	case store.KindNotice:
		return styleMuted.Render("· " + e.Notice().Text)
	case store.KindTodo:
		return renderTodo(e.Todo().Phases)
	case store.KindJobResult:
		j := e.JobResult()
		return styleMuted.Render(fmt.Sprintf("· job %s %s", j.JobID, j.Status))
	}
	return ""
}

func renderTodo(phases []store.TodoPhase) string {
	if len(phases) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, ph := range phases {
		sb.WriteString(styleMuted.Render("· " + ph.Name))
		for _, it := range ph.Items {
			mark := " "
			switch it.Status {
			case "done":
				mark = "x"
			case "active":
				mark = ">"
			case "blocked":
				mark = "!"
			}
			sb.WriteString("\n  " + styleMuted.Render("["+mark+"] "+it.Text))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func linkArgs(args string) string {
	args = strings.Join(strings.Fields(args), " ")
	if len(args) > 120 {
		args = args[:120] + "…"
	}
	return args
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
