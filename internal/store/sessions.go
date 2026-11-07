package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func now() int64 { return time.Now().Unix() }

// SessionOptions describes a new session.
type SessionOptions struct {
	CWD      string
	Model    string
	ParentID string
	Depth    int
	Title    string
}

// CreateSession inserts a new session row.
func (s *Store) CreateSession(opt SessionOptions) (*Session, error) {
	if opt.CWD == "" {
		return nil, fmt.Errorf("store: session: cwd is required")
	}
	if opt.Model == "" {
		return nil, fmt.Errorf("store: session: model is required")
	}
	sess := &Session{
		ID:        NewSessionID(),
		ParentID:  opt.ParentID,
		Depth:     opt.Depth,
		Title:     opt.Title,
		CWD:       opt.CWD,
		Model:     opt.Model,
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	_, err := s.db.Exec(`INSERT INTO sessions(id,parent_id,forked_from_seq,depth,title,cwd,model,created_at,updated_at,archived)
		VALUES (?,?,?,?,?,?,?,?,?,0)`,
		sess.ID, sess.ParentID, 0, sess.Depth, sess.Title, sess.CWD, sess.Model, sess.CreatedAt, sess.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: session: %v", err)
	}
	return sess, nil
}

const sessionCols = `id,parent_id,forked_from_seq,depth,title,cwd,model,created_at,updated_at,archived`

func scanSession(sc interface{ Scan(...any) error }) (*Session, error) {
	var s Session
	var archived int
	var parent sql.NullString
	if err := sc.Scan(&s.ID, &parent, &s.ForkedFromSeq, &s.Depth, &s.Title, &s.CWD, &s.Model,
		&s.CreatedAt, &s.UpdatedAt, &archived); err != nil {
		return nil, err
	}
	s.ParentID = parent.String
	s.Archived = archived != 0
	return &s, nil
}

// GetSession loads one session.
func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE id=?`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: session %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: session: %v", err)
	}
	return sess, nil
}

// LatestSession returns the most recently updated non-archived session.
func (s *Store) LatestSession() (*Session, error) {
	row := s.db.QueryRow(`SELECT ` + sessionCols + ` FROM sessions WHERE archived=0 ORDER BY updated_at DESC, rowid DESC LIMIT 1`)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: session: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: session: %v", err)
	}
	return sess, nil
}

// ListSessions returns sessions newest first.
func (s *Store) ListSessions(limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions ORDER BY updated_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: sessions: %v", err)
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sessions: %v", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// TouchSession updates updated_at (and optionally the model).
func (s *Store) TouchSession(id string) error {
	_, err := s.db.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, now(), id)
	return err
}

// SetSessionModel changes the model bound to a session.
func (s *Store) SetSessionModel(id, model string) error {
	res, err := s.db.Exec(`UPDATE sessions SET model=?, updated_at=? WHERE id=?`, model, now(), id)
	if err != nil {
		return fmt.Errorf("store: session: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: session %s: %w", id, ErrNotFound)
	}
	return nil
}

// SetSessionTitle writes the session title.
func (s *Store) SetSessionTitle(id, title string) error {
	_, err := s.db.Exec(`UPDATE sessions SET title=?, updated_at=? WHERE id=?`, title, now(), id)
	return err
}

// DeleteSession removes a session and its entries.
func (s *Store) DeleteSession(id string) error {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("store: session: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: session %s: %w", id, ErrNotFound)
	}
	return nil
}

// AppendEntry appends an entry and returns it with the assigned seq.
func (s *Store) AppendEntry(sessionID, kind, role, content string, tokens int) (*Entry, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: entry: %v", err)
	}
	defer tx.Rollback()
	var seq int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM entries WHERE session_id=?`, sessionID).Scan(&seq); err != nil {
		return nil, fmt.Errorf("store: entry: %v", err)
	}
	e := &Entry{
		ID:        NewEntryID(),
		SessionID: sessionID,
		Seq:       seq,
		Kind:      kind,
		Role:      role,
		Content:   content,
		Tokens:    tokens,
		CreatedAt: now(),
	}
	if _, err := tx.Exec(`INSERT INTO entries(id,session_id,seq,kind,role,content,tokens,created_at) VALUES (?,?,?,?,?,?,?,?)`,
		e.ID, e.SessionID, e.Seq, e.Kind, e.Role, e.Content, e.Tokens, e.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: entry: %v", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, e.CreatedAt, sessionID); err != nil {
		return nil, fmt.Errorf("store: entry: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: entry: %v", err)
	}
	return e, nil
}

