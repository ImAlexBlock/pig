package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Marshal encodes a config to TOML bytes.
func Marshal(c *Config) ([]byte, error) {
	b, err := toml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("config: encode: %v", err)
	}
	return b, nil
}

// Save writes the config atomically and leaves it mode 0444.
func Save(path string, c *Config) error {
	b, err := Marshal(c)
	if err != nil {
		return err
	}
	if c.Path == "" {
		c.Path = path
	}
	return writeAtomic(path, b, 0o444)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: write: %v", err)
	}
	if old, err := os.ReadFile(path); err == nil {
		_ = os.Chmod(path, 0o644)
		bak := path + ".bak"
		if err := os.WriteFile(bak, old, 0o600); err != nil {
			return fmt.Errorf("config: backup: %v", err)
		}
		_ = os.Chmod(bak, 0o600)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-config-*")
	if err != nil {
		return fmt.Errorf("config: write: %v", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: write: %v", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("config: write: %v", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: write: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("config: write: %v", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// --- raw document editing --------------------------------------------------

func rawDoc(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("config: read: %v", err)
	}
	root := map[string]any{}
	if len(strings.TrimSpace(string(b))) == 0 {
		return root, nil
	}
	if err := toml.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("config: parse: %v", err)
	}
	return root, nil
}

func isIndex(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.Atoi(s)
	return err == nil
}

func splitKey(key string) []string {
	parts := strings.Split(key, ".")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func lookupRaw(node any, parts []string) (any, bool) {
	cur := node
	for _, p := range parts {
		if isIndex(p) {
			s, ok := cur.([]any)
			if !ok {
				return nil, false
			}
			i, _ := strconv.Atoi(p)
			if i < 0 || i >= len(s) {
				return nil, false
			}
			cur = s[i]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func setRaw(node any, parts []string, v any) (any, error) {
	if len(parts) == 0 {
		return v, nil
	}
	name := parts[0]
	if isIndex(name) {
		s, ok := node.([]any)
		if !ok {
			return nil, errors.New("not an array")
		}
		i, _ := strconv.Atoi(name)
		if i < 0 || i >= len(s) {
			return nil, fmt.Errorf("index %d out of range", i)
		}
		nv, err := setRaw(s[i], parts[1:], v)
		if err != nil {
			return nil, err
		}
		s[i] = nv
		return s, nil
	}
	m, ok := node.(map[string]any)
	if !ok {
		if node == nil {
			m = map[string]any{}
		} else {
			return nil, fmt.Errorf("%s: not a table", name)
		}
	}
	if len(parts) == 1 {
		m[name] = v
		return m, nil
	}
	child, exists := m[name]
	if !exists {
		if isIndex(parts[1]) {
			child = []any{}
		} else {
			child = map[string]any{}
		}
	}
	nv, err := setRaw(child, parts[1:], v)
	if err != nil {
		return nil, err
	}
	m[name] = nv
	return m, nil
}

func schemaType(parts []string) (reflect.Type, bool) {
	t := reflect.TypeOf(Config{})
	for _, p := range parts {
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if isIndex(p) {
			if t.Kind() != reflect.Slice {
				return nil, false
			}
			t = t.Elem()
			continue
		}
		ft, ok := fieldType(t, p)
		if !ok {
			return nil, false
		}
		t = ft
	}
	return t, true
}

func typeName(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Uint, reflect.Uint64:
		return "integer"
	case reflect.Float64, reflect.Float32:
		return "float"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "table"
	}
	return "value"
}

func parseTOMLValue(raw string) (any, error) {
	doc := map[string]any{}
	if err := toml.Unmarshal([]byte("v = "+raw+"\n"), &doc); err != nil {
		return nil, err
	}
	return doc["v"], nil
}

func coerce(t reflect.Type, v any) (any, error) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32:
		switch n := v.(type) {
		case int64:
			return n, nil
		case float64:
			if n == float64(int64(n)) {
				return int64(n), nil
			}
		}
		return nil, fmt.Errorf("expected %s", typeName(t))
	case reflect.Float64, reflect.Float32:
		switch n := v.(type) {
		case float64:
			return n, nil
		case int64:
			return float64(n), nil
		}
		return nil, fmt.Errorf("expected %s", typeName(t))
	case reflect.String:
		if s, ok := v.(string); ok {
			return s, nil
		}
		return nil, fmt.Errorf("expected %s", typeName(t))
	case reflect.Bool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, fmt.Errorf("expected %s", typeName(t))
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("expected %s", typeName(t))
		}
		elem := t.Elem()
		out := make([]any, 0, len(arr))
		for _, item := range arr {
			cv, err := coerce(elem, item)
			if err != nil {
				return nil, err
			}
			out = append(out, cv)
		}
		return out, nil
	case reflect.Map, reflect.Struct:
		if _, ok := v.(map[string]any); ok {
			return v, nil
		}
		return nil, fmt.Errorf("expected %s", typeName(t))
	}
	return v, nil
}

// FormatValue renders a value the way it appears in TOML.
func FormatValue(v any) string {
	b, err := toml.Marshal(map[string]any{"v": v})
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := strings.TrimPrefix(string(b), "v = ")
	return strings.TrimSpace(s)
}

// Get returns the TOML text of one key.
func Get(path, key string) (string, error) {
	root, err := rawDoc(path)
	if err != nil {
		return "", err
	}
	v, ok := lookupRaw(root, splitKey(key))
	if !ok {
		return "", fmt.Errorf("config: %s: not set", key)
	}
	return FormatValue(v), nil
}

// Set writes one dotted key. It returns the value it stored.
func Set(path, key, raw string) (any, error) {
	if key == "version" {
		return nil, fmt.Errorf("config: version is read-only")
	}
	parts := splitKey(key)
	root, err := rawDoc(path)
	if err != nil {
		return nil, err
	}
	v, perr := parseTOMLValue(raw)
	if perr != nil {
		expected := "value"
		if t, ok := schemaType(parts); ok {
			expected = typeName(t)
		}
		return nil, fmt.Errorf("config: %s: expected %s", key, expected)
	}
	t, ok := schemaType(parts)
	if !ok {
		if prev, exists := lookupRaw(root, parts); exists {
			if _, structField := prev.(map[string]any); !structField {
				return v, applyRaw(path, root, parts, v)
			}
		}
		return nil, fmt.Errorf("config: %s: unknown key", key)
	}
	cv, cerr := coerce(t, v)
	if cerr != nil {
		return nil, fmt.Errorf("config: %s: %v", key, cerr)
	}
	if err := applyRaw(path, root, parts, cv); err != nil {
		return nil, err
	}
	return cv, nil
}

func applyRaw(path string, root map[string]any, parts []string, v any) error {
	out, err := setRaw(root, parts, v)
	if err != nil {
		return fmt.Errorf("config: %s: %v", strings.Join(parts, "."), err)
	}
	m, _ := out.(map[string]any)
	b, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("config: encode: %v", err)
	}
	if _, err := Parse(b, path); err != nil {
		return err
	}
	return writeAtomic(path, b, 0o444)
}

