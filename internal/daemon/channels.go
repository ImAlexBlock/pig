package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alexblock/pig/internal/agent"
	"github.com/alexblock/pig/internal/channel"
	"github.com/alexblock/pig/internal/channel/qq"
	"github.com/alexblock/pig/internal/channel/telegram"
	"github.com/alexblock/pig/internal/store"
)

// chatProgress renders one chat's run progress into a single message.
type chatProgress struct {
	adapter channel.Adapter
	chatID  string
	replyTo string
	ch      chan agent.Event

	mu         sync.Mutex
	text       strings.Builder
	handle     channel.Handle
	lastUpdate time.Time
	notices    []string
}

func newChatProgress(ad channel.Adapter, chatID, replyTo string) *chatProgress {
	return &chatProgress{adapter: ad, chatID: chatID, replyTo: replyTo, ch: make(chan agent.Event, 256)}
}

func (p *chatProgress) push(ev agent.Event) {
	select {
	case p.ch <- ev:
	default:
	}
}

func (p *chatProgress) interval() time.Duration {
	if p.adapter.Name() == "qq" {
		return 2 * time.Second
	}
	return 3 * time.Second
}

func (p *chatProgress) flush(ctx context.Context) {
	p.mu.Lock()
	// QQ group chats only accept passive replies, so progress stays silent
	// there; direct chats stream with replace semantics.
	if p.adapter.Name() == "qq" && strings.HasPrefix(p.chatID, "g:") {
		p.mu.Unlock()
		return
	}
	if p.adapter.Caps().Edit == false || time.Since(p.lastUpdate) < p.interval() {
		p.mu.Unlock()
		return
	}
	status := "⏳ working…"
	if len(p.notices) > 0 {
		status = "⏳ " + p.notices[len(p.notices)-1]
	}
	handle := p.handle
	p.lastUpdate = time.Now()
	p.mu.Unlock()

	o := channel.Outbound{ChatID: p.chatID, Text: status, ReplyTo: p.replyTo, Markdown: false}
	if handle.ID == "" {
		h, err := p.adapter.Send(ctx, o)
		if err == nil {
			p.mu.Lock()
			p.handle = h
			p.mu.Unlock()
		}
		return
	}
	_ = p.adapter.Update(ctx, handle, o)
}

// startChannels builds the enabled adapters.
func (s *Server) startChannels(ctx context.Context) error {
	var adapters []channel.Adapter
	var problems []string
	cfg := s.cfg.Channel

	if cfg.Telegram.Enabled {
		env := cfg.Telegram.TokenEnv
		if env == "" {
			env = "PIG_TELEGRAM_TOKEN"
		}
		if token := os.Getenv(env); token == "" {
			problems = append(problems, fmt.Sprintf("channel telegram: missing token (%s)", env))
		} else {
			adapters = append(adapters, telegram.New(token))
		}
	}
	if cfg.QQ.Enabled {
		env := cfg.QQ.AppSecretEnv
		if env == "" {
			env = "PIG_QQ_APP_SECRET"
		}
		secret := os.Getenv(env)
		switch {
		case cfg.QQ.AppID == "":
			problems = append(problems, "channel qq: missing app_id")
		case secret == "":
			problems = append(problems, fmt.Sprintf("channel qq: missing secret (%s)", env))
		default:
			adapters = append(adapters, qq.New(cfg.QQ, secret))
		}
	}
	for _, p := range problems {
		s.logf("%s", p)
		fmt.Fprintln(os.Stderr, "pig: "+p)
	}
	if len(adapters) == 0 {
		return fmt.Errorf("serve: no channels enabled")
	}
	s.channels = adapters
	for _, ad := range adapters {
		p := channel.NewPairing(ad.Name())
		s.pairings[ad.Name()] = p
		fmt.Fprintf(os.Stderr, "pair %s code: %s\n", ad.Name(), p.Code())
		in := make(chan channel.Inbound, 32)
		adapter := ad
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := adapter.Run(ctx, in); err != nil && ctx.Err() == nil {
				s.logf("channel %s: %v", adapter.Name(), err)
			}
		}()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.consumeChannel(ctx, adapter, in)
		}()
	}
	return nil
}

