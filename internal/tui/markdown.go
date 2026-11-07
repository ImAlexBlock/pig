package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// renderMarkdown renders markdown for a terminal width.
func renderMarkdown(md string, width int) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	if width < 20 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.TrimRight(out, "\n")
}