// Unset removes one dotted key.
func Unset(path, key string) error {
	if key == "version" {
		return fmt.Errorf("config: version is read-only")
	}
	parts := splitKey(key)
	root, err := rawDoc(path)
	if err != nil {
		return err
	}
	node, ok := lookupRaw(root, parts[:len(parts)-1])
	if !ok {
		return fmt.Errorf("config: %s: not set", key)
	}
	m, ok := node.(map[string]any)
	if !ok {
		return fmt.Errorf("config: %s: not a table", key)
	}
	last := parts[len(parts)-1]
	if _, ok := m[last]; !ok {
		return fmt.Errorf("config: %s: not set", key)
	}
	delete(m, last)
	b, err := toml.Marshal(root)
	if err != nil {
		return fmt.Errorf("config: encode: %v", err)
	}
	if _, err := Parse(b, path); err != nil {
		return err
	}
	return writeAtomic(path, b, 0o444)
}

// InitOptions describes `pig config init`.
type InitOptions struct {
	Provider      string
	Kind          string
	BaseURL       string
	APIKeyEnv     string
	Model         string
	Ref           string
	ContextWindow int
	MaxOutput     int
	Input         []string
}

// Init writes a fresh config with one provider and one model.
func Init(path string, opt InitOptions) (*Config, error) {
	c := Default()
	c.Providers = map[string]Provider{}
	c.Providers[opt.Provider] = Provider{Kind: opt.Kind, BaseURL: opt.BaseURL, APIKeyEnv: opt.APIKeyEnv}
	ref := opt.Ref
	if ref == "" {
		ref = opt.Provider + "/" + opt.Model
	}
	c.Models = []Model{{
		Ref:           ref,
		Provider:      opt.Provider,
		Model:         opt.Model,
		ContextWindow: opt.ContextWindow,
		MaxOutput:     opt.MaxOutput,
		Input:         opt.Input,
		Cache:         Cache{TTLSeconds: 300},
	}}
	c.Roles = Roles{Main: []string{ref}, Compact: []string{ref}, Subagent: []string{ref}}
	c.Path = path
	if err := Validate(c); err != nil {
		return nil, err
	}
	if err := Save(path, c); err != nil {
		return nil, err
	}
	return c, nil
}

