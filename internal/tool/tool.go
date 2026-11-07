// Package tool defines the tool interface, the shared environment and the
// tool registry.
package tool

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/internalurl"
	"github.com/alexblock/pig/internal/store"
)

// Call is one tool invocation requested by the model.
type Call struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Image is image output attached to a tool result.
type Image struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"-"`
}

// SentFile is a file the tool wants delivered to the user's chat.
type SentFile struct {
	Name string
	Mime string
	Data []byte
}

// Result is the outcome of a tool call.
type Result struct {
	Text     string     `json:"text"`
	Images   []Image    `json:"images,omitempty"`
	Files    []SentFile `json:"files,omitempty"`
	Artifact string     `json:"artifact,omitempty"`
	IsError  bool       `json:"is_error,omitempty"`
	Useless  bool       `json:"useless,omitempty"`
	Display  string     `json:"display,omitempty"`
}

// Error builds an error result.
func Error(text string) Result { return Result{Text: text, IsError: true} }

// SubagentRequest asks the host to run a child agent.
type SubagentRequest struct {
	ParentRunID         string
	Prompt              string
	Name                string
	Model               string
	Tools               []string
	Background          bool
	Context             string
	CallerSessionID     string
	CallerCWD           string
	CallerDepth         int
	OriginChannel       string
	OriginChat          string
	ModelSupportsImages bool
}

// SubagentResult is the outcome of a subagent request.
type SubagentResult struct {
	Text      string
	JobID     string
	SessionID string
}

// JobManager is the background job surface used by the job tool.
type JobManager interface {
	ListJobs(status string, limit int) ([]*store.Job, error)
	GetJob(id string) (*store.Job, error)
	WaitJob(ctx context.Context, id string, timeout time.Duration) (*store.Job, error)
	CancelJob(id string) error
	JobLogs(id string, tail int) (string, error)
}

// ReadSet tracks files read in a session (read-before-overwrite guard).
type ReadSet struct {
	mu sync.Mutex
	m  map[string]bool
}

// NewReadSet returns an empty read set.
func NewReadSet() *ReadSet { return &ReadSet{m: map[string]bool{}} }

// Mark records a path as read.
func (r *ReadSet) Mark(path string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.m[path] = true
	r.mu.Unlock()
}

// Has reports whether a path was read.
func (r *ReadSet) Has(path string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[path]
}

// Paths lists the recorded paths.
func (r *ReadSet) Paths() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.m))
	for p := range r.m {
		out = append(out, p)
	}
	return out
}

// Env is the execution environment handed to every tool call.
type Env struct {
	SessionID           string
	RunID               string
	CWD                 string
	Depth               int
	Store               *store.Store
	Config              *config.Config
	Resolver            *internalurl.Resolver
	ModelSupportsImages bool
	ReadFiles           *ReadSet
	Secrets             []string
	OriginChannel       string
	OriginChat          string
	MaxOutputBytes      int
	BashTimeout         time.Duration

	Notify      func(level, text string)
	EmitTodo    func(phases []store.TodoPhase) error
	RunSubagent func(ctx context.Context, req SubagentRequest) (SubagentResult, error)
	Jobs        JobManager
}

// Tool is one model-callable capability.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	ReadOnly() bool
	Run(ctx context.Context, c Call, env *Env) (Result, error)
}

// Registry holds the registered tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names returns registered tool names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Describe returns the one-line descriptions of the registered tools.
func (r *Registry) Describe() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.tools))
	for n, t := range r.tools {
		out[n] = t.Description()
	}
	return out
}

// Spec is a wire-neutral tool definition.
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Specs returns definitions for the named tools (nil order = all).
func (r *Registry) Specs(names []string) []Spec {
	if names == nil {
		names = r.Names()
	}
	out := make([]Spec, 0, len(names))
	for _, n := range names {
		if t, ok := r.Get(n); ok {
			out = append(out, Spec{Name: t.Name(), Description: t.Description(), Schema: t.Schema()})
		}
	}
	return out
}

// Builtin returns a registry populated with the core tools. Browser, search
// and skill tools are registered by the daemon.
func Builtin() *Registry {
	r := NewRegistry()
	r.Register(readTool{})
	r.Register(writeTool{})
	r.Register(editTool{})
	r.Register(bashTool{})
	r.Register(todoTool{})
	r.Register(taskTool{})
	r.Register(jobTool{})
	r.Register(memoryTool{})
	r.Register(sendTool{})
	return r
}
