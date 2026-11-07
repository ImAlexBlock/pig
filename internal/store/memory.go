package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Fact is one durable note remembered across sessions.
type Fact struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// PutFact upserts a fact by key.
func (s *Store) PutFact(key, value string) (*Fact, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("store: memory: key is required")
	}
	ts := now()
	_, err := s.db.Exec(`INSERT INTO memory(key,value,created_at,updated_at) VALUES (?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, ts, ts)
	if err != nil {
		return nil, fmt.Errorf("store: memory: %v", err)
	}
	return s.GetFact(key)
}

// GetFact returns one fact by key.
func (s *Store) GetFact(key string) (*Fact, error) {
	row := s.db.QueryRow(`SELECT id,key,value,created_at,updated_at FROM memory WHERE key=?`, key)
	var f Fact
	err := row.Scan(&f.ID, &f.Key, &f.Value, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: memory %s: %w", key, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: memory: %v", err)
	}
	return &f, nil
}

// Facts returns the most recently updated facts.
func (s *Store) Facts(limit int) ([]*Fact, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id,key,value,created_at,updated_at FROM memory
		ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: memory: %v", err)
	}
	return scanFacts(rows)
}

// SearchFacts matches a substring against keys and values.
func (s *Store) SearchFacts(query string, limit int) ([]*Fact, error) {
	if limit <= 0 {
		limit = 20
	}
	like := "%" + strings.ToLower(query) + "%"
	rows, err := s.db.Query(`SELECT id,key,value,created_at,updated_at FROM memory
		WHERE lower(key) LIKE ? OR lower(value) LIKE ?
		ORDER BY updated_at DESC, id DESC LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("store: memory: %v", err)
	}
	return scanFacts(rows)
}

func scanFacts(rows *sql.Rows) ([]*Fact, error) {
	defer rows.Close()
	var out []*Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.Key, &f.Value, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: memory: %v", err)
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

// DeleteFact removes a fact by key.
func (s *Store) DeleteFact(key string) error {
	res, err := s.db.Exec(`DELETE FROM memory WHERE key=?`, key)
	if err != nil {
		return fmt.Errorf("store: memory: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: memory %s: %w", key, ErrNotFound)
	}
	return nil
}

// CountFacts returns the number of stored facts.
func (s *Store) CountFacts() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memory`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: memory: %v", err)
	}
	return n, nil
}

// TrimFacts keeps the newest max facts.
func (s *Store) TrimFacts(max int) error {
	if max <= 0 {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM memory WHERE id NOT IN (
		SELECT id FROM memory ORDER BY updated_at DESC, id DESC LIMIT ?)`, max)
	if err != nil {
		return fmt.Errorf("store: memory: %v", err)
	}
	return nil
}
