package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// ReadAuth parses auth.env into ordered key/value pairs. Malformed lines are
// skipped. Values are split on the first '='.
func ReadAuth(path string) (map[string]string, []string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil, nil
		}
		return nil, nil, fmt.Errorf("auth: read: %v", err)
	}
	vals := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		val := line[i+1:]
		vals[name] = val
	}
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	return vals, names, nil
}

// ApplyAuth loads auth.env into the process environment. Existing variables
// win. It returns the loaded names.
func ApplyAuth(path string) ([]string, error) {
	vals, names, err := ReadAuth(path)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if _, ok := os.LookupEnv(n); ok {
			continue
		}
		if err := os.Setenv(n, vals[n]); err != nil {
			return nil, fmt.Errorf("auth: env: %v", err)
		}
	}
	return names, nil
}

func writeAuth(path string, vals map[string]string) error {
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString(n)
		sb.WriteString("=")
		sb.WriteString(vals[n])
		sb.WriteString("\n")
	}
	return writeAtomic(path, []byte(sb.String()), 0o600)
}

// SetAuth stores one secret with mode 0600.
func SetAuth(path, name, value string) error {
	if name == "" || strings.ContainsAny(name, "=\n ") {
		return fmt.Errorf("auth: name: invalid %q", name)
	}
	vals, _, err := ReadAuth(path)
	if err != nil {
		return err
	}
	vals[name] = value
	if err := writeAuth(path, vals); err != nil {
		return err
	}
	return os.Setenv(name, value)
}

// RemoveAuth deletes one secret.
func RemoveAuth(path, name string) error {
	vals, _, err := ReadAuth(path)
	if err != nil {
		return err
	}
	if _, ok := vals[name]; !ok {
		return fmt.Errorf("auth: %s: not set", name)
	}
	delete(vals, name)
	return writeAuth(path, vals)
}

// Secrets returns the values currently held in auth.env plus the values of all
// api_key_env providers, for redaction.
func Secrets(path string, c *Config) []string {
	vals, _, err := ReadAuth(path)
	if err != nil {
		vals = map[string]string{}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(vals))
	add := func(v string) {
		if len(v) < 4 || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, v := range vals {
		add(v)
	}
	if c != nil {
		for _, p := range c.Providers {
			if p.APIKeyEnv == "" {
				continue
			}
			if v, ok := os.LookupEnv(p.APIKeyEnv); ok {
				add(v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// Redact replaces every occurrence of a secret with ***.
func Redact(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, "***")
	}
	return s
}
