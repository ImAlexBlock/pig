package config

import (
	"sort"
	"strings"
)

// ModelMeta is the metadata a model needs but a relay rarely states: the
// context window, the output ceiling and the accepted modalities.
//
// The table below is a built-in hint, not a catalogue. Any value written in
// config.toml wins; the table only fills the gaps, so pointing a relay at a
// model by its standard name is enough to get a usable context window.
type ModelMeta struct {
	ContextWindow int      `json:"context_window"`
	MaxOutput     int      `json:"max_output"`
	Input         []string `json:"input"`
}

type catalogEntry struct {
	prefix string
	meta   ModelMeta
}

// Entries match a lowercased model id from the left. Windows and output limits
// are the documented maxima for the family; treat them as starting points that
// `pig models set` can correct.
var modelCatalog = []catalogEntry{
	{"claude-sonnet-4", ModelMeta{ContextWindow: 200000, MaxOutput: 64000, Input: []string{"text", "image"}}},
	{"claude-opus-4", ModelMeta{ContextWindow: 200000, MaxOutput: 32000, Input: []string{"text", "image"}}},
	{"claude-3-7-sonnet", ModelMeta{ContextWindow: 200000, MaxOutput: 64000, Input: []string{"text", "image"}}},
	{"claude-3-5-sonnet", ModelMeta{ContextWindow: 200000, MaxOutput: 8192, Input: []string{"text", "image"}}},
	{"claude-3-5-haiku", ModelMeta{ContextWindow: 200000, MaxOutput: 8192, Input: []string{"text", "image"}}},
	{"gpt-5", ModelMeta{ContextWindow: 400000, MaxOutput: 128000, Input: []string{"text", "image"}}},
	{"gpt-4.1", ModelMeta{ContextWindow: 1000000, MaxOutput: 32768, Input: []string{"text", "image"}}},
	{"gpt-4o", ModelMeta{ContextWindow: 128000, MaxOutput: 16384, Input: []string{"text", "image"}}},
	{"o4-mini", ModelMeta{ContextWindow: 200000, MaxOutput: 100000, Input: []string{"text", "image"}}},
	{"o3", ModelMeta{ContextWindow: 200000, MaxOutput: 100000, Input: []string{"text", "image"}}},
	{"deepseek-v4", ModelMeta{ContextWindow: 1000000, MaxOutput: 65536, Input: []string{"text"}}},
	{"deepseek-reasoner", ModelMeta{ContextWindow: 128000, MaxOutput: 64000, Input: []string{"text"}}},
	{"deepseek-v3", ModelMeta{ContextWindow: 128000, MaxOutput: 8192, Input: []string{"text"}}},
	{"deepseek-chat", ModelMeta{ContextWindow: 128000, MaxOutput: 8192, Input: []string{"text"}}},
	{"gemini-2.5-pro", ModelMeta{ContextWindow: 1048576, MaxOutput: 65536, Input: []string{"text", "image"}}},
	{"gemini-2.5-flash", ModelMeta{ContextWindow: 1048576, MaxOutput: 65536, Input: []string{"text", "image"}}},
	{"qwen3", ModelMeta{ContextWindow: 131072, MaxOutput: 8192, Input: []string{"text"}}},
	{"kimi-k2", ModelMeta{ContextWindow: 131072, MaxOutput: 16384, Input: []string{"text"}}},
}

func init() {
	// Longest prefix first so "gpt-5-mini" never falls back to a shorter family.
	sort.SliceStable(modelCatalog, func(i, j int) bool {
		return len(modelCatalog[i].prefix) > len(modelCatalog[j].prefix)
	})
}

// ModelIDOf returns the bare model id of a ref such as "relay/deepseek-chat".
func ModelIDOf(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// LookupModelMeta resolves a model id or ref against the built-in table.
func LookupModelMeta(id string) (ModelMeta, bool) {
	name := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return ModelMeta{}, false
	}
	for _, e := range modelCatalog {
		if strings.HasPrefix(name, e.prefix) {
			meta := e.meta
			meta.Input = append([]string(nil), e.meta.Input...)
			return meta, true
		}
	}
	return ModelMeta{}, false
}

// MetaFromCatalog reports the built-in metadata for a model.
func (m Model) MetaFromCatalog() (ModelMeta, bool) {
	id := m.Model
	if id == "" {
		id = ModelIDOf(m.Ref)
	}
	return LookupModelMeta(id)
}

// applyModelMeta fills unset model fields from the built-in table. Explicit
// values in config.toml always win.
func (c *Config) applyModelMeta() {
	for i := range c.Models {
		meta, ok := c.Models[i].MetaFromCatalog()
		if !ok {
			continue
		}
		if c.Models[i].ContextWindow == 0 {
			c.Models[i].ContextWindow = meta.ContextWindow
		}
		if c.Models[i].MaxOutput == 0 {
			c.Models[i].MaxOutput = meta.MaxOutput
		}
		if len(c.Models[i].Input) == 0 {
			c.Models[i].Input = append([]string(nil), meta.Input...)
		}
	}
}
