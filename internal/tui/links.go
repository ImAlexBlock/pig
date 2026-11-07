package tui

import "fmt"

// Link renders an OSC 8 hyperlink.
func Link(url, display string) string {
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, display)
}

// FileLink renders a clickable file path.
func FileLink(path, display string) string {
	if display == "" {
		display = path
	}
	return Link("file://"+path, display)
}