// ModelSpec describes `pig models add`.
type ModelSpec struct {
	Ref           string
	Provider      string
	Model         string
	ContextWindow int
	MaxOutput     int
	Input         []string
	Roles         []string
}

// AddModel appends a [[models]] block and wires it into roles.
func AddModel(path string, spec ModelSpec) (*Config, error) {
	c, err := LoadOrInit(path)
	if err != nil {
		return nil, err
	}
	for _, m := range c.Models {
		if m.Ref == spec.Ref {
			return nil, fmt.Errorf("config: models.ref: duplicate %q", spec.Ref)
		}
	}
	if _, ok := c.Providers[spec.Provider]; !ok {
		return nil, fmt.Errorf("config: models.provider: unknown provider %q", spec.Provider)
	}
	c.Models = append(c.Models, Model{
		Ref:           spec.Ref,
		Provider:      spec.Provider,
		Model:         spec.Model,
		ContextWindow: spec.ContextWindow,
		MaxOutput:     spec.MaxOutput,
		Input:         spec.Input,
		Cache:         Cache{TTLSeconds: 300},
	})
	if len(c.Roles.Main) == 0 {
		c.Roles.Main = []string{spec.Ref}
	}
	roles := spec.Roles
	if len(roles) == 0 {
		roles = []string{"main"}
	}
	for _, r := range roles {
		switch r {
		case "main":
			if !contains(c.Roles.Main, spec.Ref) {
				c.Roles.Main = append(c.Roles.Main, spec.Ref)
			}
		case "compact":
			if !contains(c.Roles.Compact, spec.Ref) {
				c.Roles.Compact = append(c.Roles.Compact, spec.Ref)
			}
		case "subagent":
			if !contains(c.Roles.Subagent, spec.Ref) {
				c.Roles.Subagent = append(c.Roles.Subagent, spec.Ref)
			}
		default:
			return nil, fmt.Errorf("config: roles: unknown role %q", r)
		}
	}
	if err := Validate(c); err != nil {
		return nil, err
	}
	if err := Save(path, c); err != nil {
		return nil, err
	}
	return c, nil
}

// RemoveModel deletes a model and prunes it from every role chain.
func RemoveModel(path, ref string) (*Config, error) {
	c, err := LoadOrInit(path)
	if err != nil {
		return nil, err
	}
	filtered := c.Models[:0]
	found := false
	for _, m := range c.Models {
		if m.Ref == ref {
			found = true
			continue
		}
		filtered = append(filtered, m)
	}
	if !found {
		return nil, fmt.Errorf("config: models: unknown ref %q", ref)
	}
	c.Models = filtered
	prune := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, r := range in {
			if r != ref {
				out = append(out, r)
			}
		}
		return out
	}
	c.Roles.Main = prune(c.Roles.Main)
	c.Roles.Compact = prune(c.Roles.Compact)
	c.Roles.Subagent = prune(c.Roles.Subagent)
	if err := Validate(c); err != nil {
		return nil, err
	}
	if err := Save(path, c); err != nil {
		return nil, err
	}
	return c, nil
}

// SortedProviders returns provider names in stable order.
func (c *Config) SortedProviders() []string {
	names := make([]string, 0, len(c.Providers))
	for n := range c.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