func (s *Server) consumeChannel(ctx context.Context, ad channel.Adapter, in <-chan channel.Inbound) {
	for {
		select {
		case <-ctx.Done():
			return
		case ib, ok := <-in:
			if !ok {
				return
			}
			s.handleChannelMessage(ctx, ad, ib)
		}
	}
}

func (s *Server) handleChannelMessage(ctx context.Context, ad channel.Adapter, ib channel.Inbound) {
	state, err := s.st.GetChannelState(ad.Name(), ib.ChatID)
	if err != nil {
		state = &store.ChannelState{Channel: ad.Name(), ChatID: ib.ChatID}
	}
	if !state.Paired {
		if code, ok := channel.PairCommand(ib.Text); ok {
			p := s.pairings[ad.Name()]
			if p == nil {
				return
			}
			if good, reason := p.Verify(ib.ChatID, code); !good {
				s.sendText(ctx, ad, ib.ChatID, "pairing failed: "+reason, ib.MessageID)
				return
			}
			sess, cerr := s.sessionForChat(ad.Name(), ib.ChatID)
			if cerr != nil {
				s.sendText(ctx, ad, ib.ChatID, "error: "+cerr.Error(), ib.MessageID)
				return
			}
			state.SessionID = sess.ID
			state.Paired = true
			_ = s.st.SetChannelState(state)
			s.sendText(ctx, ad, ib.ChatID, "paired. send a message to start.", ib.MessageID)
			return
		}
		s.sendText(ctx, ad, ib.ChatID, "pairing required: /pair <code>", ib.MessageID)
		return
	}
	if state.SessionID == "" {
		sess, cerr := s.sessionForChat(ad.Name(), ib.ChatID)
		if cerr != nil {
			s.sendText(ctx, ad, ib.ChatID, "error: "+cerr.Error(), ib.MessageID)
			return
		}
		state.SessionID = sess.ID
		_ = s.st.SetChannelState(state)
	}

	if strings.HasPrefix(strings.TrimSpace(ib.Text), "/") {
		s.channelCommand(ctx, ad, ib, state)
		return
	}
	sess, err := s.st.GetSession(state.SessionID)
	if err != nil {
		sess, err = s.sessionForChat(ad.Name(), ib.ChatID)
		if err != nil {
			s.sendText(ctx, ad, ib.ChatID, "error: "+err.Error(), ib.MessageID)
			return
		}
		state.SessionID = sess.ID
		_ = s.st.SetChannelState(state)
	}
	s.channelRun(ctx, ad, ib, sess)
}

func (s *Server) sessionForChat(channelName, chatID string) (*store.Session, error) {
	workdir := s.cfg.Channel.Workdir
	if workdir == "" {
		workdir = s.paths.Workdir()
	}
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return nil, fmt.Errorf("channel: workdir: %v", err)
	}
	sess, err := s.createSession(workdir)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// renderFor adapts an outbound body to the channel. Channels without markdown
// get rendered plain text, not raw markdown source.
func renderFor(ad channel.Adapter, text string) (body string, markdown bool) {
	if ad.Caps().Markdown {
		return text, true
	}
	return channel.PlainText(text), false
}

func (s *Server) sendText(ctx context.Context, ad channel.Adapter, chatID, text, replyTo string) {
	body, markdown := renderFor(ad, text)
	_, err := ad.Send(ctx, channel.Outbound{ChatID: chatID, Text: body, ReplyTo: replyTo, Markdown: markdown})
	if err != nil {
		s.logf("channel %s send: %v", ad.Name(), err)
	}
}

