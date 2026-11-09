package daemon

import (
	"sync"

	"github.com/ImAlexBlock/pig/internal/agent"
	"github.com/ImAlexBlock/pig/internal/ipc"
)

// subscriber is one event stream consumer.
type subscriber struct {
	id     int
	conn   *ipc.Conn
	fn     func(agent.Event) error
	ch     chan agent.Event
	done   chan struct{}
	closed bool
	mu     sync.Mutex
	// dropped counts delta events discarded for a slow consumer.
	dropped int
}

// fanout routes run events to subscribers.
type fanout struct {
	mu      sync.Mutex
	next    int
	run     map[string]map[int]*subscriber
	session map[string]map[int]*subscriber
}

func newFanout() *fanout {
	return &fanout{
		run:     map[string]map[int]*subscriber{},
		session: map[string]map[int]*subscriber{},
	}
}

const subscriberBuffer = 256

func (f *fanout) newSubscriber(conn *ipc.Conn) *subscriber {
	f.mu.Lock()
	f.next++
	id := f.next
	f.mu.Unlock()
	return &subscriber{id: id, conn: conn, ch: make(chan agent.Event, subscriberBuffer), done: make(chan struct{})}
}

func (f *fanout) start(s *subscriber) {
	go func() {
		for {
			select {
			case <-s.done:
				return
			case ev := <-s.ch:
				var err error
				if s.fn != nil {
					err = s.fn(ev)
				} else if s.conn != nil {
					err = s.conn.Send("run.event", map[string]any{"run_id": ev.RunID, "event": ev})
				}
				if err != nil {
					return
				}
			}
		}
	}()
}

func (f *fanout) addRun(conn *ipc.Conn, runID string) *subscriber {
	s := f.newSubscriber(conn)
	f.mu.Lock()
	if f.run[runID] == nil {
		f.run[runID] = map[int]*subscriber{}
	}
	f.run[runID][s.id] = s
	f.mu.Unlock()
	f.start(s)
	return s
}

// addRunFunc subscribes a callback instead of a socket.
func (f *fanout) addRunFunc(runID string, fn func(agent.Event) error) *subscriber {
	s := f.newSubscriber(nil)
	s.fn = fn
	f.mu.Lock()
	if f.run[runID] == nil {
		f.run[runID] = map[int]*subscriber{}
	}
	f.run[runID][s.id] = s
	f.mu.Unlock()
	f.start(s)
	return s
}

// addSessionFunc subscribes a callback to every event of a session.
func (f *fanout) addSessionFunc(sessionID string, fn func(agent.Event) error) *subscriber {
	s := f.newSubscriber(nil)
	s.fn = fn
	f.mu.Lock()
	if f.session[sessionID] == nil {
		f.session[sessionID] = map[int]*subscriber{}
	}
	f.session[sessionID][s.id] = s
	f.mu.Unlock()
	f.start(s)
	return s
}

func (f *fanout) addSession(conn *ipc.Conn, sessionID string) *subscriber {
	s := f.newSubscriber(conn)
	f.mu.Lock()
	if f.session[sessionID] == nil {
		f.session[sessionID] = map[int]*subscriber{}
	}
	f.session[sessionID][s.id] = s
	f.mu.Unlock()
	f.start(s)
	return s
}

func (f *fanout) remove(s *subscriber) {
	if s == nil {
		return
	}
	f.mu.Lock()
	for k, m := range f.run {
		delete(m, s.id)
		if len(m) == 0 {
			delete(f.run, k)
		}
	}
	for k, m := range f.session {
		delete(m, s.id)
		if len(m) == 0 {
			delete(f.session, k)
		}
	}
	f.mu.Unlock()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	s.mu.Unlock()
}

// dropRun removes a subscriber's run subscription only.
func (f *fanout) dropRun(s *subscriber, runID string) {
	if s == nil {
		return
	}
	f.mu.Lock()
	if m := f.run[runID]; m != nil {
		delete(m, s.id)
		if len(m) == 0 {
			delete(f.run, runID)
		}
	}
	f.mu.Unlock()
}

// dropSession removes a subscriber's session subscription only.
func (f *fanout) dropSession(s *subscriber, sessionID string) {
	if s == nil {
		return
	}
	f.mu.Lock()
	if m := f.session[sessionID]; m != nil {
		delete(m, s.id)
		if len(m) == 0 {
			delete(f.session, sessionID)
		}
	}
	f.mu.Unlock()
}

func (f *fanout) emit(runID, sessionID string, ev agent.Event) {
	if ev.RunID == "" {
		ev.RunID = runID
	}
	if ev.SessionID == "" {
		ev.SessionID = sessionID
	}
	var targets []*subscriber
	seen := map[any]bool{}
	add := func(s *subscriber) {
		var key any = s
		if s.conn != nil {
			key = s.conn
		}
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, s)
	}
	f.mu.Lock()
	for _, s := range f.run[runID] {
		add(s)
	}
	for _, s := range f.session[sessionID] {
		add(s)
	}
	f.mu.Unlock()
	for _, s := range targets {
		f.deliver(s, ev)
	}
}

func (f *fanout) deliver(s *subscriber, ev agent.Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	select {
	case s.ch <- ev:
		return
	default:
	}
	if isDelta(ev.Type) {
		s.mu.Lock()
		s.dropped++
		first := s.dropped == 1
		s.mu.Unlock()
		if first {
			// Best-effort notice; still may be dropped on a wedged reader.
			select {
			case s.ch <- agent.Event{Type: agent.EvNotice, Level: "warn", Notice: "slow consumer"}:
			default:
			}
		}
		return
	}
	// Terminal and tool events must be delivered; block briefly.
	select {
	case s.ch <- ev:
	case <-s.done:
	}
}

func isDelta(t string) bool {
	return t == agent.EvTextDelta || t == agent.EvThinkingDelta
}
