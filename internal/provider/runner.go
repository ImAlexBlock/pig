package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/store"
)

type coolEntry struct {
	until time.Time
	level int
}

// Outcome reports which model answered a request.
type Outcome struct {
	Provider string
	Model    string
	Ref      string
	Notices  []string
}

// Runner executes requests over a failover chain.
type Runner struct {
	cfg   *config.Config
	reg   *Registry
	store *store.Store

	mu     sync.Mutex
	cool   map[string]*coolEntry
	sticky map[string]string

	now func() time.Time
}

// NewRunner returns a runner. store may be nil (usage is then dropped).
func NewRunner(cfg *config.Config, reg *Registry, st *store.Store) *Runner {
	return &Runner{
		cfg:    cfg,
		reg:    reg,
		store:  st,
		cool:   map[string]*coolEntry{},
		sticky: map[string]string{},
		now:    time.Now,
	}
}

// SetRegistry swaps the client registry (used by daemon reload).
func (r *Runner) SetRegistry(reg *Registry) {
	r.mu.Lock()
	r.reg = reg
	r.mu.Unlock()
}

// Chain returns the ordered refs to try for a role. The session model is
// tried first, then the role chain, deduplicated.
func (r *Runner) Chain(sessionModel, role string) []string {
	var roleChain []string
	if role == "subagent" && sessionModel != "" {
		roleChain = append(roleChain, sessionModel)
		roleChain = append(roleChain, r.cfg.RoleChain("subagent")...)
	} else if sessionModel != "" {
		roleChain = append(roleChain, sessionModel)
		roleChain = append(roleChain, r.cfg.RoleChain("main")...)
	} else {
		roleChain = append(roleChain, r.cfg.RoleChain(role)...)
	}

	r.mu.Lock()
	stickyRef := r.sticky[role]
	r.mu.Unlock()

	seen := map[string]bool{}
	out := make([]string, 0, len(roleChain)+1)
	if stickyRef != "" && r.ModelExists(stickyRef) {
		out = append(out, stickyRef)
		seen[stickyRef] = true
	}
	for _, ref := range roleChain {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// ModelExists reports whether a ref is configured.
func (r *Runner) ModelExists(ref string) bool {
	_, ok := r.cfg.Model(ref)
	return ok
}

func (r *Runner) cooling(ref string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cool[ref]
	if !ok {
		return false
	}
	return r.now().Before(c.until)
}

func (r *Runner) noteFailure(ref string, pe *ProviderError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.cool[ref]
	if c == nil {
		c = &coolEntry{}
		r.cool[ref] = c
	}
	c.level++
	d := pe.RetryAfter
	if d <= 0 {
		d = time.Duration(r.cfg.Failover.CooldownSeconds) * time.Second
		for i := 1; i < c.level; i++ {
			d *= 2
			if d >= time.Duration(r.cfg.Failover.CooldownMaxSeconds)*time.Second {
				break
			}
		}
	}
	max := time.Duration(r.cfg.Failover.CooldownMaxSeconds) * time.Second
	if max > 0 && d > max {
		d = max
	}
	c.until = r.now().Add(d)
}

func (r *Runner) noteSuccess(role, ref string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cool, ref)
	r.sticky[role] = ref
}

// maxOutput bounds tokens by the model limit.
func (r *Runner) maxOutput(m config.Model, reqMax int) int {
	out := m.MaxOutput
	if reqMax > 0 && (out == 0 || reqMax < out) {
		out = reqMax
	}
	return out
}

func (r *Runner) recordUsage(sessionID string, m config.Model, u Usage) {
	if r.store == nil {
		return
	}
	cost := float64(u.Input)/1e6*m.Cost.Input +
		float64(u.Output)/1e6*m.Cost.Output +
		float64(u.CacheRead)/1e6*m.Cost.CacheRead +
		float64(u.CacheWrite)/1e6*m.Cost.CacheWrite
	_ = r.store.RecordUsage(&store.Usage{
		SessionID:  sessionID,
		Provider:   m.Provider,
		Model:      m.Model,
		Input:      u.Input,
		Output:     u.Output,
		CacheRead:  u.CacheRead,
		CacheWrite: u.CacheWrite,
		CostUSD:    cost,
	})
}

// Stream runs one request over the chain, emitting deltas.
func (r *Runner) Stream(ctx context.Context, role string, chain []string, req Request, emit func(Event) error) (Result, Outcome, error) {
	if len(chain) == 0 {
		return Result{}, Outcome{}, fmt.Errorf("provider: no models configured for role %s", role)
	}

	// Prefer non-cooling models; fall back to the full chain so a long
	// cooldown never dead-ends a request.
	ordered := make([]string, 0, len(chain))
	var cooled []string
	for _, ref := range chain {
		if r.cooling(ref) {
			cooled = append(cooled, ref)
			continue
		}
		ordered = append(ordered, ref)
	}
	if len(ordered) == 0 {
		ordered = append(ordered, cooled...)
	}

	maxAttempts := r.cfg.Failover.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 4
	}
	attempts := 0
	var lastErr error
	var notices []string
	out := Outcome{}

	for _, ref := range ordered {
		m, ok := r.cfg.Model(ref)
		if !ok {
			continue
		}
		client, err := r.reg.Client(m.Provider)
		if err != nil {
			lastErr = err
			continue
		}
		req2 := req
		req2.Model = m.Model
		req2.MaxTokens = r.maxOutput(m, req.MaxTokens)
		req2.MaxTokensField = m.MaxTokensField
		req2.Temperature = m.Temperature
		req2.TopP = m.TopP
		req2.Seed = m.Seed

		for retry := 0; ; retry++ {
			if attempts >= maxAttempts {
				break
			}
			attempts++
			emitted := false
			emitFn := func(e Event) error {
				emitted = true
				return emit(e)
			}
			res, serr := client.Stream(ctx, req2, emitFn)
			if serr == nil {
				r.noteSuccess(role, ref)
				r.recordUsage(req.SessionID, m, res.Usage)
				out.Provider = m.Provider
				out.Model = m.Model
				out.Ref = ref
				out.Notices = notices
				return res, out, nil
			}
			lastErr = serr
			pe, ok := AsProviderError(serr)
			if !ok {
				pe = &ProviderError{Class: ClassServer, Message: serr.Error()}
			}
			if pe.Class == ClassCanceled || ctx.Err() != nil {
				return Result{}, out, serr
			}
			r.noteFailure(ref, pe)

			if emitted {
				next := nextRef(ordered, ref)
				notices = append(notices, fmt.Sprintf("switch %s → %s (%s)", ref, next, pe.Class))
				if err := emit(Event{Type: EvTextStart, Reset: true}); err != nil {
					return Result{}, out, err
				}
				break
			}
			if pe.Retryable() && retry < 2 {
				waits := []time.Duration{time.Second, 3 * time.Second}
				select {
				case <-ctx.Done():
					return Result{}, out, &ProviderError{Class: ClassCanceled, Message: "canceled"}
				case <-time.After(waits[retry]):
				}
				continue
			}
			if pe.Class == ClassContextOverflow {
				if !hasLargerWindow(r.cfg, ordered, ref, m.ContextWindow) {
					out.Notices = notices
					return Result{}, out, serr
				}
			}
			break
		}
		if attempts >= maxAttempts {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("provider: no usable model in chain")
	}
	pe, ok := AsProviderError(lastErr)
	class := "server"
	if ok {
		class = string(pe.Class)
	}
	out.Notices = notices
	return Result{}, out, fmt.Errorf("provider: all models failed (%s)", class)
}

func nextRef(chain []string, ref string) string {
	for i, r := range chain {
		if r == ref && i+1 < len(chain) {
			return chain[i+1]
		}
	}
	return "none"
}

func hasLargerWindow(cfg *config.Config, chain []string, current string, window int) bool {
	seenCurrent := false
	for _, ref := range chain {
		if ref == current {
			seenCurrent = true
			continue
		}
		if !seenCurrent {
			continue
		}
		if m, ok := cfg.Model(ref); ok && m.ContextWindow > window {
			return true
		}
	}
	return false
}