func (s *Server) channelCommand(ctx context.Context, ad channel.Adapter, ib channel.Inbound, state *store.ChannelState) {
	fields := strings.Fields(ib.Text)
	cmd := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(ib.Text, fields[0]))
	switch cmd {
	case "/help":
		s.sendText(ctx, ad, ib.ChatID, helpText(), ib.MessageID)
	case "/new":
		sess, err := s.sessionForChat(ad.Name(), ib.ChatID)
		if err != nil {
			s.sendText(ctx, ad, ib.ChatID, "error: "+err.Error(), ib.MessageID)
			return
		}
		state.SessionID = sess.ID
		_ = s.st.SetChannelState(state)
		s.sendText(ctx, ad, ib.ChatID, "new session "+sess.ID, ib.MessageID)
	case "/resume":
		if rest == "" {
			s.sendText(ctx, ad, ib.ChatID, "usage: /resume <session-id>", ib.MessageID)
			return
		}
		if _, err := s.st.GetSession(rest); err != nil {
			s.sendText(ctx, ad, ib.ChatID, "session not found: "+rest, ib.MessageID)
			return
		}
		state.SessionID = rest
		_ = s.st.SetChannelState(state)
		s.sendText(ctx, ad, ib.ChatID, "resumed "+rest, ib.MessageID)
	case "/stop":
		if runID, _, ok := s.ActiveRun(state.SessionID); ok {
			_ = s.CancelRun(runID)
			s.sendText(ctx, ad, ib.ChatID, "stopped", ib.MessageID)
			return
		}
		s.sendText(ctx, ad, ib.ChatID, "nothing running", ib.MessageID)
	case "/status":
		s.sendText(ctx, ad, ib.ChatID, s.channelStatus(state.SessionID), ib.MessageID)
	case "/compact":
		sess, err := s.st.GetSession(state.SessionID)
		if err != nil {
			s.sendText(ctx, ad, ib.ChatID, "error: "+err.Error(), ib.MessageID)
			return
		}
		model, _ := s.cfg.Model(sess.Model)
		res, cerr := s.comp.Compact(ctx, compactRequest(sess.ID, model))
		if cerr != nil {
			s.sendText(ctx, ad, ib.ChatID, "error: "+cerr.Error(), ib.MessageID)
			return
		}
		if !res.Compacted {
			s.sendText(ctx, ad, ib.ChatID, "nothing to compact", ib.MessageID)
			return
		}
		s.sendText(ctx, ad, ib.ChatID, fmt.Sprintf("compacted with %s: %d → %d tokens", res.Method, res.TokensBefore, res.TokensAfter), ib.MessageID)
	case "/model":
		if rest == "" {
			sess, _ := s.st.GetSession(state.SessionID)
			if sess != nil {
				s.sendText(ctx, ad, ib.ChatID, "model: "+sess.Model, ib.MessageID)
			}
			return
		}
		if _, ok := s.cfg.Model(rest); !ok {
			s.sendText(ctx, ad, ib.ChatID, "unknown model: "+rest, ib.MessageID)
			return
		}
		if err := s.st.SetSessionModel(state.SessionID, rest); err != nil {
			s.sendText(ctx, ad, ib.ChatID, "error: "+err.Error(), ib.MessageID)
			return
		}
		s.sendText(ctx, ad, ib.ChatID, "model set to "+rest, ib.MessageID)
	case "/jobs":
		jobs, _ := s.st.ListJobs("", 10)
		if len(jobs) == 0 {
			s.sendText(ctx, ad, ib.ChatID, "no jobs", ib.MessageID)
			return
		}
		var sb strings.Builder
		for _, j := range jobs {
			fmt.Fprintf(&sb, "%s %s %s\n", j.ID, j.Kind, j.Status)
		}
		s.sendText(ctx, ad, ib.ChatID, strings.TrimSpace(sb.String()), ib.MessageID)
	default:
		s.sendText(ctx, ad, ib.ChatID, "unknown command; /help for the list", ib.MessageID)
	}
}

