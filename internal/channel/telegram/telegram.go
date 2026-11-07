// Package telegram implements the Telegram long-polling channel.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/alexblock/pig/internal/channel"
)

// Adapter talks to the Telegram Bot API.
type Adapter struct {
	token  string
	base   string
	client *http.Client
}

// New returns a Telegram adapter.
func New(token string) *Adapter {
	return &Adapter{
		token:  token,
		base:   "https://api.telegram.org",
		client: &http.Client{Timeout: 70 * time.Second},
	}
}

func (a *Adapter) Name() string { return "telegram" }

func (a *Adapter) Caps() channel.Caps {
	return channel.Caps{Edit: true, Stream: true, Markdown: true, MaxChars: 4096}
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
		Chat      struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"message"`
}

func (a *Adapter) call(ctx context.Context, method string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	url := fmt.Sprintf("%s/bot%s/%s", a.base, a.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	if !env.OK {
		return fmt.Errorf("telegram: %s", env.Description)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// Run long-polls getUpdates until ctx is cancelled.
func (a *Adapter) Run(ctx context.Context, in chan<- channel.Inbound) error {
	var offset int64
	for {
		if ctx.Err() != nil {
			return nil
		}
		var updates []update
		payload := map[string]any{"timeout": 50, "offset": offset, "allowed_updates": []string{"message"}}
		err := a.call(ctx, "getUpdates", payload, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message == nil {
				continue
			}
			text := u.Message.Text
			if text == "" {
				continue
			}
			ib := channel.Inbound{
				Channel:   "telegram",
				ChatID:    strconv.FormatInt(u.Message.Chat.ID, 10),
				UserID:    strconv.FormatInt(u.Message.From.ID, 10),
				Text:      text,
				MessageID: strconv.FormatInt(u.Message.MessageID, 10),
				IsGroup:   u.Message.Chat.Type == "group" || u.Message.Chat.Type == "supergroup",
			}
			select {
			case in <- ib:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// Send posts a message, splitting long text.
func (a *Adapter) Send(ctx context.Context, o channel.Outbound) (channel.Handle, error) {
	h := channel.Handle{}
	for i, chunk := range channel.Split(o.Text, a.Caps().MaxChars) {
		payload := map[string]any{"chat_id": o.ChatID, "text": chunk}
		if o.Markdown {
			payload["text"] = channel.TelegramHTML(chunk)
			payload["parse_mode"] = "HTML"
		}
		if o.ReplyTo != "" && i == 0 {
			if id, err := strconv.ParseInt(o.ReplyTo, 10, 64); err == nil {
				payload["reply_to_message_id"] = id
			}
		}
		var res struct {
			MessageID int64 `json:"message_id"`
		}
		if err := a.call(ctx, "sendMessage", payload, &res); err != nil {
			return h, err
		}
		if i == 0 {
			h.ID = strconv.FormatInt(res.MessageID, 10)
		}
	}
	return h, nil
}

// Update edits a previously sent message.
func (a *Adapter) Update(ctx context.Context, h channel.Handle, o channel.Outbound) error {
	if h.ID == "" {
		return a.sendUpdateFallback(ctx, o)
	}
	payload := map[string]any{"chat_id": o.ChatID, "message_id": h.ID, "text": o.Text}
	if o.Markdown {
		payload["text"] = channel.TelegramHTML(o.Text)
		payload["parse_mode"] = "HTML"
	}
	err := a.call(ctx, "editMessageText", payload, nil)
	if err != nil {
		return err
	}
	return nil
}

func (a *Adapter) sendUpdateFallback(ctx context.Context, o channel.Outbound) error {
	_, err := a.Send(ctx, o)
	return err
}

// Typing sends the typing chat action.
func (a *Adapter) Typing(ctx context.Context, chatID string) error {
	return a.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil)
}

// Close is a no-op for long polling.
func (a *Adapter) Close() error { return nil }
