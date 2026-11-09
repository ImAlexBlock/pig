// Package tui implements the Bubble Tea client.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ImAlexBlock/pig/internal/agent"
	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/ipc"
	"github.com/ImAlexBlock/pig/internal/store"
)

// Options configures the TUI.
type Options struct {
	Client    *ipc.Client
	Config    *config.Config
	Paths     config.Paths
	SessionID string
	Model     string
}

// eventMsg wraps one run event.
type eventMsg struct {
	ev agent.Event
	ch <-chan agent.Event
}

type snapshotMsg struct {
	session *store.Session
	entries []*store.Entry
	ch      <-chan agent.Event
}

type errMsg struct{ err error }
type doneMsg struct{}
type elapsedMsg struct{}
type statusMsg struct{ text string }

// Model is the TUI state.
type Model struct {
	opts Options
	cfg  *config.Config

	width, height int

	input    textarea.Model
	viewport viewport.Model

	lines []string

	sessionID     string
	model         string
	runID         string
	streaming     bool
	phase         string
	lastTool      string
	started       time.Time
	elapsed       time.Duration
	tokens        int
	cost          float64
	jobCount      int
	connected     bool
	status        string
	partial       strings.Builder
	thinking      strings.Builder
	showThink     bool
	streamingLine bool

	events   chan agent.Event
	quitting bool
}

// New builds the TUI model.
func New(opts Options) Model {
	ta := textarea.New()
	ta.Placeholder = "message · Enter sends · Alt+Enter newline · /help"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Focus()
	ta.SetHeight(3)

	vp := viewport.New(80, 20)
	m := Model{
		opts:      opts,
		cfg:       opts.Config,
		input:     ta,
		viewport:  vp,
		sessionID: opts.SessionID,
		model:     opts.Model,
		events:    make(chan agent.Event, 512),
	}
	m.lines = append(m.lines, "pig — /help for commands")
	return m
}

// Ch returns the event channel used by the IPC callback.
func (m *Model) Ch() chan agent.Event {
	if m.events == nil {
		m.events = make(chan agent.Event, 512)
	}
	return m.events
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.tick(), m.waitEvent())
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return elapsedMsg{} })
}

