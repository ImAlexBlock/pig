package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ImAlexBlock/pig/internal/agent"
	"github.com/ImAlexBlock/pig/internal/compact"
	"github.com/ImAlexBlock/pig/internal/store"
)

// continuationPrompt is appended to the forked child session.
const continuationPrompt = "Continue the task from the copied context. Complete it and report the result."

type runRecord struct {
	ID              string
	SessionID       string
	cancel          context.CancelFunc
	detachRequested bool
	detached        bool
	originChannel   string
	originChat      string
	client          string
	done            chan struct{}
}

type runSpec struct {
	SessionID     string
	Prompt        string
	Attachments   []string
	Model         string
	CWD           string
	Client        string
	Queue         bool
	OriginChannel string
	OriginChat    string
	// Adopt receives "" when a queued prompt is folded into the running turn,
	// otherwise the id of the run that carries it. Optional.
	Adopt chan string
}

// resolveSession returns an existing session or creates the default one.
func (s *Server) resolveSession(sessionID, cwd string) (*store.Session, error) {
	if sessionID != "" {
		return s.st.GetSession(sessionID)
	}
	sess, err := s.st.LatestSession()
	if err == nil {
		return sess, nil
	}
	if cwd == "" {
		cwd, _ = userCWD()
	}
	return s.createSession(cwd)
}

func (s *Server) createSession(cwd string) (*store.Session, error) {
	model := ""
	if chain := s.cfg.RoleChain("main"); len(chain) > 0 {
		model = chain[0]
	}
	if model == "" {
		return nil, fmt.Errorf("run: no model configured for roles.main")
	}
	return s.st.CreateSession(store.SessionOptions{CWD: cwd, Model: model})
}

// StartRun begins or queues a run and returns (runID, sessionID, queued).
func (s *Server) StartRun(spec runSpec) (string, string, bool, error) {
	sess, err := s.resolveSession(spec.SessionID, spec.CWD)
	if err != nil {
		return "", "", false, err
	}
	spec.SessionID = sess.ID

	s.mu.Lock()
	if active := s.bySession[sess.ID]; active != nil {
		if !spec.Queue {
			s.mu.Unlock()
			return "", sess.ID, false, fmt.Errorf("run: session busy")
		}
		s.queue[sess.ID] = append(s.queue[sess.ID], queuedInput{Text: spec.Prompt, Model: spec.Model, Adopt: spec.Adopt})
		id := active.ID
		s.mu.Unlock()
		return id, sess.ID, true, nil
	}
	rec := &runRecord{
		ID:            store.NewRunID(),
		SessionID:     sess.ID,
		originChannel: spec.OriginChannel,
		originChat:    spec.OriginChat,
		client:        spec.Client,
		done:          make(chan struct{}),
	}
	s.runs[rec.ID] = rec
	s.bySession[sess.ID] = rec
	s.lastActivity = time.Now()
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runLoop(rec, spec)
	}()
	return rec.ID, sess.ID, false, nil
}

func (s *Server) runLoop(rec *runRecord, spec runSpec) {
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	rec.cancel = cancel
	s.mu.Unlock()
	defer cancel()
	defer func() {
		s.mu.Lock()
		delete(s.runs, rec.ID)
		if s.bySession[rec.SessionID] == rec {
			delete(s.bySession, rec.SessionID)
		}
		var next *queuedInput
		if q := s.queue[rec.SessionID]; len(q) > 0 {
			n := q[0]
			next = &n
			s.queue[rec.SessionID] = q[1:]
		}
		s.lastActivity = time.Now()
		s.mu.Unlock()
		close(rec.done)
		if next != nil {
			id, _, _, err := s.StartRun(runSpec{
				SessionID:     rec.SessionID,
				Prompt:        next.Text,
				Model:         next.Model,
				Client:        rec.client,
				Queue:         true,
				OriginChannel: rec.originChannel,
				OriginChat:    rec.originChat,
			})
			if err != nil {
				s.logf("queued run: %v", err)
				notifyAdopt(next, "")
				return
			}
			notifyAdopt(next, id)
		}
	}()

	opt := agent.Options{
		RunID:         rec.ID,
		Prompt:        spec.Prompt,
		Attachments:   spec.Attachments,
		Model:         spec.Model,
		OriginChannel: rec.originChannel,
		OriginChat:    rec.originChat,
	}
	if err := s.ag.Run(ctx, rec.SessionID, opt); err != nil {
		s.fan.emit(rec.ID, rec.SessionID, agent.Event{
			Type: agent.EvError, RunID: rec.ID, SessionID: rec.SessionID, Error: err.Error(),
		})
		s.logf("run %s: %v", rec.ID, err)
	}
}

