package daemon

import (
	"context"
	"time"

	"github.com/ImAlexBlock/pig/internal/compact"
	"github.com/ImAlexBlock/pig/internal/store"
)

// idleFor reports whether the daemon has been idle long enough to exit.
func (s *Server) idleFor(seconds int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.runs) > 0 || len(s.channels) > 0 {
		return false
	}
	queued, running, err := s.jobs.Capacity()
	if err != nil || queued > 0 || running > 0 {
		return false
	}
	return time.Since(s.lastActivity) > time.Duration(seconds)*time.Second
}

// schedulerLoop runs the idle-compaction scheduler once per second.
func (s *Server) schedulerLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.schedulerTick(ctx)
		}
	}
}

// schedulerTick compacts idle sessions and honours daemon.idle_exit_seconds.
func (s *Server) schedulerTick(ctx context.Context) {
	if s.cfg.Daemon.IdleExitSeconds > 0 && s.idleFor(s.cfg.Daemon.IdleExitSeconds) {
		s.logf("idle for %ds, exiting", s.cfg.Daemon.IdleExitSeconds)
		s.cancel()
		return
	}
	if !s.cfg.Compaction.MidTurn && s.cfg.Compaction.IdleTimeoutSeconds <= 0 {
		return
	}
	if s.RunCounts() > 0 {
		return
	}
	if q, running, err := s.jobs.Capacity(); err != nil || q > 0 || running > 0 {
		return
	}
	sessions, err := s.st.ListSessions(20)
	if err != nil {
		return
	}
	now := time.Now()
	for _, sess := range sessions {
		if sess.Archived {
			continue
		}
		model, ok := s.cfg.Model(sess.Model)
		if !ok {
			continue
		}
		entries, err := s.st.AllEntries(sess.ID)
		if err != nil {
			continue
		}
		cmp, _ := s.st.LatestCompaction(sess.ID)
		startSeq := 0
		if cmp != nil {
			startSeq = cmp.FirstKeptSeq
		}
		window := make([]*store.Entry, 0, len(entries))
		for _, e := range entries {
			if e.Seq >= startSeq {
				window = append(window, e)
			}
		}
		msgs := compact.Project(cmp, window, compact.ProjectOptions{ImageInput: model.SupportsImages()})
		tokens := compact.ProjectTokens(msgs, s.cfg.Snapcompact.ImageTokens)
		if tokens < int(s.cfg.Compaction.IdleThreshold*float64(model.ContextWindow)) {
			continue
		}
		last := sess.UpdatedAt
		s.mu.Lock()
		if t, ok := s.lastRequest[sess.ID]; ok {
			last = t.Unix()
		}
		s.mu.Unlock()

		target := time.Unix(last, 0).Add(time.Duration(s.cfg.Compaction.IdleTimeoutSeconds) * time.Second)
		if usage, err := s.st.LastUsage(sess.ID); err == nil && usage.CacheRead+usage.CacheWrite > 0 {
			ttl := model.Cache.TTLSeconds
			if ttl > 60 {
				target = time.Unix(last, 0).Add(time.Duration(ttl-60) * time.Second)
			}
		}
		if now.Before(target) {
			continue
		}
		if _, err := s.comp.Compact(ctx, compact.Request{
			SessionID:           sess.ID,
			Reason:              "idle",
			ModelSupportsImages: model.SupportsImages(),
		}); err != nil {
			s.logf("idle compaction %s: %v", sess.ID, err)
		}
	}
}
