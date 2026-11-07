package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"path/filepath"
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
	// 2000 chars keeps a typical answer inside the five passive replies QQ
	// allows per inbound message.
	return channel.Caps{Edit: true, Stream: true, Markdown: a.Cfg.Markdown, MaxChars: 2000}
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

// randomSeq returns a fresh msg_seq for an active (non-reply) message.
func (a *Adapter) randomSeq() int {
	return int((time.Now().Unix()%100000000 ^ int64(rand.Uint32()&0xFFFF)) % 65536)
}

// PassiveReplyLimit is how many replies one inbound QQ message may receive.
const PassiveReplyLimit = 5

// nextSeq returns the next reply sequence for a passive message.
func (a *Adapter) nextSeq(msgID string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seqs[msgID]++
	if a.seqs[msgID] > PassiveReplyLimit {
		return 0, false
	}
	return a.seqs[msgID], true
}

// RemainingSlots reports how many passive replies the source message can still
// receive, so the daemon can budget progress updates against the answer.
func (a *Adapter) RemainingSlots(chatID, replyTo string) int {
	if replyTo == "" {
		return PassiveReplyLimit
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	left := PassiveReplyLimit - a.seqs[replyTo]
	if left < 0 {
		return 0
	}
	return left
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
	Attachments []struct {
		ContentType string `json:"content_type"`
		URL         string `json:"url"`
		Filename    string `json:"filename"`
		Size        int    `json:"size"`
	} `json:"attachments"`
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
	for _, att := range d.Attachments {
		if att.URL == "" {
			continue
		}
		media, err := a.fetchAttachment(ctx, att.URL, att.ContentType, att.Filename, att.Size)
		if err != nil {
			continue
		}
		ib.Attachments = append(ib.Attachments, media)
	}
	select {
	case in <- ib:
	case <-ctx.Done():
	}
	return nil
}

// fetchAttachment downloads a chat attachment with the bot token.
func (a *Adapter) fetchAttachment(ctx context.Context, url, contentType, filename string, size int) (channel.Attachment, error) {
	if size > channel.MaxInboundFileBytes {
		return channel.Attachment{}, fmt.Errorf("attachment too large: %d bytes", size)
	}
	token, err := a.Token.Get(ctx)
	if err != nil {
		return channel.Attachment{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return channel.Attachment{}, err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := a.Client.Do(req)
	if err != nil {
		return channel.Attachment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return channel.Attachment{}, fmt.Errorf("attachment fetch: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, channel.MaxInboundFileBytes+1))
	if err != nil {
		return channel.Attachment{}, err
	}
	if len(data) > channel.MaxInboundFileBytes {
		return channel.Attachment{}, fmt.Errorf("attachment too large")
	}
	mime := strings.TrimSpace(contentType)
	if mime == "" {
		mime = mimeByExt(filename)
	}
	kind := "file"
	switch {
	case strings.HasPrefix(mime, "image/"):
		kind = "image"
	case strings.HasPrefix(mime, "audio/"), mime == "voice":
		kind, mime = "audio", "audio/ogg"
	}
	if filename == "" {
		filename = "attachment"
	}
	return channel.Attachment{Kind: kind, Name: filename, Mime: mime, Data: data}, nil
}

func mimeByExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md":
		return "text/plain"
	case ".json":
		return "application/json"
	}
	return "application/octet-stream"
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
