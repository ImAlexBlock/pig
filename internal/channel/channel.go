// Package channel defines the messaging channel contract shared by Telegram
// and the QQ official bot.
package channel

import "context"

// Caps describes what a channel can do.
type Caps struct {
	Edit     bool
	Stream   bool
	Markdown bool
	MaxChars int
}

// Inbound is one received message.
type Inbound struct {
	Channel   string
	ChatID    string
	UserID    string
	Text      string
	MessageID string
	IsGroup   bool
}

// Outbound is one message to send.
type Outbound struct {
	ChatID   string
	Text     string
	ReplyTo  string
	Markdown bool
}

// Handle identifies a sent message so it can be edited later.
type Handle struct {
	ID string
}

// Adapter is a messaging channel.
type Adapter interface {
	Name() string
	Caps() Caps
	Run(ctx context.Context, in chan<- Inbound) error
	Send(ctx context.Context, o Outbound) (Handle, error)
	Update(ctx context.Context, h Handle, o Outbound) error
	Close() error
}
