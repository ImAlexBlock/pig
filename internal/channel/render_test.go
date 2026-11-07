package channel

import (
	"strings"
	"testing"
)

func TestTelegramHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**bold**", "<b>bold</b>"},
		{"`code`", "<code>code</code>"},
		{"[x](https://example.com)", `<a href="https://example.com">x</a>`},
		{"a & b", "a &amp; b"},
		{"<script>", "&lt;script&gt;"},
	}
	for _, c := range cases {
		if got := TelegramHTML(c.in); !strings.Contains(got, c.want) {
			t.Errorf("TelegramHTML(%q) = %q, want it to contain %q", c.in, got, c.want)
		}
	}
	got := TelegramHTML("```go\nif a < b {}\n```")
	if !strings.Contains(got, "<pre>") || !strings.Contains(got, "a &lt; b") {
		t.Fatalf("code block = %q", got)
	}
}

func TestPlainTextStripsMarkers(t *testing.T) {
	got := PlainText("**bold** and `code` and [x](https://e.com)")
	for _, bad := range []string{"**", "`"} {
		if strings.Contains(got, bad) {
			t.Fatalf("PlainText left %q in %q", bad, got)
		}
	}
	if !strings.Contains(got, "bold") || !strings.Contains(got, "https://e.com") {
		t.Fatalf("PlainText = %q", got)
	}
}

func TestSplitRespectsLimitAndUTF8(t *testing.T) {
	text := strings.Repeat("日本語テキスト", 40)
	parts := Split(text, 100)
	if len(parts) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(parts))
	}
	for _, p := range parts {
		if len(p) > 100 {
			t.Fatalf("chunk of %d bytes exceeds 100", len(p))
		}
		if strings.ContainsRune(p, '\uFFFD') {
			t.Fatalf("chunk split inside a rune: %q", p)
		}
	}
	if strings.Join(parts, "") != text {
		t.Fatal("chunks do not reassemble the original text")
	}
}

func TestPairing(t *testing.T) {
	p := NewPairing("telegram")
	code := p.Code()
	if len(code) != 6 {
		t.Fatalf("code = %q, want 6 digits", code)
	}
	if ok, reason := p.Verify("chat-1", "000000x"); ok {
		t.Fatalf("bad code accepted (%s)", reason)
	}
	if ok, reason := p.Verify("chat-1", code); !ok {
		t.Fatalf("good code rejected: %s", reason)
	}
	next := p.Code()
	if next == code {
		t.Fatal("code was not rotated after pairing")
	}
	// Five wrong attempts in a minute block the chat.
	for range 6 {
		p.Verify("chat-2", "999999")
	}
	if ok, reason := p.Verify("chat-2", next); ok || reason != "too many attempts" {
		t.Fatalf("rate limit not applied: ok=%v reason=%q", ok, reason)
	}
}

func TestPairCommand(t *testing.T) {
	if code, ok := PairCommand("/pair 123456"); !ok || code != "123456" {
		t.Fatalf("PairCommand = %q, %v", code, ok)
	}
	if _, ok := PairCommand("/pair"); ok {
		t.Fatal("PairCommand accepted a bare /pair")
	}
	if _, ok := PairCommand("hello"); ok {
		t.Fatal("PairCommand accepted a normal message")
	}
}