func (s *Server) channelStatus(sessionID string) string {
	sess, err := s.st.GetSession(sessionID)
	if err != nil {
		return "session: unknown"
	}
	model, _ := s.cfg.Model(sess.Model)
	totals, _ := s.st.SessionUsage(sessionID)
	jobs, _ := s.st.ListJobs("", 5)
	running := 0
	for _, j := range jobs {
		if j.Status == store.JobRunning {
			running++
		}
	}
	return fmt.Sprintf("session: %s\nmodel: %s (window %d)\nusage: in %d out %d cost $%.4f\njobs running: %d",
		sess.ID, sess.Model, model.ContextWindow, totals.Input, totals.Output, totals.CostUSD, running)
}

func helpText() string {
	return strings.Join([]string{
		"/new — start a new session",
		"/resume <id> — resume a session",
		"/stop — cancel the running turn",
		"/status — session, model, usage, jobs",
		"/compact — compact the context now",
		"/model [ref] — show or set the model",
		"/jobs — recent background jobs",
		"/help — this list",
	}, "\n")
}

// channelRun starts a run and mirrors it into the chat.
// saveInboundAttachments stores chat attachments as artifacts and returns the
// ids plus a prompt note the model can act on.
func (s *Server) saveInboundAttachments(sessionID string, attachments []channel.Attachment) ([]string, string) {
	var ids []string
	var lines []string
	for _, a := range attachments {
		if len(a.Data) == 0 {
			continue
		}
		mime := a.Mime
		if mime == "" {
			mime = "application/octet-stream"
		}
		art, err := s.st.PutArtifact(sessionID, mime, a.Data)
		if err != nil {
			s.logf("attachment store: %v", err)
			continue
		}
		ids = append(ids, art.ID)
		name := a.Name
		if name == "" {
			name = art.ID
		}
		lines = append(lines, fmt.Sprintf("[attachment %s %s (%d bytes) artifact://%s]", a.Kind, name, len(a.Data), art.ID))
	}
	if len(ids) > 0 {
		lines = append(lines, "Read an attachment with `read artifact://<id>`; images are shown to you when the model supports them.")
	}
	return ids, strings.Join(lines, "\n")
}

