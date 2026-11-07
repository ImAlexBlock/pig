package qq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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
	if os.Getenv("PIG_QQ_DEBUG") == "1" {
		body, _ := json.Marshal(payload)
		log.Printf("qq debug: POST %s req=%s resp=%d %s", path, truncate(body, 400), resp.StatusCode, truncate(data, 400))
	}
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
		// Mirrors nousresearch/hermes-agent's _build_text_body: msg_seq is
		// always present, markdown mode swaps content for a markdown block,
		// and text-mode replies carry message_reference next to msg_id.
		payload := map[string]any{"content": chunk, "msg_type": 0}
		if o.Markdown && !isGroup {
			payload["markdown"] = map[string]any{"content": chunk}
			payload["msg_type"] = 2
		}
		if o.ReplyTo != "" {
			seq, ok := a.nextSeq(o.ReplyTo)
			if !ok {
				return h, fmt.Errorf("qq: passive reply limit reached for %s", o.ReplyTo)
			}
			payload["msg_seq"] = seq
			payload["msg_id"] = o.ReplyTo
			if !o.Markdown {
				payload["message_reference"] = map[string]any{"message_id": o.ReplyTo}
			}
		} else {
			payload["msg_seq"] = a.randomSeq()
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

// Update streams a replacement message for C2C chats. Only markdown mode uses
// the streaming endpoint: a plain-text payload there is delivered as a message
// type the QQ client cannot render.
func (a *Adapter) Update(ctx context.Context, h channel.Handle, o channel.Outbound) error {
	if o.Markdown && strings.HasPrefix(o.ChatID, PrefixUser) {
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

// truncate shortens debug output.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// SendMedia uploads files and delivers them as native QQ media messages
// (msg_type 7). Each upload returns file_info, which the follow-up message
// references; both stay inside the passive reply window of replyTo.
func (a *Adapter) SendMedia(ctx context.Context, chatID string, files []channel.File, replyTo string) error {
	path, isGroup := a.endpoint(chatID)
	if path == "" {
		return fmt.Errorf("qq: unsupported chat %q", chatID)
	}
	filesPath := strings.TrimSuffix(path, "/messages") + "/files"
	for _, f := range files {
		fileType := 4
		if strings.HasPrefix(f.Mime, "image/") {
			fileType = 1
		}
		upload := map[string]any{
			"file_type":    fileType,
			"srv_send_msg": false,
			"file_data":    base64.StdEncoding.EncodeToString(f.Data),
		}
		if fileType == 4 && f.Name != "" {
			upload["file_name"] = f.Name
		}
		res, err := a.post(ctx, filesPath, upload)
		if err != nil {
			return err
		}
		fileInfo, _ := res["file_info"].(string)
		if fileInfo == "" {
			return fmt.Errorf("qq: upload returned no file_info")
		}
		msg := map[string]any{"msg_type": 7, "media": map[string]any{"file_info": fileInfo}}
		seq, ok := a.nextSeq(replyTo)
		if !ok {
			return fmt.Errorf("qq: passive reply limit reached for %s", replyTo)
		}
		msg["msg_id"] = replyTo
		msg["msg_seq"] = seq
		if !isGroup {
			msg["message_reference"] = map[string]any{"message_id": replyTo}
		}
		if _, err := a.post(ctx, path, msg); err != nil {
			return err
		}
	}
	return nil
}
