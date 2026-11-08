package store

import (
	"database/sql"
	"errors"
	"fmt"
)

const jobCols = `id,kind,session_id,parent_session_id,prompt,origin_channel,origin_chat,model,tools,
	priority,status,result,error,created_at,started_at,ended_at,notified,deliver_result`

func scanJob(sc interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var notified, deliver int
	if err := sc.Scan(&j.ID, &j.Kind, &j.SessionID, &j.ParentSessionID, &j.Prompt, &j.OriginChannel,
		&j.OriginChat, &j.Model, &j.Tools, &j.Priority, &j.Status, &j.Result, &j.Error,
		&j.CreatedAt, &j.StartedAt, &j.EndedAt, &notified, &deliver); err != nil {
		return nil, err
	}
	j.Notified = notified != 0
	j.DeliverResult = deliver != 0
	return &j, nil
}

// CreateJob inserts a job. Status defaults to queued.
func (s *Store) CreateJob(j *Job) error {
	if j.ID == "" {
		j.ID = NewJobID()
	}
	if j.Status == "" {
		j.Status = JobQueued
	}
	if j.CreatedAt == 0 {
		j.CreatedAt = now()
	}
	if j.Tools == "" {
		j.Tools = "[]"
	}
	_, err := s.db.Exec(`INSERT INTO jobs(id,kind,session_id,parent_session_id,prompt,origin_channel,origin_chat,
		model,tools,priority,status,result,error,created_at,started_at,ended_at,notified,deliver_result)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Kind, j.SessionID, j.ParentSessionID, j.Prompt, j.OriginChannel, j.OriginChat,
		j.Model, j.Tools, j.Priority, j.Status, j.Result, j.Error, j.CreatedAt, j.StartedAt,
		j.EndedAt, boolInt(j.Notified), boolInt(j.DeliverResult))
	if err != nil {
		return fmt.Errorf("store: job: %v", err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetJobStatus updates status and timing.
func (s *Store) SetJobStatus(id, status string) error {
	ts := now()
	var err error
	switch status {
	case JobRunning:
		_, err = s.db.Exec(`UPDATE jobs SET status=?,started_at=? WHERE id=?`, status, ts, id)
	case JobDone, JobFailed, JobCanceled, JobInterrupted:
		_, err = s.db.Exec(`UPDATE jobs SET status=?,ended_at=? WHERE id=?`, status, ts, id)
	default:
		_, err = s.db.Exec(`UPDATE jobs SET status=? WHERE id=?`, status, id)
	}
	if err != nil {
		return fmt.Errorf("store: job: %v", err)
	}
	return nil
}

// FinishJob records a terminal state plus result/error text.
func (s *Store) FinishJob(id, status, result, errText string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=?,result=?,error=?,ended_at=? WHERE id=?`,
		status, result, errText, now(), id)
	if err != nil {
		return fmt.Errorf("store: job: %v", err)
	}
	return nil
}

// MarkJobNotified flips the notified flag.
func (s *Store) MarkJobNotified(id string) error {
	_, err := s.db.Exec(`UPDATE jobs SET notified=1 WHERE id=?`, id)
	return err
}

// GetJob loads one job.
func (s *Store) GetJob(id string) (*Job, error) {
	row := s.db.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE id=?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: job %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: job: %v", err)
	}
	return j, nil
}

// ListJobs returns jobs filtered by status (empty = all), newest first.
func (s *Store) ListJobs(status string, limit int) ([]*Job, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.Query(`SELECT `+jobCols+` FROM jobs ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(`SELECT `+jobCols+` FROM jobs WHERE status=? ORDER BY created_at DESC, rowid DESC LIMIT ?`, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("store: jobs: %v", err)
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("store: jobs: %v", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// NextQueuedJob returns the highest-priority queued job.
func (s *Store) NextQueuedJob() (*Job, error) {
	row := s.db.QueryRow(`SELECT ` + jobCols + ` FROM jobs WHERE status='queued'
		ORDER BY priority ASC, created_at ASC, rowid ASC LIMIT 1`)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: job: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: job: %v", err)
	}
	return j, nil
}

// QueueDepth counts queued and running jobs.
func (s *Store) QueueDepth() (queued, running int, err error) {
	err = s.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status='queued' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),0) FROM jobs`).Scan(&queued, &running)
	if err != nil {
		return 0, 0, fmt.Errorf("store: jobs: %v", err)
	}
	return queued, running, nil
}

// RequeueInterrupted marks jobs left running by a crash as interrupted.
func (s *Store) RequeueInterrupted() (int, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status='interrupted',ended_at=? WHERE status='running'`, now())
	if err != nil {
		return 0, fmt.Errorf("store: job: %v", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RetryJob puts a terminal job back in the queue.
func (s *Store) RetryJob(id string) error {
	j, err := s.GetJob(id)
	if err != nil {
		return err
	}
	switch j.Status {
	case JobInterrupted, JobFailed, JobCanceled:
	default:
		return fmt.Errorf("store: job %s: cannot retry status %s", id, j.Status)
	}
	_, err = s.db.Exec(`UPDATE jobs SET status='queued',result='',error='',started_at=0,ended_at=0,notified=0 WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("store: job: %v", err)
	}
	return nil
}

// SetJobPriority changes queue priority.
func (s *Store) SetJobPriority(id string, p int) error {
	_, err := s.db.Exec(`UPDATE jobs SET priority=? WHERE id=?`, p, id)
	return err
}

// ChannelState is the pairing/session binding for one chat.
type ChannelState struct {
	Channel   string `json:"channel"`
	ChatID    string `json:"chat_id"`
	SessionID string `json:"session_id"`
	Paired    bool   `json:"paired"`
	// Lang is the reply language for this chat; empty follows the config.
	Lang      string `json:"lang"`
	UpdatedAt int64  `json:"updated_at"`
}

// GetChannelState loads the binding for (channel, chat).
func (s *Store) GetChannelState(channel, chatID string) (*ChannelState, error) {
	row := s.db.QueryRow(`SELECT channel,chat_id,session_id,paired,lang,updated_at FROM channel_state
		WHERE channel=? AND chat_id=?`, channel, chatID)
	var cs ChannelState
	var paired int
	err := row.Scan(&cs.Channel, &cs.ChatID, &cs.SessionID, &paired, &cs.Lang, &cs.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: channel_state: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: channel_state: %v", err)
	}
	cs.Paired = paired != 0
	return &cs, nil
}

// SetChannelState upserts a chat binding.
func (s *Store) SetChannelState(cs *ChannelState) error {
	_, err := s.db.Exec(`INSERT INTO channel_state(channel,chat_id,session_id,paired,lang,updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(channel,chat_id) DO UPDATE SET session_id=excluded.session_id,
		paired=excluded.paired,lang=excluded.lang,updated_at=excluded.updated_at`,
		cs.Channel, cs.ChatID, cs.SessionID, boolInt(cs.Paired), cs.Lang, now())
	if err != nil {
		return fmt.Errorf("store: channel_state: %v", err)
	}
	return nil
}