func (s *Server) channelRun(ctx context.Context, ad channel.Adapter, ib channel.Inbound, sess *store.Session) {
	attachIDs, attachNote := s.saveInboundAttachments(sess.ID, ib.Attachments)
	prompt := ib.Text
	if attachNote != "" {
		prompt = strings.TrimSpace(prompt + "\n\n" + attachNote)
	}
	p := newChatProgress(ad, ib.ChatID, ib.MessageID)
	// Subscribe to the session first: a fast run may finish before we know its
	// id, and losing run_end would strand the chat.
	sub := s.fan.addSessionFunc(sess.ID, func(ev agent.Event) error {
		p.push(ev)
		return nil
	})
	defer s.fan.remove(sub)

	runID, sessionID, queued, err := s.StartRun(runSpec{
		SessionID:     sess.ID,
		Prompt:        prompt,
		Attachments:   attachIDs,
		Queue:         true,
		Client:        ad.Name(),
		OriginChannel: ad.Name(),
		OriginChat:    ib.ChatID,
	})
	if err != nil {
		s.sendText(ctx, ad, ib.ChatID, "error: "+err.Error(), ib.MessageID)
		return
	}
	if queued {
		// Wait until the queued turn becomes the active run.
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			if id, _, ok := s.ActiveRun(sessionID); ok && id != runID {
				runID = id
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	ticker := time.NewTicker(p.interval())
	defer ticker.Stop()
	deadline := time.NewTimer(2 * time.Hour)
	defer deadline.Stop()
	pending := ""
	var produced []agent.Event
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			p.flush(ctx)
		case ev := <-p.ch:
			if ev.RunID != "" && ev.RunID != runID {
				continue
			}
			switch ev.Type {
			case agent.EvTextDelta:
				p.mu.Lock()
				p.text.WriteString(ev.Text)
				p.mu.Unlock()
			case agent.EvArtifact:
				if ev.Artifact != "" && len(produced) < channel.MaxOutboundFiles {
					produced = append(produced, ev)
				}
			case agent.EvNotice:
				p.mu.Lock()
				if ev.Notice != "" {
					p.notices = append(p.notices, ev.Notice)
					if len(p.notices) > 20 {
						p.notices = p.notices[1:]
					}
				}
				p.mu.Unlock()
			case agent.EvError:
				pending = "✗ " + ev.Error
			case agent.EvRunEnd:
				final := pending
				if final == "" {
					p.mu.Lock()
					final = strings.TrimSpace(p.text.String())
					p.mu.Unlock()
				}
				if final == "" {
					final = "(no output)"
				}
				body, markdown := renderFor(ad, final)
				chunks := channel.Split(body, ad.Caps().MaxChars)
				for i, chunk := range chunks {
					// Every chunk stays inside the passive reply window: a chunk
					// without msg_id becomes an active message, which QQ renders
					// as "unsupported message type" for unapproved bots.
					replyTo := ib.MessageID
					out := channel.Outbound{ChatID: ib.ChatID, Text: chunk, ReplyTo: replyTo, Markdown: markdown}
					// Replace the status bubble instead of leaving "working…" above the answer.
					if i == 0 {
						p.mu.Lock()
						handle := p.handle
						p.mu.Unlock()
						if handle.ID != "" && ad.Caps().Edit {
							if uerr := ad.Update(ctx, handle, out); uerr == nil {
								continue
							}
						}
					}
					if _, serr := ad.Send(ctx, out); serr != nil {
						s.logf("channel %s send: %v", ad.Name(), serr)
					}
				}
				s.sendProducedMedia(ctx, ad, ib, produced)
				return
			}
		}
	}
}

// notifyChannel reports a finished job to its origin chat.
func (s *Server) notifyChannel(j *store.Job) {
	var ad channel.Adapter
	for _, a := range s.channels {
		if a.Name() == j.OriginChannel {
			ad = a
			break
		}
	}
	if ad == nil {
		return
	}
	var text string
	if j.Status == store.JobDone {
		summary := firstLineOf(j.Result, 300)
		text = fmt.Sprintf("job %s done: %s", j.ID, summary)
	} else {
		text = fmt.Sprintf("job %s %s: %s", j.ID, j.Status, firstLineOf(j.Error, 300))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := ad.Send(ctx, channel.Outbound{ChatID: j.OriginChat, Text: text}); err != nil {
		s.logf("channel notify: %v", err)
	}
}

func firstLineOf(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return strings.TrimSpace(s)
}

// sendProducedMedia delivers images a run produced (screenshots, rendered
// files) back to the chat when the adapter supports media.
func (s *Server) sendProducedMedia(ctx context.Context, ad channel.Adapter, ib channel.Inbound, produced []agent.Event) {
	sender, ok := ad.(channel.MediaSender)
	if !ok || len(produced) == 0 {
		return
	}
	files := make([]channel.File, 0, len(produced))
	for _, ev := range produced {
		art, data, err := s.st.ReadArtifact(ev.Artifact)
		if err != nil {
			continue
		}
		if len(data) > channel.MaxOutboundFileBytes {
			continue
		}
		name := ev.Name
		if name == "" {
			name = art.ID
		}
		files = append(files, channel.File{Name: name, Mime: art.Mime, Data: data})
	}
	if len(files) == 0 {
		return
	}
	if err := sender.SendMedia(ctx, ib.ChatID, files, ib.MessageID); err != nil {
		s.logf("channel %s media: %v", ad.Name(), err)
	}
}
