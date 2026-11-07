package skill

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/tool"
)

const stderrKeep = 8192

// Host owns one skill tool server process.
type Host struct {
	cfg   *config.Config
	skill *Skill
	cwd   string

	mu       sync.Mutex
	proc     *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	nextID   int64
	stderr   strings.Builder
	started  bool
	closed   bool
	lastUsed time.Time
	idle     *time.Timer
}

// NewHost returns a host for a skill with tools.
func NewHost(cfg *config.Config, sk *Skill, cwd string) *Host {
	return &Host{cfg: cfg, skill: sk, cwd: cwd}
}

func (h *Host) callTimeout() time.Duration {
	d := time.Duration(h.cfg.Skills.CallTimeoutSeconds) * time.Second
	if d <= 0 {
		d = 120 * time.Second
	}
	return d
}

func (h *Host) idleTimeout() time.Duration {
	d := time.Duration(h.cfg.Skills.IdleTimeoutSeconds) * time.Second
	if d <= 0 {
		d = 60 * time.Second
	}
	return d
}

// Call invokes one skill tool. Callers must not call concurrently.
func (h *Host) Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return "", true, fmt.Errorf("skill %s: server exited", h.skill.Name)
	}
	if err := h.ensureStarted(ctx); err != nil {
		return "", true, err
	}
	h.armIdle()
	params := map[string]any{"name": name, "arguments": json.RawMessage(args)}
	resp, err := h.request(ctx, "tools/call", params)
	if err != nil {
		return "", true, err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return "", true, fmt.Errorf("skill %s: invalid response: %v", h.skill.Name, err)
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
			sb.WriteString("\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n"), out.IsError, nil
}

func (h *Host) ensureStarted(ctx context.Context) error {
	if h.started {
		return nil
	}
	if h.skill.Manifest == nil || len(h.skill.Manifest.Command) == 0 {
		return fmt.Errorf("skill %s: no command", h.skill.Name)
	}
	args := h.skill.Manifest.Command
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...)
	cmd.Dir = h.skill.Dir
	cmd.Env = append(os.Environ(), "PIG_SKILL_DIR="+h.skill.Dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("skill %s: %v", h.skill.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("skill %s: %v", h.skill.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("skill %s: %v", h.skill.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("skill %s: %v", h.skill.Name, err)
	}
	h.proc = cmd
	h.stdin = stdin
	h.stdout = bufio.NewReaderSize(stdout, 1<<20)
	h.stderr.Reset()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := stderr.Read(buf)
			if n > 0 {
				cur := h.stderr.String() + string(buf[:n])
				if len(cur) > stderrKeep {
					cur = cur[len(cur)-stderrKeep:]
				}
				h.stderr.Reset()
				h.stderr.WriteString(cur)
			}
			if rerr != nil {
				return
			}
		}
	}()
	h.started = true
	h.lastUsed = time.Now()

	initParams := map[string]any{"protocol": 1, "skill_dir": h.skill.Dir, "cwd": h.cwd}
	if _, err := h.request(ctx, "initialize", initParams); err != nil {
		h.kill()
		return err
	}
	h.armIdle()
	return nil
}

func (h *Host) armIdle() {
	if h.idle != nil {
		h.idle.Stop()
	}
	h.idle = time.AfterFunc(h.idleTimeout(), func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.started || h.closed {
			return
		}
		_ = h.writeLocked(map[string]any{"jsonrpc": "2.0", "id": -1, "method": "shutdown"})
		h.kill()
	})
}

func (h *Host) writeLocked(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := h.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("skill %s: write: %v", h.skill.Name, err)
	}
	return nil
}

func (h *Host) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	h.nextID++
	id := h.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := h.writeLocked(req); err != nil {
		return nil, err
	}
	type readResult struct {
		raw json.RawMessage
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		for {
			line, err := h.stdout.ReadBytes('\n')
			if err != nil {
				done <- readResult{err: err}
				return
			}
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			var resp struct {
				ID     *int64          `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(line, &resp); err != nil {
				continue
			}
			if resp.ID == nil || *resp.ID != id {
				continue
			}
			if resp.Error != nil {
				done <- readResult{err: fmt.Errorf("%s", resp.Error.Message)}
				return
			}
			done <- readResult{raw: resp.Result}
			return
		}
	}()

	timer := time.NewTimer(h.callTimeout())
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			if strings.Contains(r.err.Error(), "EOF") {
				h.kill()
				return nil, fmt.Errorf("skill %s: server exited", h.skill.Name)
			}
			return nil, fmt.Errorf("skill %s: %v", h.skill.Name, r.err)
		}
		return r.raw, nil
	case <-timer.C:
		h.kill()
		return nil, fmt.Errorf("skill %s: call timed out", h.skill.Name)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *Host) kill() {
	if h.idle != nil {
		h.idle.Stop()
	}
	if h.proc != nil && h.proc.Process != nil {
		_ = h.proc.Process.Kill()
		go func(p *exec.Cmd) {
			_ = p.Wait()
		}(h.proc)
	}
	h.started = false
	h.proc = nil
	if h.stdin != nil {
		_ = h.stdin.Close()
		h.stdin = nil
	}
}

// Close shuts the server down.
func (h *Host) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	if h.started {
		_ = h.writeLocked(map[string]any{"jsonrpc": "2.0", "id": -1, "method": "shutdown"})
		done := make(chan struct{})
		go func() {
			time.Sleep(2 * time.Second)
			close(done)
		}()
		select {
		case <-done:
		}
	}
	h.kill()
}

// skillTool adapts a skill manifest tool to the tool interface.
type skillTool struct {
	host *Host
	sk   *Skill
	def  ToolDef
}

func (t *skillTool) Name() string        { return t.sk.Name + "__" + t.def.Name }
func (t *skillTool) Description() string { return t.def.Description }
func (t *skillTool) Schema() json.RawMessage {
	if len(t.def.InputSchema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
	return t.def.InputSchema
}
func (t *skillTool) ReadOnly() bool { return false }

func (t *skillTool) Run(ctx context.Context, c tool.Call, env *tool.Env) (tool.Result, error) {
	text, isErr, err := t.host.Call(ctx, t.def.Name, c.Args)
	if err != nil {
		return tool.Error(err.Error()), nil
	}
	return tool.Result{Text: text, IsError: isErr, Display: t.sk.Name + "__" + t.def.Name}, nil
}

// RegisterTools adds every manifest tool of every skill to the registry.
func (r *Registry) RegisterTools(reg *tool.Registry, cfg *config.Config, cwd string) {
	for _, sk := range r.List() {
		if sk.Manifest == nil {
			continue
		}
		host := NewHost(cfg, sk, cwd)
		for _, def := range sk.Tools {
			reg.Register(&skillTool{host: host, sk: sk, def: def})
		}
	}
}

// Close tears down all hosts.
func (r *Registry) Close() {}