// StartBackground enqueues a job that answers the prompt.
func (s *Server) StartBackground(spec runSpec) (string, error) {
	sess, err := s.resolveSession(spec.SessionID, spec.CWD)
	if err != nil {
		return "", err
	}
	if !s.cfg.Background.Enabled {
		return "", fmt.Errorf("background: disabled")
	}
	if _, err := s.st.AppendEntry(sess.ID, store.KindMessage, "user",
		store.NewMessageContent(store.MessageContent{Text: spec.Prompt}),
		compact.EstimateTokens(spec.Prompt)); err != nil {
		return "", err
	}
	j := &store.Job{
		Kind:          "background",
		SessionID:     sess.ID,
		Prompt:        spec.Prompt,
		Model:         spec.Model,
		Priority:      2,
		OriginChannel: spec.OriginChannel,
		OriginChat:    spec.OriginChat,
		DeliverResult: true,
	}
	return s.jobs.Enqueue(j)
}

// pressure reports whether the run metrics exceed the auto-fork thresholds.
func (s *Server) pressure(info agent.BoundaryInfo) bool {
	if s.cfg.Background.PressureSecs > 0 && info.Elapsed.Seconds() >= float64(s.cfg.Background.PressureSecs) {
		return true
	}
	if s.cfg.Background.PressureTools > 0 && info.ToolRounds >= s.cfg.Background.PressureTools {
		return true
	}
	return false
}

// boundary is the safe-boundary hook shared by every run.
func (s *Server) boundary(_ context.Context, info agent.BoundaryInfo) (string, string) {
	s.mu.Lock()
	rec := s.runs[info.RunID]
	queueLen := len(s.queue[info.SessionID])
	detach := rec != nil && rec.detachRequested
	s.mu.Unlock()

	shouldFork := detach || (s.cfg.Background.AutoFork && queueLen > 0 && s.pressure(info))
	if rec != nil && shouldFork {
		ok, note := s.autoFork(rec, info)
		if ok {
			return "", "detached"
		}
		s.fan.emit(info.RunID, info.SessionID, agent.Event{
			Type: agent.EvNotice, RunID: info.RunID, SessionID: info.SessionID, Level: "warn", Notice: note,
		})
	}

	s.mu.Lock()
	var steering string
	var folded *queuedInput
	if q := s.queue[info.SessionID]; len(q) > 0 {
		steering = q[0].Text
		folded = &q[0]
		s.queue[info.SessionID] = q[1:]
	}
	s.mu.Unlock()
	if folded != nil {
		// Empty id: the running turn already carries this input, so its own
		// channel session answers and the waiter can stop.
		notifyAdopt(folded, "")
	}
	return steering, ""
}

// autoFork copies the session into a child and enqueues a continuation job.
func (s *Server) autoFork(rec *runRecord, info agent.BoundaryInfo) (bool, string) {
	if !s.cfg.Background.Enabled {
		return false, "background: disabled"
	}
	queued, running, err := s.jobs.Capacity()
	if err != nil {
		return false, "fork failed: " + err.Error()
	}
	if queued+running >= s.cfg.Background.MaxRunningJobs+s.cfg.Background.MaxQueuedJobs {
		return false, "background capacity full"
	}
	seq, err := s.st.MaxSeq(rec.SessionID)
	if err != nil {
		return false, "fork failed: " + err.Error()
	}
	child, err := s.st.ForkSession(rec.SessionID, seq)
	if err != nil {
		return false, "fork failed: " + err.Error()
	}
	if _, err := s.st.AppendEntry(child.ID, store.KindMessage, "user",
		store.NewMessageContent(store.MessageContent{Text: continuationPrompt}),
		compact.EstimateTokens(continuationPrompt)); err != nil {
		return false, "fork failed: " + err.Error()
	}
	j := &store.Job{
		Kind:            "continuation",
		SessionID:       child.ID,
		ParentSessionID: rec.SessionID,
		Prompt:          continuationPrompt,
		Priority:        1,
		OriginChannel:   rec.originChannel,
		OriginChat:      rec.originChat,
		DeliverResult:   true,
	}
	id, err := s.jobs.Enqueue(j)
	if err != nil {
		return false, "fork failed: " + err.Error()
	}
	notice := fmt.Sprintf("[background] job %s continues this task", id)
	_, _ = s.st.AppendEntry(rec.SessionID, store.KindNotice, "",
		store.NewNoticeContent(store.NoticeContent{Level: "info", Text: notice}), 0)
	rec.detached = true
	s.fan.emit(rec.ID, rec.SessionID, agent.Event{
		Type: agent.EvNotice, RunID: rec.ID, SessionID: rec.SessionID, Level: "info", Notice: notice,
	})
	if fresh, gerr := s.st.GetJob(id); gerr == nil {
		s.fan.emit("", rec.SessionID, agent.Event{Type: agent.EvJobUpdate, Job: fresh})
	}
	return true, ""
}

