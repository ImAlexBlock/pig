package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/alexblock/pig/internal/channel"
	"github.com/alexblock/pig/internal/config"
)

// Chat id prefixes distinguish direct and group conversations.
const (
	PrefixUser  = "u:"
	PrefixGroup = "g:"
)

// IntentGroupAndC2C is the intents bitmask for guild/C2C messages.
const IntentGroupAndC2C = 1 << 25

// Adapter runs the QQ bot gateway.
type Adapter struct {
	Cfg    config.QQChannel
	Token  *Token
	Client *http.Client

	mu    sync.Mutex
	seen  map[string]bool
	order []string
	seqs  map[string]int
}

// New returns a QQ adapter.
func New(cfg config.QQChannel, secret string) *Adapter {
	client := &http.Client{Timeout: 20 * time.Second}
	return &Adapter{
		Cfg:    cfg,
		Client: client,
		Token: &Token{
			APIBase:    cfg.APIBase,
			AppID:      cfg.AppID,
			AppSecret:  secret,
			HTTPClient: client,
		},
		seen:  map[string]bool{},
		order: nil,
		seqs:  map[string]int{},
	}
}

func (a *Adapter) Name() string { return "qq" }

func (a *Adapter) Caps() channel.Caps {
	return channel.Caps{Edit: true, Stream: true, Markdown: a.Cfg.Markdown, MaxChars: 1800}
}

func (a *Adapter) markSeen(id string) bool {
	if id == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen[id] {
		return false
	}
	a.seen[id] = true
	a.order = append(a.order, id)
	if len(a.order) > 1000 {
		old := a.order[0]
		a.order = a.order[1:]
		delete(a.seen, old)
	}
	return true
}

// nextSeq returns the next reply sequence for a passive message. QQ allows at
// most five passive replies per source message.
func (a *Adapter) nextSeq(msgID string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seqs[msgID]++
	if a.seqs[msgID] > 5 {
		return 0, false
	}
	return a.seqs[msgID], true
}

type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  int64           `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type helloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

type messageData struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Author  struct {
		UserOpenID   string `json:"user_openid"`
		MemberOpenID string `json:"member_openid"`
	} `json:"author"`
	GroupOpenID string `json:"group_openid"`
}

func (a *Adapter) gatewayURL(ctx context.Context) string {
	token, err := a.Token.Get(ctx)
	if err == nil {
		url := strings.TrimSuffix(a.Cfg.APIBase, "/") + "/gateway"
		req, rerr := newRequest(ctx, "GET", url, nil, token)
		if rerr == nil {
			resp, derr := a.Client.Do(req)
			if derr == nil {
				var out struct {
					URL string `json:"url"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&out)
				resp.Body.Close()
				if out.URL != "" {
					return out.URL
				}
			}
		}
	}
	return "wss://api.sgroup.qq.com/websocket"
}

// Run connects to the gateway and forwards messages until ctx is cancelled.
func (a *Adapter) Run(ctx context.Context, in chan<- channel.Inbound) error {
	var sessionID string
	var lastSeq int64
	for {
		if ctx.Err() != nil {
			return nil
		}
		url := a.gatewayURL(ctx)
		conn, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		sid, seq, err := a.session(ctx, conn, in, sessionID, lastSeq)
		conn.Close(websocket.StatusNormalClosure, "")
		if err != nil && ctx.Err() != nil {
			return nil
		}
		if sid != "" {
			sessionID = sid
		}
		if seq > 0 {
			lastSeq = seq
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func (a *Adapter) session(ctx context.Context, conn *websocket.Conn, in chan<- channel.Inbound, sessionID string, lastSeq int64) (string, int64, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Wait for Hello.
	_, data, err := conn.Read(sctx)
	if err != nil {
		return sessionID, lastSeq, err
	}
	var hello gatewayPayload
	if err := json.Unmarshal(data, &hello); err != nil {
		return sessionID, lastSeq, err
	}
	interval := 30000
	if hello.Op == 10 {
		var h helloData
		_ = json.Unmarshal(hello.D, &h)
		if h.HeartbeatInterval > 0 {
			interval = h.HeartbeatInterval
		}
	}

	token, err := a.Token.Get(ctx)
	if err != nil {
		return sessionID, lastSeq, err
	}
	if sessionID != "" {
		_ = writeJSON(sctx, conn, map[string]any{
			"op": 6, "d": map[string]any{"token": "QQBot " + token, "session_id": sessionID, "seq": lastSeq},
		})
	} else {
		_ = writeJSON(sctx, conn, map[string]any{
			"op": 2, "d": map[string]any{
				"token":      "QQBot " + token,
				"intents":    IntentGroupAndC2C,
				"shard":      []int{0, 1},
				"properties": map[string]any{},
			},
		})
	}

	heartbeat := time.NewTicker(time.Duration(interval) * time.Millisecond)
	defer heartbeat.Stop()

	type readResult struct {
		data []byte
		err  error
	}
	reads := make(chan readResult, 4)
	go func() {
		for {
			_, d, rerr := conn.Read(sctx)
			if rerr != nil {
				reads <- readResult{err: rerr}
				return
			}
			reads <- readResult{data: d}
		}
	}()

	for {
		select {
		case <-sctx.Done():
			return sessionID, lastSeq, sctx.Err()
		case <-heartbeat.C:
			_ = writeJSON(sctx, conn, map[string]any{"op": 1, "d": lastSeq})
		case r := <-reads:
			if r.err != nil {
				return sessionID, lastSeq, r.err
			}
			var p gatewayPayload
			if err := json.Unmarshal(r.data, &p); err != nil {
				continue
			}
			if p.S > 0 {
				lastSeq = p.S
			}
			switch p.Op {
			case 11:
				continue
			case 7:
				return sessionID, lastSeq, fmt.Errorf("gateway: reconnect requested")
			case 9:
				return "", 0, fmt.Errorf("gateway: invalid session")
			case 0:
				if err := a.handleEvent(sctx, p.T, p.D, in); err != nil {
					return sessionID, lastSeq, err
				}
			}
		}
	}
}

func (a *Adapter) handleEvent(ctx context.Context, typ string, raw json.RawMessage, in chan<- channel.Inbound) error {
	switch typ {
	case "C2C_MESSAGE_CREATE", "GROUP_AT_MESSAGE_CREATE", "GROUP_MESSAGE_CREATE":
	default:
		return nil
	}
	var d messageData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	if !a.markSeen(d.ID) {
		return nil
	}
	ib := channel.Inbound{Channel: "qq", Text: strings.TrimSpace(d.Content), MessageID: d.ID}
	if typ == "C2C_MESSAGE_CREATE" {
		ib.ChatID = PrefixUser + d.Author.UserOpenID
		ib.UserID = d.Author.UserOpenID
	} else {
		ib.ChatID = PrefixGroup + d.GroupOpenID
		ib.UserID = d.Author.MemberOpenID
		ib.IsGroup = true
	}
	select {
	case in <- ib:
	case <-ctx.Done():
	}
	return nil
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

func (a *Adapter) Close() error { return nil }