func (m Model) waitEvent() tea.Cmd {
	ch := m.events
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return doneMsg{}
		}
		return eventMsg{ev: ev, ch: ch}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case elapsedMsg:
		if m.streaming {
			m.elapsed = time.Since(m.started)
		}
		return m, m.tick()

	case eventMsg:
		m.applyEvent(msg.ev)
		return m, m.waitEvent()

	case statusMsg:
		m.status = msg.text
		return m, nil

	case errMsg:
		m.appendLine(styleError.Render("✗ " + msg.err.Error()))
		m.streaming = false
		return m, nil

	case doneMsg:
		m.connected = false
		m.appendLine(styleMuted.Render("daemon disconnected · /reconnect"))
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+d":
			m.quitting = true
			return m, tea.Quit
		case "ctrl+c":
			if m.streaming && m.opts.Client != nil {
				_, _ = m.opts.Client.Call(context.Background(), "run.cancel", map[string]any{"run_id": m.runID})
				m.appendLine(styleMuted.Render("· cancel requested"))
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case "alt+enter":
			m.input.InsertString("\n")
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			if strings.HasPrefix(text, "/") {
				nm, cmd := m.runCommand(text)
				return *nm, cmd
			}
			nm, cmd := m.submit(text)
			return *nm, cmd
		case "pgup", "ctrl+u":
			m.viewport.HalfViewUp()
			return m, nil
		case "pgdown", "ctrl+o":
			m.viewport.HalfViewDown()
			return m, nil
		}
		if msg.String() == "ctrl+t" {
			m.showThink = !m.showThink
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	inputH := 4
	chrome := 4
	vpH := m.height - inputH - chrome
	if vpH < 3 {
		vpH = 3
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpH
	m.input.SetWidth(m.width - 2)
	m.refreshViewport()
}

func (m *Model) appendLine(s string) {
	m.lines = append(m.lines, s)
	if len(m.lines) > 5000 {
		m.lines = m.lines[len(m.lines)-5000:]
	}
	m.refreshViewport()
}

func (m *Model) refreshViewport() {
	m.viewport.SetContent(strings.Join(m.lines, "\n"))
	m.viewport.GotoBottom()
}

func (m *Model) applyEvent(ev agent.Event) {
	switch ev.Type {
	case agent.EvRunStart:
		m.runID = ev.RunID
		m.sessionID = ev.SessionID
		if ev.Model != "" {
			m.model = ev.Model
		}
		m.streaming = true
		m.started = time.Now()
		m.partial.Reset()
		m.thinking.Reset()
		m.phase = "thinking"
	case agent.EvTextDelta:
		m.partial.WriteString(ev.Text)
		m.phase = "writing"
		m.refreshPartial()
	case agent.EvThinkingDelta:
		m.thinking.WriteString(ev.Text)
	case agent.EvToolStart:
		m.phase = "tool: " + ev.ToolName
		m.lastTool = ev.ToolName
		m.appendLine(styleTool.Render(fmt.Sprintf("→ %s %s", ev.ToolName, linkArgs(ev.Args))))
	case agent.EvToolEnd:
		status := "✓"
		if ev.IsError {
			status = "✗"
		}
		line := fmt.Sprintf("%s %s", status, ev.ToolName)
		if ev.Display != "" {
			line += " · " + ev.Display
		}
		if ev.Result != "" {
			line += " · " + firstLine(ev.Result, 90)
		}
		m.appendLine(styleTool.Render(line))
	case agent.EvNotice:
		m.appendLine(styleMuted.Render("· " + ev.Notice))
	case agent.EvTodo:
		m.appendLine(renderTodo(ev.Phases))
	case agent.EvUsage:
		if ev.Usage != nil {
			m.tokens = ev.Usage.Input + ev.Usage.Output
		}
		m.cost += ev.CostUSD
	case agent.EvCompactStart, agent.EvCompactEnd:
		m.appendLine(styleMuted.Render("· compaction " + ev.Method))
	case agent.EvJobUpdate:
		if ev.Job != nil {
			m.jobCount++
			m.appendLine(styleMuted.Render(fmt.Sprintf("· job %s %s", ev.Job.ID, ev.Job.Status)))
		}
	case agent.EvRunDetached:
		m.appendLine(styleMuted.Render("· detached to background"))
	case agent.EvError:
		m.appendLine(styleError.Render("✗ " + ev.Error))
		m.streaming = false
	case agent.EvRunEnd:
		m.finalizePartial()
		m.streaming = false
		m.phase = ""
	}
}

func (m *Model) refreshPartial() {
	text := m.partial.String()
	if text == "" {
		return
	}
	if n := len(m.lines); n > 0 && m.streamingLine {
		m.lines[n-1] = text
	} else {
		m.lines = append(m.lines, text)
		m.streamingLine = true
	}
	m.refreshViewport()
}

func (m *Model) finalizePartial() {
	if !m.streamingLine {
		return
	}
	text := strings.TrimSpace(m.partial.String())
	if text == "" {
		m.lines = m.lines[:len(m.lines)-1]
		m.streamingLine = false
		m.refreshViewport()
		return
	}
	m.lines[len(m.lines)-1] = renderMarkdown(text, m.width)
	m.streamingLine = false
	m.refreshViewport()
}

// submit starts a run for the typed text.
func (m *Model) submit(text string) (*Model, tea.Cmd) {
	m.appendLine(styleUser.Render("› " + text))
	if m.opts.Client == nil {
		m.appendLine(styleError.Render("✗ no daemon connection"))
		return m, nil
	}
	params := map[string]any{
		"prompt":     text,
		"session_id": m.sessionID,
		"client":     "tui",
		"queue":      true,
	}
	if m.model != "" {
		params["model"] = m.model
	}
	var out struct {
		RunID     string `json:"run_id"`
		SessionID string `json:"session_id"`
		Queued    bool   `json:"queued"`
	}
	if err := callM(m.opts.Client, "run.start", params, &out); err != nil {
		m.appendLine(styleError.Render("✗ " + err.Error()))
		return m, nil
	}
	m.sessionID = out.SessionID
	m.runID = out.RunID
	if out.Queued {
		m.appendLine(styleMuted.Render("· queued"))
	}
	return m, nil
}

// runCommand handles a slash command.
func (m *Model) runCommand(text string) (*Model, tea.Cmd) {
	fields := strings.Fields(text)
	cmd := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(text, cmd))
	client := m.opts.Client
	switch cmd {
	case "/quit", "/exit":
		m.quitting = true
		return m, tea.Quit
	case "/help":
		for _, ln := range helpLines() {
			m.appendLine(ln)
		}
		return m, nil
	case "/new":
		var out struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
		}
		if err := callM(client, "session.new", map[string]any{}, &out); err != nil {
			m.appendLine(styleError.Render("✗ " + err.Error()))
			return m, nil
		}
		m.sessionID = out.Session.ID
		m.lines = nil
		m.appendLine(styleMuted.Render("· new session " + m.sessionID))
		return m, nil
	case "/resume", "/model", "/compact", "/export", "/jobs", "/agents", "/skills", "/config", "/status", "/stop", "/background", "/reconnect":
		m.appendLine(styleMuted.Render("· " + m.commandEcho(cmd, rest)))
		return m.executeCommand(cmd, rest)
	}
	m.appendLine(styleError.Render("✗ unknown command " + cmd))
	return m, nil
}

func (m *Model) commandEcho(cmd, rest string) string {
	if rest == "" {
		return cmd
	}
	return cmd + " " + rest
}
