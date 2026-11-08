package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ErrNotFound reports a missing config file.
var ErrNotFound = errors.New("config: file not found")

// Load reads and validates a config file. Missing keys take documented
// defaults. Unknown keys are collected in Config.Unknown and are not fatal.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("config: read: %v", err)
	}
	return Parse(b, path)
}

// Parse decodes config bytes. path is recorded for messages only.
func Parse(b []byte, path string) (*Config, error) {
	cfg := Default()
	if err := toml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("config: parse: %v", err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("config: parse: %v", err)
	}
	cfg.Unknown = unknownKeys(raw, reflect.TypeOf(Config{}), "")
	cfg.Path = path
	cfg.applyModelMeta()
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadOrInit loads the config. A missing file yields an in-memory default; it
// is only written once a command adds a provider and a model.
func LoadOrInit(path string) (*Config, error) {
	cfg, err := Load(path)
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	cfg = Default()
	cfg.Path = path
	cfg.Providers = map[string]Provider{}
	return cfg, nil
}

// MockConfig returns an in-memory config that talks to the mock provider.
func MockConfig(path string) *Config {
	c := Default()
	c.Path = path
	c.Providers = map[string]Provider{"mock": {Kind: "mock", BaseURL: "mock://", APIKeyEnv: ""}}
	c.Models = []Model{{
		Ref:           "mock/mock",
		Provider:      "mock",
		Model:         "mock",
		ContextWindow: 128000,
		MaxOutput:     4096,
		Input:         []string{"text"},
	}}
	c.Roles = Roles{Main: []string{"mock/mock"}, Compact: []string{"mock/mock"}, Subagent: []string{"mock/mock"}}
	return c
}

// --- unknown key detection -------------------------------------------------

func fieldType(t reflect.Type, name string) (reflect.Type, bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() == reflect.Map {
		// map[string]X: every key is allowed, recurse into X.
		return t.Elem(), true
	}
	if t.Kind() != reflect.Struct {
		return nil, false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" {
			continue
		}
		tag = strings.Split(tag, ",")[0]
		if tag == name {
			return f.Type, true
		}
	}
	return nil, false
}

func unknownKeys(m map[string]any, t reflect.Type, prefix string) []string {
	var out []string
	for k, v := range m {
		name := k
		if prefix != "" {
			name = prefix + "." + k
		}
		ft, ok := fieldType(t, k)
		if !ok {
			out = append(out, name)
			continue
		}
		out = append(out, unknownKeysValue(v, ft, name)...)
	}
	sort.Strings(out)
	return out
}

func unknownKeysValue(v any, t reflect.Type, name string) []string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch val := v.(type) {
	case map[string]any:
		switch t.Kind() {
		case reflect.Struct:
			return unknownKeys(val, t, name)
		case reflect.Map:
			var out []string
			for k, sub := range val {
				out = append(out, unknownKeysValue(sub, t.Elem(), name+"."+k)...)
			}
			sort.Strings(out)
			return out
		}
	case []any:
		if t.Kind() == reflect.Slice {
			var out []string
			for _, sub := range val {
				out = append(out, unknownKeysValue(sub, t.Elem(), name)...)
			}
			sort.Strings(out)
			return out
		}
	}
	return nil
}
