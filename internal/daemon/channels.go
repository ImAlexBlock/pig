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

const (
	// firstStatusDelay keeps QQ from spending a passive reply on a status line
	// before there is anything to report.
	firstStatusDelay = 6 * time.Second
	// statusEvery paces further status lines on channels that cannot edit a
	// message: each one costs a passive reply.
	statusEvery = 60 * time.Second
	// maxStatuses caps the status lines a run may post on such a channel.
	maxStatuses = 2
	// milestoneMinSlots is the free-slot floor below which no further status is
	// sent: the answer needs those replies.
	milestoneMinSlots = 3
)

// chatProgress renders one chat's run progress into a single status line.
type chatProgress struct {
	adapter channel.Adapter
	chatID  string
	replyTo string
	ch      chan agent.Event

	mu           sync.Mutex
	text         strings.Builder
	handle       channel.Handle
	lastUpdate   time.Time
	notices      []string
	startedAt    time.Time
	steps        int
	lastTool     string
	phase        string
	ctxTokens    int
	sentFirst    bool
	statuses     int
	lastStatusAt time.Time
}

func newChatProgress(ad channel.Adapter, chatID, replyTo string) *chatProgress {
	return &chatProgress{
		adapter:   ad,
		chatID:    chatID,
		replyTo:   replyTo,
		ch:        make(chan agent.Event, 256),
		startedAt: time.Now(),
	}
}

func (p *chatProgress) push(ev agent.Event) {
	select {
	case p.ch <- ev:
	default:
	}
}

// observe folds one run event into the progress counters.
func (p *chatProgress) observe(ev agent.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch ev.Type {
	case agent.EvToolStart:
		if ev.ToolName != "" {
			p.lastTool = ev.ToolName
		}
	case agent.EvToolEnd:
		p.steps++
		if ev.ToolName != "" {
			p.lastTool = ev.ToolName
		}
	case agent.EvTodo:
		for _, ph := range ev.Phases {
			for _, it := range ph.Items {
				if it.Status == "active" {
					p.phase = ph.Name
				}
			}
		}
	case agent.EvUsage:
		if ev.Usage != nil && ev.Usage.Input > 0 {
			p.ctxTokens = ev.Usage.Input
		}
	case agent.EvNotice:
		if ev.Notice != "" {
			p.notices = append(p.notices, ev.Notice)
			if len(p.notices) > 20 {
				p.notices = p.notices[1:]
			}
		}
	}
}

func (p *chatProgress) interval() time.Duration {
	if p.adapter.Name() == "qq" {
		return 2 * time.Second
	}
	return 3 * time.Second
}

// statusLocked renders the status line: elapsed, phase, tool, step count and
// context size, plus the latest notice.
func (p *chatProgress) statusLocked(elapsed time.Duration) string {
	parts := []string{"⏳ " + shortDuration(elapsed)}
	if p.phase != "" {
		parts = append(parts, p.phase)
	}
	if p.lastTool != "" {
		parts = append(parts, p.lastTool)
	}
	if p.steps > 0 {
		parts = append(parts, fmt.Sprintf("%d tools", p.steps))
	}
	if p.ctxTokens > 0 {
		parts = append(parts, "ctx "+shortTokens(p.ctxTokens))
	}
	if n := len(p.notices); n > 0 {
		if last := strings.TrimSpace(p.notices[n-1]); last != "" {
			parts = append(parts, trimRunes(last, 40))
		}
	}
	return strings.Join(parts, " · ")
}

// flush sends or updates the status line when the channel allows it.
func (p *chatProgress) flush(ctx context.Context) error {
	// QQ group chats only accept passive replies, so progress stays silent
	// there; direct chats stream with replace semantics when they support it.
	if p.adapter.Name() == "qq" && strings.HasPrefix(p.chatID, "g:") {
		return nil
	}
	editable := p.adapter.Caps().Edit

	p.mu.Lock()
	elapsed := time.Since(p.startedAt)
	sent := p.sentFirst
	handle := p.handle
	recent := time.Since(p.lastUpdate)
	since := time.Since(p.lastStatusAt)
	statuses := p.statuses
	steps := p.steps
	text := p.statusLocked(elapsed)
	p.mu.Unlock()

	if sent && recent < p.interval() {
		return nil
	}
	if !editable {
		// Without message editing every status costs a passive reply: wait for
		// real work, then post at most one line a minute and never more than
		// maxStatuses per run.
		if !sent {
			if elapsed < firstStatusDelay && steps == 0 {
				return nil
			}
		} else {
			if statuses >= maxStatuses || since < statusEvery {
				return nil
			}
			if sr, ok := p.adapter.(channel.SlotReporter); ok && sr.RemainingSlots(p.chatID, p.replyTo) < milestoneMinSlots {
				return nil
			}
		}
	}

	o := channel.Outbound{ChatID: p.chatID, Text: text, ReplyTo: p.replyTo}
	if handle.ID == "" || !editable {
		h, err := p.adapter.Send(ctx, o)
		p.mu.Lock()
		p.lastUpdate = time.Now()
		if err == nil {
			if p.handle.ID == "" {
				p.handle = h
			}
			p.sentFirst = true
			p.statuses++
			p.lastStatusAt = time.Now()
		}
		p.mu.Unlock()
		return err
	}
	err := p.adapter.Update(ctx, handle, o)
	p.mu.Lock()
	p.lastUpdate = time.Now()
	p.mu.Unlock()
	return err
}

func shortDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 0 {
		s = 0
	}
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	}
}

func shortTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 100000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1000000:
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// budgetChunks limits an answer to the replies the chat can still receive and
// marks where the rest went, instead of losing it to a failed send.
func budgetChunks(ad channel.Adapter, chatID, replyTo, sessionID string, chunks []string) []string {
	slots, ok := passiveSlots(ad, chatID, replyTo)
	if !ok {
		return chunks
	}
	if slots <= 0 {
		return nil
	}
	if len(chunks) <= slots {
		return chunks
	}
	out := chunks[:slots]
	out[slots-1] = strings.TrimRight(out[slots-1], "\n") +
		fmt.Sprintf("\n[… reply limit reached; full text: session://%s]", sessionID)
	return out
}

// passiveSlots reports how many replies the chat can still send for a message.
func passiveSlots(ad channel.Adapter, chatID, replyTo string) (int, bool) {
	sr, ok := ad.(channel.SlotReporter)
	if !ok || replyTo == "" {
		return 0, false
	}
	return sr.RemainingSlots(chatID, replyTo), true
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
			// A run takes minutes; the reader must not block on it or a /stop
			// sent mid-run would only be seen after the run finished.
			if strings.HasPrefix(strings.TrimSpace(ib.Text), "/") {
				go s.handleChannelMessage(ctx, ad, ib)
				continue
			}
			s.dispatchMessage(ctx, ad, ib)
		}
	}
}

// dispatchMessage handles one message without blocking the adapter reader while
// keeping arrival order per chat.
func (s *Server) dispatchMessage(ctx context.Context, ad channel.Adapter, ib channel.Inbound) {
	key := ad.Name() + "|" + ib.ChatID
	done := make(chan struct{})

	s.chatMu.Lock()
	prev := s.chatTail[key]
	s.chatTail[key] = done
	s.chatMu.Unlock()

	go func() {
		defer close(done)
		defer func() {
			s.chatMu.Lock()
			if s.chatTail[key] == done {
				delete(s.chatTail, key)
			}
			s.chatMu.Unlock()
		}()
		if prev != nil {
			select {
			case <-prev:
			case <-ctx.Done():
				return
			}
		}
		s.handleChannelMessage(ctx, ad, ib)
	}()
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
		dropped := s.ClearQueue(state.SessionID)
		if runID, _, ok := s.ActiveRun(state.SessionID); ok {
			_ = s.CancelRun(runID)
			if dropped > 0 {
				s.sendText(ctx, ad, ib.ChatID, fmt.Sprintf("stopping (dropped %d queued)", dropped), ib.MessageID)
				return
			}
			s.sendText(ctx, ad, ib.ChatID, "stopping", ib.MessageID)
			return
		}
		if dropped > 0 {
			s.sendText(ctx, ad, ib.ChatID, fmt.Sprintf("queue cleared (%d dropped)", dropped), ib.MessageID)
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
			if err := p.flush(ctx); err != nil {
				s.logf("channel %s progress: %v", ad.Name(), err)
			}
		case ev := <-p.ch:
			if ev.RunID != "" && ev.RunID != runID {
				continue
			}
			p.observe(ev)
			switch ev.Type {
			case agent.EvTextDelta:
				p.mu.Lock()
				p.text.WriteString(ev.Text)
				p.mu.Unlock()
			case agent.EvArtifact:
				if ev.Artifact != "" && len(produced) < channel.MaxOutboundFiles {
					produced = append(produced, ev)
				}
			case agent.EvError:
				pending = "✗ " + ev.Error
			case agent.EvRunEnd:
				final := pending
				if final == "" && ev.Stop == "canceled" {
					final = "⏳ stopped after " + shortDuration(time.Duration(ev.ElapsedMS)*time.Millisecond)
				}
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
				chunks = budgetChunks(ad, ib.ChatID, ib.MessageID, sess.ID, chunks)
				if len(chunks) == 0 {
					s.logf("channel %s reply limit reached for %s; answer kept in session %s", ad.Name(), ib.MessageID, sess.ID)
					return
				}
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
