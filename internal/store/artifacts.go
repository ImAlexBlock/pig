package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PutArtifact writes bytes under $PIG_HOME/artifacts/<session>/<id> and records it.
func (s *Store) PutArtifact(sessionID, mime string, data []byte) (*Artifact, error) {
	if sessionID == "" {
		sessionID = "global"
	}
	id := NewArtifactID()
	dir := filepath.Join(s.dir, "artifacts", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: artifact: %v", err)
	}
	path := filepath.Join(dir, id)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("store: artifact: %v", err)
	}
	a := &Artifact{
		ID:        id,
		SessionID: sessionID,
		Path:      path,
		Mime:      mime,
		Bytes:     int64(len(data)),
		CreatedAt: now(),
	}
	if _, err := s.db.Exec(`INSERT INTO artifacts(id,session_id,path,mime,bytes,created_at) VALUES (?,?,?,?,?,?)`,
		a.ID, a.SessionID, a.Path, a.Mime, a.Bytes, a.CreatedAt); err != nil {
		return nil, fmt.Errorf("store: artifact: %v", err)
	}
	return a, nil
}

// GetArtifact returns artifact metadata.
func (s *Store) GetArtifact(id string) (*Artifact, error) {
	row := s.db.QueryRow(`SELECT id,session_id,path,mime,bytes,created_at FROM artifacts WHERE id=?`, id)
	var a Artifact
	err := row.Scan(&a.ID, &a.SessionID, &a.Path, &a.Mime, &a.Bytes, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: artifact %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: artifact: %v", err)
	}
	return &a, nil
}

// ReadArtifact returns artifact metadata and bytes.
func (s *Store) ReadArtifact(id string) (*Artifact, []byte, error) {
	a, err := s.GetArtifact(id)
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(a.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("store: artifact: %v", err)
	}
	return a, b, nil
}

// RecordUsage stores one provider usage record and returns its id.
func (s *Store) RecordUsage(u *Usage) error {
	if u.CreatedAt == 0 {
		u.CreatedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO usage(session_id,provider,model,input,output,cache_read,cache_write,cost_usd,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, u.SessionID, u.Provider, u.Model, u.Input, u.Output,
		u.CacheRead, u.CacheWrite, u.CostUSD, u.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: usage: %v", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		u.ID = id
	}
	return nil
}

// UsageTotals aggregates usage for a session.
type UsageTotals struct {
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	CostUSD    float64 `json:"cost_usd"`
	Requests   int     `json:"requests"`
}

// SessionUsage sums usage for a session.
func (s *Store) SessionUsage(sessionID string) (UsageTotals, error) {
	var t UsageTotals
	err := s.db.QueryRow(`SELECT COALESCE(SUM(input),0),COALESCE(SUM(output),0),COALESCE(SUM(cache_read),0),
		COALESCE(SUM(cache_write),0),COALESCE(SUM(cost_usd),0),COUNT(*) FROM usage WHERE session_id=?`,
		sessionID).Scan(&t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &t.CostUSD, &t.Requests)
	if err != nil {
		return t, fmt.Errorf("store: usage: %v", err)
	}
	return t, nil
}

// LastUsage returns the newest usage record of a session.
func (s *Store) LastUsage(sessionID string) (*Usage, error) {
	row := s.db.QueryRow(`SELECT id,session_id,provider,model,input,output,cache_read,cache_write,cost_usd,created_at
		FROM usage WHERE session_id=? ORDER BY id DESC LIMIT 1`, sessionID)
	var u Usage
	err := row.Scan(&u.ID, &u.SessionID, &u.Provider, &u.Model, &u.Input, &u.Output,
		&u.CacheRead, &u.CacheWrite, &u.CostUSD, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: usage: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: usage: %v", err)
	}
	return &u, nil
}

// RecordRTK stores one RTK savings row.
func (s *Store) RecordRTK(sessionID, command, rule string, rawBytes, filteredBytes int) error {
	_, err := s.db.Exec(`INSERT INTO rtk_savings(session_id,command,rule,raw_bytes,filtered_bytes,created_at)
		VALUES (?,?,?,?,?,?)`, sessionID, command, rule, rawBytes, filteredBytes, now())
	if err != nil {
		return fmt.Errorf("store: rtk: %v", err)
	}
	return nil
}

// RTKGain aggregates RTK savings.
type RTKGain struct {
	Calls         int   `json:"calls"`
	RawBytes      int64 `json:"raw_bytes"`
	FilteredBytes int64 `json:"filtered_bytes"`
	SavedBytes    int64 `json:"saved_bytes"`
}

// RTKGain returns aggregate savings.
func (s *Store) RTKGain() (RTKGain, error) {
	var g RTKGain
	err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(raw_bytes),0),COALESCE(SUM(filtered_bytes),0)
		FROM rtk_savings`).Scan(&g.Calls, &g.RawBytes, &g.FilteredBytes)
	if err != nil {
		return g, fmt.Errorf("store: rtk: %v", err)
	}
	g.SavedBytes = g.RawBytes - g.FilteredBytes
	return g, nil
}
