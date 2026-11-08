package config

import "testing"

func TestLookupModelMeta(t *testing.T) {
	cases := []struct {
		id             string
		window, maxOut int
		input          string
	}{
		{"deepseek/deepseek-v4.1-flash", 1000000, 65536, "text"},
		{"claude-sonnet-4-5-20250929", 200000, 64000, "text,image"},
		{"gpt-4.1-mini", 1000000, 32768, "text,image"},
		{"openai/gpt-5", 400000, 128000, "text,image"},
		{"gemini-2.5-pro", 1048576, 65536, "text,image"},
		{"deepseek-chat", 128000, 8192, "text"},
	}
	for _, c := range cases {
		meta, ok := LookupModelMeta(c.id)
		if !ok {
			t.Fatalf("%s: not found", c.id)
		}
		if meta.ContextWindow != c.window || meta.MaxOutput != c.maxOut {
			t.Errorf("%s: got %d/%d, want %d/%d", c.id, meta.ContextWindow, meta.MaxOutput, c.window, c.maxOut)
		}
		if got := joinComma(meta.Input); got != c.input {
			t.Errorf("%s: input = %q, want %q", c.id, got, c.input)
		}
	}
	if _, ok := LookupModelMeta("relay/local-model-9000"); ok {
		t.Error("unknown model resolved")
	}
}

func joinComma(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// A relay entry only needs the standard model name: the rest is filled in.
func TestParseFillsModelMeta(t *testing.T) {
	src := `
version = 1
[providers.relay]
kind = "openai-chat"
base_url = "https://relay.example/v1"
api_key_env = "RELAY_KEY"
[[models]]
ref = "relay/gpt-4.1"
provider = "relay"
model = "gpt-4.1"
[roles]
main = ["relay/gpt-4.1"]
`
	cfg, err := Parse([]byte(src), "test.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	m := cfg.Models[0]
	if m.ContextWindow != 1000000 || m.MaxOutput != 32768 {
		t.Fatalf("catalog not applied: %+v", m)
	}
	if len(m.Input) != 2 {
		t.Fatalf("input = %v", m.Input)
	}

	// Explicit values win.
	src = `
version = 1
[providers.relay]
kind = "openai-chat"
base_url = "https://relay.example/v1"
api_key_env = "RELAY_KEY"
[[models]]
ref = "relay/gpt-4.1"
provider = "relay"
model = "gpt-4.1"
context_window = 64000
max_output = 4096
input = ["text"]
[roles]
main = ["relay/gpt-4.1"]
`
	cfg, err = Parse([]byte(src), "test.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	m = cfg.Models[0]
	if m.ContextWindow != 64000 || m.MaxOutput != 4096 || len(m.Input) != 1 {
		t.Fatalf("explicit values overwritten: %+v", m)
	}
}

// An unknown model without dimensions still fails validation: the catalog is a
// hint, not a guess.
func TestParseRejectsUnknownModelWithoutWindow(t *testing.T) {
	src := `
version = 1
[providers.relay]
kind = "openai-chat"
base_url = "https://relay.example/v1"
api_key_env = "RELAY_KEY"
[[models]]
ref = "relay/mystery"
provider = "relay"
model = "mystery"
[roles]
main = ["relay/mystery"]
`
	if _, err := Parse([]byte(src), "test.toml"); err == nil {
		t.Fatal("unknown model without a window accepted")
	}
}
