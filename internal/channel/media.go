package channel

import "context"

// File is an outbound attachment.
type File struct {
	Name string
	Mime string
	Data []byte
}

// Attachment is an inbound attachment received from a chat.
type Attachment struct {
	Kind string // "image" | "file" | "audio"
	Name string
	Mime string
	Data []byte
}

// MediaSender is implemented by channels that can deliver attachments.
// SendMedia delivers files inside the passive reply window of replyTo.
type MediaSender interface {
	SendMedia(ctx context.Context, chatID string, files []File, replyTo string) error
}

// Limits applied to inbound and outbound attachments.
const (
	MaxInboundFileBytes  = 8 << 20
	MaxOutboundFiles     = 4
	MaxOutboundFileBytes = 8 << 20
)
