package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"encoding/json"

	"github.com/ImAlexBlock/pig/internal/rtk"
)

const bashSchema = `{
  "type": "object",
  "properties": {
    "command":         {"type": "string", "description": "Shell command executed with /bin/bash -lc."},
    "timeout_seconds": {"type": "integer", "description": "Timeout in seconds (max 3600)."},
    "cwd":             {"type": "string", "description": "Working directory (default: session cwd)."},
    "raw":             {"type": "boolean", "description": "Skip RTK compression."}
  },
  "required": ["command"],
  "additionalProperties": false
}`

const (
	maxBashCapture = 8 << 20
	maxBashTimeout = 3600
	stderrTail     = 50
)

type bashTool struct{}

func (bashTool) Name() string { return "bash" }
func (bashTool) Description() string {
	return "Run a shell command. Output is compressed by RTK unless raw is set; timeout kills the process group."
}
func (bashTool) Schema() json.RawMessage { return json.RawMessage(bashSchema) }
func (bashTool) ReadOnly() bool          { return false }

type bashArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Cwd            string `json:"cwd"`
	Raw            bool   `json:"raw"`
}

type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.max <= 0 {
		l.max = maxBashCapture
	}
	room := l.max - l.buf.Len()
	if room <= 0 {
		return len(p), nil
	}
	if len(p) > room {
		l.buf.Write(p[:room])
		return len(p), nil
	}
	l.buf.Write(p)
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.buf.String() }

func (bashTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a bashArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("bash: invalid arguments: " + err.Error()), nil
	}
	if strings.TrimSpace(a.Command) == "" {
		return Error("bash: command is required"), nil
	}
	timeout := a.TimeoutSeconds
	if timeout <= 0 {
		timeout = int(env.BashTimeout / time.Second)
	}
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > maxBashTimeout {
		timeout = maxBashTimeout
	}
	cwd := env.CWD
	if a.Cwd != "" {
		cwd = a.Cwd
		if !strings.HasPrefix(cwd, "/") {
			cwd = env.CWD + "/" + cwd
		}
	}

	useRTK := !a.Raw && env.Config != nil && env.Config.RTK.Enabled
	runCmd := a.Command
	if useRTK {
		if rewritten, ok := rtk.Rewrite(a.Command); ok {
			runCmd = rewritten
		}
	}

	var stdout, stderr limitedBuffer
	stdout.max = maxBashCapture
	stderr.max = maxBashCapture

	proc := exec.Command("/bin/bash", "-lc", runCmd)
	proc.Dir = cwd
	proc.Stdout = &stdout
	proc.Stderr = &stderr
	proc.Stdin = nil
	proc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	proc.Env = os.Environ()

	if err := proc.Start(); err != nil {
		return Error(fmt.Sprintf("bash: %v", err)), nil
	}
	pgid := proc.Process.Pid

	var once sync.Once
	kill := func() {
		once.Do(func() {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		})
	}
	defer kill()

	waitc := make(chan error, 1)
	go func() { waitc <- proc.Wait() }()

	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()

	exitCode := 0
	timedOut := false
	select {
	case <-waitc:
		exitCode = proc.ProcessState.ExitCode()
	case <-timer.C:
		timedOut = true
		kill()
		<-waitc
		exitCode = proc.ProcessState.ExitCode()
	case <-ctx.Done():
		kill()
		<-waitc
		return Result{Text: "bash: canceled", IsError: true}, nil
	}

	stdoutText := stdout.String()
	body := stdoutText
	if useRTK {
		filtered, rid, saved := rtk.Apply(runCmd, stdoutText, exitCode)
		body = filtered
		if rid != "" && saved > 0 && env.Store != nil {
			_ = env.Store.RecordRTK(env.SessionID, runCmd, rid, len(stdoutText), len(filtered))
		}
	} else {
		body = rtk.StripANSI(stdoutText)
	}
	if st := strings.TrimRight(rtk.StripANSI(stderr.String()), "\n"); st != "" {
		body += "\n[stderr]\n" + tailText(st, stderrTail)
	}
	if timedOut {
		body += fmt.Sprintf("\n[timed out after %ds]", timeout)
	}
	body += fmt.Sprintf("\n[exit %d]", exitCode)

	res := Result{Text: body, Display: summarizeCommand(a.Command)}
	if exitCode != 0 || timedOut {
		res.IsError = true
	}
	return res, nil
}

func tailText(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func summarizeCommand(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if len(cmd) > 80 {
		return cmd[:80] + "…"
	}
	return cmd
}
