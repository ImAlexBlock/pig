package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexblock/pig/internal/internalurl"
)

const sendSchema = `{
  "type": "object",
  "properties": {
    "path":    {"type": "string", "description": "File to deliver to the user's chat."},
    "caption": {"type": "string", "description": "Short note sent with the file."}
  },
  "required": ["path"],
  "additionalProperties": false
}`

// MaxSendFileBytes bounds files delivered to a chat.
const MaxSendFileBytes = 8 << 20

type sendTool struct{}

func (sendTool) Name() string { return "send" }
func (sendTool) Description() string {
	return "Deliver a file or image from disk to the user's chat (max 8 MB). Use it for reports, screenshots and generated artifacts."
}
func (sendTool) Schema() json.RawMessage { return json.RawMessage(sendSchema) }
func (sendTool) ReadOnly() bool          { return false }

type sendArgs struct {
	Path    string `json:"path"`
	Caption string `json:"caption"`
}

func (sendTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a sendArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("send: invalid arguments: " + err.Error()), nil
	}
	if strings.TrimSpace(a.Path) == "" {
		return Error("send: path is required"), nil
	}
	abs := a.Path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(env.CWD, abs)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return Error("send: file not found: " + a.Path), nil
	}
	if info.Size() > MaxSendFileBytes {
		return Error(fmt.Sprintf("send: file too large (%d bytes, max %d)", info.Size(), MaxSendFileBytes)), nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Error("send: " + err.Error()), nil
	}
	mime := internalurl.MimeByName(abs)
	text := fmt.Sprintf("sent %s (%d bytes)", filepath.Base(abs), len(data))
	if a.Caption != "" {
		text = a.Caption
	}
	return Result{
		Text:    text,
		Display: "send " + filepath.Base(abs),
		Files:   []SentFile{{Name: filepath.Base(abs), Mime: mime, Data: data}},
	}, nil
}
