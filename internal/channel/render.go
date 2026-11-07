package channel

import (
	"regexp"
	"strings"
)

var (
	reCodeBlock = regexp.MustCompile("(?s)```([a-zA-Z0-9]*)\\n?(.*?)```")
	reInline    = regexp.MustCompile("`([^`\n]+)`")
	reBold      = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reItalic    = regexp.MustCompile(`(^|[\s(])\*([^*\n]+)\*($|[\s).,!?])`)
	reLink      = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
	reHeading   = regexp.MustCompile(`(?m)^#{1,6}\s*`)
)

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// TelegramHTML converts markdown to the Telegram HTML subset.
func TelegramHTML(md string) string {
	var blocks []string
	out := reCodeBlock.ReplaceAllStringFunc(md, func(m string) string {
		sub := reCodeBlock.FindStringSubmatch(m)
		blocks = append(blocks, "<pre>"+escapeHTML(sub[2])+"</pre>")
		return "\x00PRE" + itoa(len(blocks)-1) + "\x00"
	})
	out = escapeHTML(out)
	out = reInline.ReplaceAllStringFunc(out, func(m string) string {
		sub := reInline.FindStringSubmatch(m)
		return "<code>" + sub[1] + "</code>"
	})
	out = reBold.ReplaceAllString(out, "<b>$1</b>")
	out = reItalic.ReplaceAllString(out, "$1<i>$2</i>$3")
	out = reLink.ReplaceAllString(out, `<a href="$2">$1</a>`)
	out = reHeading.ReplaceAllString(out, "")
	for i, b := range blocks {
		out = strings.ReplaceAll(out, "\x00PRE"+itoa(i)+"\x00", b)
	}
	return out
}

// PlainText strips markdown markers.
func PlainText(md string) string {
	out := reCodeBlock.ReplaceAllString(md, "$2")
	out = reInline.ReplaceAllString(out, "$1")
	out = reBold.ReplaceAllString(out, "$1")
	out = reItalic.ReplaceAllString(out, "$1$2$3")
	out = reLink.ReplaceAllString(out, "$1 ($2)")
	out = reHeading.ReplaceAllString(out, "")
	return out
}

// Split breaks text into chunks of at most max runes, preferring newlines.
func Split(text string, max int) []string {
	if max <= 0 || len(text) <= max {
		return []string{text}
	}
	var out []string
	rest := text
	for len(rest) > max {
		cut := max
		if idx := strings.LastIndex(rest[:max], "\n"); idx > max/2 {
			cut = idx + 1
		} else {
			for cut > 0 && !utf8Boundary(rest, cut) {
				cut--
			}
		}
		if cut <= 0 {
			cut = max
		}
		out = append(out, rest[:cut])
		rest = rest[cut:]
	}
	if rest != "" {
		out = append(out, rest)
	}
	return out
}

func utf8Boundary(s string, i int) bool {
	if i <= 0 || i >= len(s) {
		return true
	}
	return s[i]&0xC0 != 0x80
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
