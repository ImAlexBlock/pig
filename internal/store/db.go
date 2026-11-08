package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// SchemaVersion is the current store schema version. Version 1 databases are
// upgraded in place on open.
const SchemaVersion = 2

// ErrNotFound reports a missing row.
var ErrNotFound = errors.New("not found")

// Store is the SQLite-backed persistence layer.
type Store struct {
	db  *sql.DB
	dir string // $PIG_HOME
}

// Session is one conversation.
type Session struct {
	ID            string `json:"id"`
	ParentID      string `json:"parent_id"`
	ForkedFromSeq int    `json:"forked_from_seq"`
	Depth         int    `json:"depth"`
	Title         string `json:"title"`
	CWD           string `json:"cwd"`
	Model         string `json:"model"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	Archived      bool   `json:"archived"`
}

// Entry kinds.
const (
	KindMessage    = "message"
	KindToolCall   = "tool_call"
	KindToolResult = "tool_result"
	KindNotice     = "notice"
	KindTodo       = "todo"
	KindJobResult  = "job_result"
	KindCompaction = "compaction"
)

// Entry is one persisted session event.
type Entry struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
	Kind      string `json:"kind"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Tokens    int    `json:"tokens"`
	CreatedAt int64  `json:"created_at"`
}

// Compaction is one compaction record.
type Compaction struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	EntryID      string `json:"entry_id"`
	Method       string `json:"method"`
	Summary      string `json:"summary"`
	FirstKeptSeq int    `json:"first_kept_seq"`
	SourceText   string `json:"source_text"`
	Frames       string `json:"frames"`
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
	CreatedAt    int64  `json:"created_at"`
}

// Artifact is a stored blob.
type Artifact struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Mime      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
}

// Usage is one provider usage record.
type Usage struct {
	ID         int64   `json:"id"`
	SessionID  string  `json:"session_id"`
	Provider   string  `json:"provider"`
	Model      string  `json:"model"`
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	CostUSD    float64 `json:"cost_usd"`
	CreatedAt  int64   `json:"created_at"`
}

// Job statuses.
const (
	JobQueued      = "queued"
	JobRunning     = "running"
	JobDone        = "done"
	JobFailed      = "failed"
	JobCanceled    = "canceled"
	JobInterrupted = "interrupted"
)

// Job is one queued unit of background work.
type Job struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id"`
	ParentSessionID string `json:"parent_session_id"`
	Prompt          string `json:"prompt"`
	OriginChannel   string `json:"origin_channel"`
	OriginChat      string `json:"origin_chat"`
	Model           string `json:"model"`
	Tools           string `json:"tools"`
	Priority        int    `json:"priority"`
	Status          string `json:"status"`
	Result          string `json:"result"`
	Error           string `json:"error"`
	CreatedAt       int64  `json:"created_at"`
	StartedAt       int64  `json:"started_at"`
	EndedAt         int64  `json:"ended_at"`
	Notified        bool   `json:"notified"`
	DeliverResult   bool   `json:"deliver_result"`
}

// Open opens (and migrates) the database at path. dir is $PIG_HOME, used to
// resolve artifact files.
func Open(path, dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: open: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open: %v", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dir: dir}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: chmod: %v", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("store: migration failed at version %d: %v", SchemaVersion, err)
	}
	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_meta LIMIT 1`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(`INSERT INTO schema_meta(version) VALUES (?)`, SchemaVersion); err != nil {
			return fmt.Errorf("store: migration failed at version %d: %v", SchemaVersion, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("store: migration failed at version %d: %v", SchemaVersion, err)
	case version == 1:
		// v2 adds channel_state.lang: chat reply language.
		if _, err := s.db.Exec(`ALTER TABLE channel_state ADD COLUMN lang TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("store: migration 1→2: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE schema_meta SET version=?`, SchemaVersion); err != nil {
			return fmt.Errorf("store: migration 1→2: %v", err)
		}
		return nil
	case version != SchemaVersion:
		return fmt.Errorf("store: migration failed at version %d: unsupported schema version %d", SchemaVersion, version)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for ad-hoc queries (read paths in the CLI).
func (s *Store) DB() *sql.DB { return s.db }

// Dir returns $PIG_HOME.
func (s *Store) Dir() string { return s.dir }