// Input injects steering text into a run, or starts a new run.
func (s *Server) Input(runID, text string) error {
	s.mu.Lock()
	rec := s.runs[runID]
	s.mu.Unlock()
	if rec == nil {
		return fmt.Errorf("run: unknown run %s", runID)
	}
	s.mu.Lock()
	s.queue[rec.SessionID] = append(s.queue[rec.SessionID], queuedInput{Text: text})
	s.mu.Unlock()
	return nil
}

// CancelRun aborts a run and clears its queued input.
func (s *Server) CancelRun(runID string) error {
	s.mu.Lock()
	rec := s.runs[runID]
	s.mu.Unlock()
	if rec == nil {
		return fmt.Errorf("run: unknown run %s", runID)
	}
	s.mu.Lock()
	delete(s.queue, rec.SessionID)
	cancel := rec.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Detach requests a background fork at the next safe boundary.
func (s *Server) Detach(runID string) error {
	s.mu.Lock()
	rec := s.runs[runID]
	s.mu.Unlock()
	if rec == nil {
		return fmt.Errorf("run: unknown run %s", runID)
	}
	s.mu.Lock()
	rec.detachRequested = true
	s.mu.Unlock()
	return nil
}

// ActiveRun returns the run id and session for a session.
// notifyAdopt tells a waiting client what happened to its queued prompt.
func notifyAdopt(item *queuedInput, runID string) {
	if item == nil || item.Adopt == nil {
		return
	}
	select {
	case item.Adopt <- runID:
	default:
	}
}

// QueueLen reports how many inputs wait behind the active run for a session.
func (s *Server) QueueLen(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue[sessionID])
}

// ClearQueue drops inputs waiting behind the active run for a session and
// reports how many were dropped.
func (s *Server) ClearQueue(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.queue[sessionID])
	delete(s.queue, sessionID)
	return n
}

func (s *Server) ActiveRun(sessionID string) (runID, sessID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.bySession[sessionID]; rec != nil {
		return rec.ID, rec.SessionID, true
	}
	return "", "", false
}

// RunCounts reports active runs.
func (s *Server) RunCounts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

// WaitRun blocks until a run finishes.
func (s *Server) WaitRun(ctx context.Context, runID string, timeout time.Duration) error {
	s.mu.Lock()
	rec := s.runs[runID]
	s.mu.Unlock()
	if rec == nil {
		return nil
	}
	select {
	case <-rec.done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("run: %s still active", runID)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// onJobUpdate forwards job state changes.
func (s *Server) onJobUpdate(j *store.Job) {
	s.fan.emit("", j.SessionID, agent.Event{Type: agent.EvJobUpdate, Job: j})
}

// onJobFinish writes the result into the parent session and notifies channels.
func (s *Server) onJobFinish(j *store.Job) {
	s.fan.emit("", j.SessionID, agent.Event{Type: agent.EvJobUpdate, Job: j})
	if j.ParentSessionID != "" && j.DeliverResult {
		text := j.Result
		if text == "" {
			text = j.Error
		}
		limit := s.cfg.Subagents.ResultMaxChar
		if limit > 0 && len(text) > limit {
			if art, err := s.st.PutArtifact(j.SessionID, "text/plain", []byte(text)); err == nil {
				text = text[:limit] + "\n[truncated · artifact://" + art.ID + "]"
			} else {
				text = text[:limit]
			}
		}
		content := store.NewJobResultContent(store.JobResultContent{JobID: j.ID, Status: j.Status, Text: text})
		if entry, err := s.st.AppendEntry(j.ParentSessionID, store.KindJobResult, "", content, 0); err == nil {
			s.fan.emit("", j.ParentSessionID, agent.Event{
				Type: agent.EvJobUpdate, Job: j, SessionID: j.ParentSessionID, Text: text,
			})
			_ = entry
		}
	}
	if s.cfg.Background.Notify && j.OriginChannel != "" && j.OriginChat != "" {
		s.notifyChannel(j)
	}
}

func userCWD() (string, error) { return os.Getwd() }
