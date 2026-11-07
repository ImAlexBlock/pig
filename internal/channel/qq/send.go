package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alexblock/pig/internal/channel"
)

func newRequest(ctx context.Context, method, url string, body []byte, token string) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("X-Union-Appid", "")
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	return req, nil
}

func (a *Adapter) endpoint(chatID string) (string, bool) {
	switch {
	case strings.HasPrefix(chatID, PrefixUser):
		return "/v2/users/" + strings.TrimPrefix(chatID, PrefixUser) + "/messages", false
	case strings.HasPrefix(chatID, PrefixGroup):
		return "/v2/groups/" + strings.TrimPrefix(chatID, PrefixGroup) + "/messages", true
	}
	return "", false
}

func (a *Adapter) post(ctx context.Context, path string, payload any) (map[string]any, error) {
	token, err := a.Token.Get(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimSuffix(a.Cfg.APIBase, "/") + path
	req, err := newRequest(ctx, http.MethodPost, url, body, token)
	if err != nil {
		return nil, err
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	if resp.StatusCode == http.StatusTooManyRequests {
		time.Sleep(2 * time.Second)
		return out, fmt.Errorf("qq: rate limited")
	}
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("qq: status %d: %s", resp.StatusCode, string(data))
	}
	return out, nil
}

// Send posts a message reply.
func (a *Adapter) Send(ctx context.Context, o channel.Outbound) (channel.Handle, error) {
	path, isGroup := a.endpoint(o.ChatID)
	if path == "" {
		return channel.Handle{}, fmt.Errorf("qq: unsupported chat %q", o.ChatID)
	}
	h := channel.Handle{}
	for i, chunk := range channel.Split(o.Text, a.Caps().MaxChars) {
		payload := map[string]any{"content": chunk, "msg_type": 0}
		if o.Markdown && !isGroup {
			payload["markdown"] = map[string]any{"content": chunk}
			payload["msg_type"] = 2
		}
		if o.ReplyTo != "" {
			if seq, ok := a.nextSeq(o.ReplyTo); ok {
				payload["msg_id"] = o.ReplyTo
				payload["msg_seq"] = seq
			}
		}
		res, err := a.post(ctx, path, payload)
		if err != nil {
			return h, err
		}
		if i == 0 {
			if id, ok := res["id"].(string); ok {
				h.ID = id
			} else {
				h.ID = o.ReplyTo
			}
		}
	}
	return h, nil
}

// Update streams a replacement message for C2C chats.
func (a *Adapter) Update(ctx context.Context, h channel.Handle, o channel.Outbound) error {
	if strings.HasPrefix(o.ChatID, PrefixUser) {
		openid := strings.TrimPrefix(o.ChatID, PrefixUser)
		path := "/v2/users/" + openid + "/stream_messages"
		payload := map[string]any{
			"input_mode": "replace",
			"content":    o.Text,
		}
		if seq, ok := a.nextSeq(o.ReplyTo); ok {
			payload["msg_id"] = o.ReplyTo
			payload["msg_seq"] = seq
		}
		if _, err := a.post(ctx, path, payload); err == nil {
			return nil
		}
	}
	_, err := a.Send(ctx, o)
	return err
}
