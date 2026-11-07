package channel

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

// Pairing guards channel binding with a 6-digit code.
type Pairing struct {
	Channel string
	code    string

	mu       sync.Mutex
	attempts map[string][]time.Time
	blocked  map[string]time.Time
}

// NewPairing creates a pairing guard with a fresh code.
func NewPairing(channel string) *Pairing {
	return &Pairing{
		Channel:  channel,
		code:     newCode(),
		attempts: map[string][]time.Time{},
		blocked:  map[string]time.Time{},
	}
}

func newCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "000000"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// Code returns the current pairing code.
func (p *Pairing) Code() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code
}

// PairCommand reports whether text is a /pair command and returns its code.
func PairCommand(text string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "/pair") {
		return "", false
	}
	return fields[1], true
}

// Verify checks one pairing attempt. It returns ok=false with a reason when the
// chat is rate limited or the code is wrong.
func (p *Pairing) Verify(chatID, code string) (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	if until, ok := p.blocked[chatID]; ok {
		if now.Before(until) {
			return false, "too many attempts"
		}
		delete(p.blocked, chatID)
	}
	window := now.Add(-time.Minute)
	kept := p.attempts[chatID][:0]
	for _, t := range p.attempts[chatID] {
		if t.After(window) {
			kept = append(kept, t)
		}
	}
	p.attempts[chatID] = append(kept, now)
	if len(p.attempts[chatID]) > 5 {
		p.blocked[chatID] = now.Add(10 * time.Minute)
		delete(p.attempts, chatID)
		return false, "too many attempts"
	}
	if code != p.code {
		return false, "wrong code"
	}
	p.code = newCode()
	return true, ""
}