const entryCols = `id,session_id,seq,kind,role,content,tokens,created_at`

func scanEntry(sc interface{ Scan(...any) error }) (*Entry, error) {
	var e Entry
	if err := sc.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Kind, &e.Role, &e.Content, &e.Tokens, &e.CreatedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) scanEntries(rows *sql.Rows) ([]*Entry, error) {
	defer rows.Close()
	var out []*Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("store: entries: %v", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AllEntries returns every entry of a session in seq order.
func (s *Store) AllEntries(sessionID string) ([]*Entry, error) {
	rows, err := s.db.Query(`SELECT `+entryCols+` FROM entries WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: entries: %v", err)
	}
	return s.scanEntries(rows)
}

// EntriesAfter returns entries with seq > after, ascending.
func (s *Store) EntriesAfter(sessionID string, after int, limit int) ([]*Entry, error) {
	q := `SELECT ` + entryCols + ` FROM entries WHERE session_id=? AND seq>? ORDER BY seq`
	args := []any{sessionID, after}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: entries: %v", err)
	}
	return s.scanEntries(rows)
}

// EntriesRange returns entries with from<=seq<=to, ascending.
func (s *Store) EntriesRange(sessionID string, from, to int) ([]*Entry, error) {
	rows, err := s.db.Query(`SELECT `+entryCols+` FROM entries WHERE session_id=? AND seq>=? AND seq<=? ORDER BY seq`,
		sessionID, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: entries: %v", err)
	}
	return s.scanEntries(rows)
}

// TailEntries returns the last n entries in seq order.
func (s *Store) TailEntries(sessionID string, n int) ([]*Entry, error) {
	if n <= 0 {
		n = 200
	}
	rows, err := s.db.Query(`SELECT `+entryCols+` FROM (
		SELECT `+entryCols+` FROM entries WHERE session_id=? ORDER BY seq DESC LIMIT ?
	) ORDER BY seq`, sessionID, n)
	if err != nil {
		return nil, fmt.Errorf("store: entries: %v", err)
	}
	return s.scanEntries(rows)
}

// Entry returns one entry by id.
func (s *Store) Entry(id string) (*Entry, error) {
	row := s.db.QueryRow(`SELECT `+entryCols+` FROM entries WHERE id=?`, id)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: entry %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: entry: %v", err)
	}
	return e, nil
}

// EntryAt returns the entry with a given seq.
func (s *Store) EntryAt(sessionID string, seq int) (*Entry, error) {
	row := s.db.QueryRow(`SELECT `+entryCols+` FROM entries WHERE session_id=? AND seq=?`, sessionID, seq)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: entry %s#%d: %w", sessionID, seq, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: entry: %v", err)
	}
	return e, nil
}

// MaxSeq returns the highest seq in a session (0 when empty).
func (s *Store) MaxSeq(sessionID string) (int, error) {
	var seq int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM entries WHERE session_id=?`, sessionID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: entries: %v", err)
	}
	return seq, nil
}

// UpdateEntryContent rewrites an entry body (used by shake).
func (s *Store) UpdateEntryContent(id, content string, tokens int) error {
	_, err := s.db.Exec(`UPDATE entries SET content=?, tokens=? WHERE id=?`, content, tokens, id)
	if err != nil {
		return fmt.Errorf("store: entry: %v", err)
	}
	return nil
}

// AppendCompaction stores a compaction record.
func (s *Store) AppendCompaction(c *Compaction) error {
	if c.ID == "" {
		c.ID = NewCompactionID()
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	_, err := s.db.Exec(`INSERT INTO compactions(id,session_id,entry_id,method,summary,first_kept_seq,
		source_text,frames,tokens_before,tokens_after,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.SessionID, c.EntryID, c.Method, c.Summary, c.FirstKeptSeq, c.SourceText,
		c.Frames, c.TokensBefore, c.TokensAfter, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: compaction: %v", err)
	}
	return nil
}

// LatestCompaction returns the newest compaction that keeps context
// (first_kept_seq >= 0). Shake records first_kept_seq = -1 and are ignored.
func (s *Store) LatestCompaction(sessionID string) (*Compaction, error) {
	row := s.db.QueryRow(`SELECT id,session_id,entry_id,method,summary,first_kept_seq,source_text,
		frames,tokens_before,tokens_after,created_at FROM compactions
		WHERE session_id=? AND first_kept_seq>=0 ORDER BY created_at DESC, rowid DESC LIMIT 1`, sessionID)
	var c Compaction
	err := row.Scan(&c.ID, &c.SessionID, &c.EntryID, &c.Method, &c.Summary, &c.FirstKeptSeq,
		&c.SourceText, &c.Frames, &c.TokensBefore, &c.TokensAfter, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: compaction: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: compaction: %v", err)
	}
	return &c, nil
}

// CompactionByID loads one compaction.
func (s *Store) CompactionByID(id string) (*Compaction, error) {
	row := s.db.QueryRow(`SELECT id,session_id,entry_id,method,summary,first_kept_seq,source_text,
		frames,tokens_before,tokens_after,created_at FROM compactions WHERE id=?`, id)
	var c Compaction
	err := row.Scan(&c.ID, &c.SessionID, &c.EntryID, &c.Method, &c.Summary, &c.FirstKeptSeq,
		&c.SourceText, &c.Frames, &c.TokensBefore, &c.TokensAfter, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: compaction %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: compaction: %v", err)
	}
	return &c, nil
}

// ForkSession copies a session up to and including seq into a new child.
func (s *Store) ForkSession(parentID string, seq int) (*Session, error) {
	parent, err := s.GetSession(parentID)
	if err != nil {
		return nil, err
	}
	child := &Session{
		ID:            NewSessionID(),
		ParentID:      parent.ID,
		ForkedFromSeq: seq,
		Depth:         parent.Depth,
		Title:         parent.Title,
		CWD:           parent.CWD,
		Model:         parent.Model,
		CreatedAt:     now(),
		UpdatedAt:     now(),
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: fork: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO sessions(id,parent_id,forked_from_seq,depth,title,cwd,model,created_at,updated_at,archived)
		VALUES (?,?,?,?,?,?,?,?,?,0)`,
		child.ID, child.ParentID, child.ForkedFromSeq, child.Depth, child.Title, child.CWD, child.Model,
		child.CreatedAt, child.UpdatedAt); err != nil {
		return nil, fmt.Errorf("store: fork: %v", err)
	}
	rows, err := tx.Query(`SELECT seq,kind,role,content,tokens FROM entries WHERE session_id=? AND seq<=? ORDER BY seq`,
		parentID, seq)
	if err != nil {
		return nil, fmt.Errorf("store: fork: %v", err)
	}
	type copyRow struct {
		seq, tokens int
		kind, role  string
		content     string
	}
	var copies []copyRow
	for rows.Next() {
		var c copyRow
		if err := rows.Scan(&c.seq, &c.kind, &c.role, &c.content, &c.tokens); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: fork: %v", err)
		}
		copies = append(copies, c)
	}
	rows.Close()
	for _, c := range copies {
		if _, err := tx.Exec(`INSERT INTO entries(id,session_id,seq,kind,role,content,tokens,created_at) VALUES (?,?,?,?,?,?,?,?)`,
			NewEntryID(), child.ID, c.seq, c.kind, c.role, c.content, c.tokens, child.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: fork: %v", err)
		}
	}
	crows, err := tx.Query(`SELECT method,summary,first_kept_seq,source_text,frames,tokens_before,tokens_after
		FROM compactions WHERE session_id=? AND first_kept_seq>=0 AND first_kept_seq<=?`, parentID, seq)
	if err != nil {
		return nil, fmt.Errorf("store: fork: %v", err)
	}
	type cmpRow struct {
		method, summary, source, frames string
		firstKept, before, after        int
	}
	var cmps []cmpRow
	for crows.Next() {
		var c cmpRow
		if err := crows.Scan(&c.method, &c.summary, &c.firstKept, &c.source, &c.frames, &c.before, &c.after); err != nil {
			crows.Close()
			return nil, fmt.Errorf("store: fork: %v", err)
		}
		cmps = append(cmps, c)
	}
	crows.Close()
	for _, c := range cmps {
		if _, err := tx.Exec(`INSERT INTO compactions(id,session_id,entry_id,method,summary,first_kept_seq,
			source_text,frames,tokens_before,tokens_after,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			NewCompactionID(), child.ID, "", c.method, c.summary, c.firstKept, c.source, c.frames,
			c.before, c.after, child.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: fork: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: fork: %v", err)
	}
	return child, nil
}
