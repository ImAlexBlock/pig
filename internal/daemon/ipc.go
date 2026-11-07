package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/alexblock/pig/internal/compact"
	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/ipc"
	"github.com/alexblock/pig/internal/provider"
	"github.com/alexblock/pig/internal/store"
)

func decodeParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &ipc.Error{Code: ipc.CodeInvalidParams, Message: err.Error()}
	}
	return nil
}

func (s *Server) handleIPC(ctx context.Context, c *ipc.Conn, method string, params json.RawMessage) (any, error) {
	switch method {
	case "daemon.status":
		return s.statusPayload(), nil

	case "daemon.shutdown":
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.cancel()
		}()
		return map[string]any{"ok": true}, nil

	case "daemon.reload":
		return s.reload()

	case "session.list":
		var p struct {
			Limit int `json:"limit"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		sessions, err := s.st.ListSessions(p.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessions": sessions}, nil

	case "session.load":
		var p struct {
			ID   string `json:"id"`
			Tail int    `json:"tail"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		sess, err := s.resolveSession(p.ID, "")
		if err != nil {
			return nil, err
		}
		entries, err := s.st.TailEntries(sess.ID, p.Tail)
		if err != nil {
			return nil, err
		}
		cmp, _ := s.st.LatestCompaction(sess.ID)
		jobs, _ := s.st.ListJobs("", 20)
		return map[string]any{"session": sess, "entries": entries, "compaction": cmp, "jobs": jobs}, nil

	case "session.new":
		var p struct {
			CWD string `json:"cwd"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		cwd := p.CWD
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		sess, err := s.createSession(cwd)
		if err != nil {
			return nil, err
		}
		return map[string]any{"session": sess}, nil

	case "session.rm":
		var p struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if _, _, ok := s.ActiveRun(p.ID); ok {
			return nil, fmt.Errorf("session busy")
		}
		if err := s.st.DeleteSession(p.ID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "run.start":
		var p struct {
			SessionID   string   `json:"session_id"`
			Prompt      string   `json:"prompt"`
			CWD         string   `json:"cwd"`
			Model       string   `json:"model"`
			Client      string   `json:"client"`
			Queue       *bool    `json:"queue"`
			Background  bool     `json:"background"`
			Attachments []string `json:"attachments"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		queue := true
		if p.Queue != nil {
			queue = *p.Queue
		}
		spec := runSpec{
			SessionID: p.SessionID, Prompt: p.Prompt, CWD: p.CWD, Model: p.Model,
			Client: p.Client, Queue: queue, Attachments: p.Attachments,
		}
		if p.Background {
			jobID, err := s.StartBackground(spec)
			if err != nil {
				return nil, err
			}
			sess, _ := s.resolveSession(p.SessionID, p.CWD)
			return map[string]any{"job_id": jobID, "session_id": sess.ID}, nil
		}
		runID, sessionID, queued, err := s.StartRun(spec)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run_id": runID, "session_id": sessionID, "queued": queued}, nil

	case "run.input":
		var p struct {
			RunID string `json:"run_id"`
			Text  string `json:"text"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if err := s.Input(p.RunID, p.Text); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "run.cancel":
		var p struct {
			RunID string `json:"run_id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if err := s.CancelRun(p.RunID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "run.detach":
		var p struct {
			RunID string `json:"run_id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if err := s.Detach(p.RunID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "run.subscribe":
		var p struct {
			RunID string `json:"run_id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		s.mu.Lock()
		rec := s.runs[p.RunID]
		s.mu.Unlock()
		if rec == nil {
			return nil, fmt.Errorf("run: unknown run %s", p.RunID)
		}
		s.subscribeConn(c, rec.ID, rec.SessionID, true)
		return map[string]any{"run_id": rec.ID, "session_id": rec.SessionID}, nil

	case "run.attach":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		runID, sessionID, ok := s.ActiveRun(p.SessionID)
		if !ok {
			s.subscribeConn(c, "", p.SessionID, false)
			return map[string]any{"run_id": "", "session_id": p.SessionID, "active": false}, nil
		}
		s.subscribeConn(c, runID, sessionID, true)
		return map[string]any{"run_id": runID, "session_id": sessionID, "active": true}, nil

	case "jobs.list":
		var p struct {
			Status string `json:"status"`
			Limit  int    `json:"limit"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		jobs, err := s.st.ListJobs(p.Status, p.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"jobs": jobs}, nil

	case "jobs.get":
		var p struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		j, err := s.st.GetJob(p.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": j}, nil

	case "jobs.wait":
		var p struct {
			ID             string `json:"id"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		j, err := s.jobs.WaitJob(ctx, p.ID, time.Duration(p.TimeoutSeconds)*time.Second)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": j}, nil

	case "jobs.cancel":
		var p struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if err := s.jobs.CancelJob(p.ID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "jobs.retry":
		var p struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if err := s.st.RetryJob(p.ID); err != nil {
			return nil, err
		}
		if j, err := s.st.GetJob(p.ID); err == nil {
			_, _ = s.st.AppendEntry(j.SessionID, store.KindNotice, "",
				store.NewNoticeContent(store.NoticeContent{Level: "warn", Text: "[previous attempt interrupted]"}), 0)
		}
		s.jobs.Wake()
		return map[string]any{"ok": true}, nil

	case "jobs.logs":
		var p struct {
			ID   string `json:"id"`
			Tail int    `json:"tail"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		text, err := s.jobs.JobLogs(p.ID, p.Tail)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text}, nil

	case "skills.list":
		out := make([]map[string]any, 0, 8)
		for _, sk := range s.skills.List() {
			item := map[string]any{"name": sk.Name, "description": sk.Description, "dir": sk.Dir}
			if len(sk.Tools) > 0 {
				names := make([]string, 0, len(sk.Tools))
				for _, t := range sk.Tools {
					names = append(names, t.Name)
				}
				item["tools"] = names
			}
			out = append(out, item)
		}
		return map[string]any{"skills": out}, nil

	case "sessions.usage":
		var p struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		totals, err := s.st.SessionUsage(p.ID)
		if err != nil {
			return nil, err
		}
		return totals, nil

	case "rtk.gain":
		g, err := s.st.RTKGain()
		if err != nil {
			return nil, err
		}
		return g, nil

	case "compact.now":
		var p struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		sess, err := s.st.GetSession(p.ID)
		if err != nil {
			return nil, err
		}
		model, _ := s.cfg.Model(sess.Model)
		res, err := s.comp.Compact(ctx, compactRequest(sess.ID, model))
		if err != nil {
			return nil, err
		}
		return res, nil

	case "session.set_model":
		var p struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		}
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
		if _, ok := s.cfg.Model(p.Model); !ok {
			return nil, fmt.Errorf("model: unknown ref %q", p.Model)
		}
		if err := s.st.SetSessionModel(p.ID, p.Model); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "daemon.pair":
		codes := map[string]string{}
		for name, p := range s.pairings {
			codes[name] = p.Code()
		}
		return map[string]any{"codes": codes}, nil
	}
	return nil, ipc.ErrMethodNotFound
}

func (s *Server) subscribeConn(c *ipc.Conn, runID, sessionID string, withSnapshot bool) {
	var subs []*subscriber
	if runID != "" {
		subs = append(subs, s.fan.addRun(c, runID))
	}
	if sessionID != "" {
		subs = append(subs, s.fan.addSession(c, sessionID))
	}
	s.mu.Lock()
	s.connSubs[c.ID()] = append(s.connSubs[c.ID()], subs...)
	s.mu.Unlock()
	if withSnapshot && sessionID != "" {
		tail, _ := s.st.TailEntries(sessionID, 200)
		sess, _ := s.st.GetSession(sessionID)
		_ = c.Send("run.snapshot", map[string]any{"session_id": sessionID, "session": sess, "entries_tail": tail})
	}
}

func (s *Server) statusPayload() map[string]any {
	queued, running, _ := s.jobs.Capacity()
	channels := make([]map[string]any, 0, len(s.channels))
	for _, ad := range s.channels {
		channels = append(channels, map[string]any{"name": ad.Name()})
	}
	var mtime any
	if st, err := os.Stat(s.cfg.Path); err == nil {
		mtime = st.ModTime().Unix()
	}
	pid := os.Getpid()
	if v, ok := ReadPID(s.paths.PID()); ok {
		pid = v
	}
	return map[string]any{
		"pid":          pid,
		"uptime":       int64(time.Since(s.started).Seconds()),
		"version":      Version,
		"runs":         s.RunCounts(),
		"jobs":         map[string]any{"queued": queued, "running": running},
		"channels":     channels,
		"config_mtime": mtime,
		"session":      s.lastSessionID(),
	}
}

func (s *Server) lastSessionID() string {
	if sess, err := s.st.LatestSession(); err == nil {
		return sess.ID
	}
	return ""
}

func (s *Server) reload() (any, error) {
	if s.RunCounts() > 0 {
		return nil, fmt.Errorf("daemon: runs active")
	}
	if _, running, _ := s.jobs.Capacity(); running > 0 {
		return nil, fmt.Errorf("daemon: jobs running")
	}
	cfg, err := config.Load(s.cfg.Path)
	if err != nil {
		return nil, err
	}
	if cfg.Channel != s.cfg.Channel {
		return nil, fmt.Errorf("daemon: restart required for channel changes")
	}
	s.cfg = cfg
	s.jobs.SetConfig(cfg)
	reg := providerRegistry(cfg)
	s.runner.SetRegistry(reg)
	s.ag.Config = cfg
	s.ag.Runner = s.runner
	return map[string]any{"ok": true}, nil
}

// compactRequest builds a compaction request for a session.
func compactRequest(sessionID string, model config.Model) compact.Request {
	return compact.Request{
		SessionID:           sessionID,
		Reason:              "manual",
		ModelSupportsImages: model.SupportsImages(),
	}
}

// providerRegistry rebuilds the provider client cache for a config.
func providerRegistry(cfg *config.Config) *provider.Registry {
	return provider.NewRegistry(cfg)
}
