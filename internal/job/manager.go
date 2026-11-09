// Package job runs background agent jobs with priority, concurrency limits and
// crash recovery.
package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ImAlexBlock/pig/internal/agent"
	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/internalurl"
	"github.com/ImAlexBlock/pig/internal/store"
)

// Runner executes one job.
type Runner func(ctx context.Context, job *store.Job) error

// Manager owns the job queue and workers.
type Manager struct {
	cfg   *config.Config
	store *store.Store

	Run      Runner
	OnFinish func(job *store.Job)
	OnUpdate func(job *store.Job)

	mu      sync.Mutex
	running map[string]*runningJob
	wake    chan struct{}
	stop    chan struct{}
	wg      sync.WaitGroup
	closed  bool
}

// New returns a manager.
func New(cfg *config.Config, st *store.Store) *Manager {
	return &Manager{
		cfg:     cfg,
		store:   st,
		running: map[string]*runningJob{},
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
	}
}

// SetConfig swaps the config used for queue limits.
func (m *Manager) SetConfig(cfg *config.Config) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// Start recovers interrupted jobs and starts the worker pool.
func (m *Manager) Start(ctx context.Context) (int, error) {
	n, err := m.store.RequeueInterrupted()
	if err != nil {
		return 0, err
	}
	workers := m.cfg.Background.MaxRunningJobs
	if workers < 1 {
		workers = 1
	}
	for range workers {
		m.wg.Add(1)
		go m.worker(ctx)
	}
	return n, nil
}

// Stop cancels running jobs and waits for workers.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stop)
	for _, rj := range m.running {
		rj.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// Wake nudges the workers.
func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Capacity returns queued and running counts.
func (m *Manager) Capacity() (queued, running int, err error) {
	return m.store.QueueDepth()
}

// Enqueue stores a job and signals the workers.
func (m *Manager) Enqueue(j *store.Job) (string, error) {
	queued, _, err := m.store.QueueDepth()
	if err != nil {
		return "", err
	}
	maxQueued := m.cfg.Background.MaxQueuedJobs
	if maxQueued > 0 && queued >= maxQueued {
		return "", fmt.Errorf("jobs: queue full (%d)", queued)
	}
	if err := m.store.CreateJob(j); err != nil {
		return "", err
	}
	m.Wake()
	return j.ID, nil
}

func (m *Manager) worker(ctx context.Context) {
	defer m.wg.Done()
	for {
		select {
		case <-m.stop:
			return
		case <-ctx.Done():
			return
		case <-m.wake:
		}
		for {
			j, err := m.store.NextQueuedJob()
			if err != nil {
				break
			}
			if !m.begin(ctx, j) {
				continue
			}
			m.execute(j)
			if m.isClosed() {
				return
			}
		}
	}
}

type runningJob struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *Manager) begin(parent context.Context, j *store.Job) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	rctx, cancel := context.WithCancel(parent)
	m.running[j.ID] = &runningJob{ctx: rctx, cancel: cancel}
	m.mu.Unlock()
	if err := m.store.SetJobStatus(j.ID, store.JobRunning); err != nil {
		m.release(j.ID)
		return false
	}
	return true
}

func (m *Manager) release(id string) {
	m.mu.Lock()
	if rj, ok := m.running[id]; ok {
		rj.cancel()
	}
	delete(m.running, id)
	m.mu.Unlock()
}

func (m *Manager) execute(j *store.Job) {
	m.mu.Lock()
	rj := m.running[j.ID]
	m.mu.Unlock()
	ctx := context.Background()
	if rj != nil {
		ctx = rj.ctx
	}
	if m.OnUpdate != nil {
		m.OnUpdate(j)
	}
	var err error
	if m.Run != nil {
		err = m.Run(ctx, j)
	}
	status := store.JobDone
	result := ""
	errText := ""
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		status = store.JobCanceled
		errText = "canceled"
	case err != nil:
		status = store.JobFailed
		errText = err.Error()
	default:
		result = m.resultText(j)
	}
	_ = m.store.FinishJob(j.ID, status, result, errText)
	m.release(j.ID)
	if fresh, gerr := m.store.GetJob(j.ID); gerr == nil && m.OnFinish != nil {
		m.OnFinish(fresh)
	}
}

// resultText reads the final assistant message of the job session.
func (m *Manager) resultText(j *store.Job) string {
	entries, err := m.store.AllEntries(j.SessionID)
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

// CancelJob cancels a queued or running job.
func (m *Manager) CancelJob(id string) error {
	j, err := m.store.GetJob(id)
	if err != nil {
		return err
	}
	switch j.Status {
	case store.JobDone, store.JobFailed, store.JobCanceled, store.JobInterrupted:
		return fmt.Errorf("jobs: %s is already %s", id, j.Status)
	}
	m.mu.Lock()
	rj, ok := m.running[id]
	m.mu.Unlock()
	if ok && rj != nil {
		rj.cancel()
	}
	if j.Status == store.JobQueued {
		if err := m.store.FinishJob(id, store.JobCanceled, "", "canceled before start"); err != nil {
			return err
		}
		if fresh, gerr := m.store.GetJob(id); gerr == nil && m.OnFinish != nil {
			m.OnFinish(fresh)
		}
	}
	return nil
}

// WaitJob blocks until the job reaches a terminal state.
func (m *Manager) WaitJob(ctx context.Context, id string, timeout time.Duration) (*store.Job, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		j, err := m.store.GetJob(id)
		if err != nil {
			return nil, err
		}
		switch j.Status {
		case store.JobDone, store.JobFailed, store.JobCanceled, store.JobInterrupted:
			return j, nil
		}
		if time.Now().After(deadline) {
			return j, fmt.Errorf("jobs: %s still %s", id, j.Status)
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ListJobs lists jobs filtered by status.
func (m *Manager) ListJobs(status string, limit int) ([]*store.Job, error) {
	return m.store.ListJobs(status, limit)
}

// GetJob loads one job.
func (m *Manager) GetJob(id string) (*store.Job, error) {
	return m.store.GetJob(id)
}

// JobLogs renders the tail of a job session transcript.
func (m *Manager) JobLogs(id string, tail int) (string, error) {
	j, err := m.store.GetJob(id)
	if err != nil {
		return "", err
	}
	if tail <= 0 {
		tail = 50
	}
	entries, err := m.store.TailEntries(j.SessionID, tail)
	if err != nil {
		return "", err
	}
	var out string
	for _, e := range entries {
		out += internalurl.RenderEntry(e) + "\n"
	}
	if out == "" {
		out = "(no output)\n"
	}
	return out, nil
}

// ContinueOptions builds the agent options for a job.
func ContinueOptions(j *store.Job) agent.Options {
	return agent.Options{
		RunID:         store.NewRunID(),
		Model:         j.Model,
		OriginChannel: j.OriginChannel,
		OriginChat:    j.OriginChat,
	}
}
