// Package skill discovers SKILL.md directories and hosts their tool servers.
package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ImAlexBlock/pig/internal/config"
)

// ToolDef is one tool declared by a skill manifest.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Manifest is the tools.json file of a tool-providing skill.
type Manifest struct {
	Protocol int       `json:"protocol"`
	Command  []string  `json:"command"`
	Tools    []ToolDef `json:"tools"`
}

// Skill is one discovered skill.
type Skill struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Command     string    `json:"command,omitempty"`
	Dir         string    `json:"dir"`
	Body        string    `json:"-"`
	Tools       []ToolDef `json:"tools,omitempty"`
	Manifest    *Manifest `json:"-"`
}

// Registry holds the discovered skills.
type Registry struct {
	skills map[string]*Skill
	order  []string
	warns  []string
}

// Discover scans the standard skill locations. Earlier locations win.
func Discover(cfg *config.Config, home, cwd string) *Registry {
	r := &Registry{skills: map[string]*Skill{}}
	var roots []string
	if cwd != "" {
		roots = append(roots, filepath.Join(cwd, ".pig", "skills"))
	}
	roots = append(roots, filepath.Join(home, "skills"))
	if cfg != nil {
		roots = append(roots, cfg.Skills.Paths...)
	}
	for _, root := range roots {
		r.scan(root)
	}
	sort.Strings(r.order)
	return r
}

func (r *Registry) scan(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		path := filepath.Join(dir, "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sk, err := parseSkill(dir, string(data))
		if err != nil {
			r.warns = append(r.warns, fmt.Sprintf("skill %s: %v", dir, err))
			continue
		}
		if _, exists := r.skills[sk.Name]; exists {
			continue
		}
		r.skills[sk.Name] = sk
		r.order = append(r.order, sk.Name)
	}
}

func parseSkill(dir, data string) (*Skill, error) {
	meta, body := splitFrontmatter(data)
	sk := &Skill{
		Name:        meta["name"],
		Description: meta["description"],
		Command:     meta["command"],
		Dir:         dir,
		Body:        body,
	}
	if sk.Name == "" {
		sk.Name = filepath.Base(dir)
	}
	if sk.Description == "" {
		return nil, fmt.Errorf("missing description")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "tools.json")); err == nil {
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("tools.json: %v", err)
		}
		if len(m.Tools) > 0 {
			sk.Manifest = &m
			sk.Tools = m.Tools
		}
	}
	return sk, nil
}

// splitFrontmatter parses a scalar-only YAML frontmatter block.
func splitFrontmatter(data string) (map[string]string, string) {
	meta := map[string]string{}
	if !strings.HasPrefix(data, "---\n") && !strings.HasPrefix(data, "---\r\n") {
		return meta, data
	}
	rest := data[strings.Index(data, "\n")+1:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return meta, data
	}
	block := rest[:idx]
	body := rest[idx+len("\n---"):]
	body = strings.TrimPrefix(body, "\r")
	body = strings.TrimPrefix(body, "\n")
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		val = strings.Trim(val, `"'`)
		meta[key] = val
	}
	return meta, body
}

// Get returns a skill by name.
func (r *Registry) Get(name string) (*Skill, bool) {
	s, ok := r.skills[name]
	return s, ok
}

// Dir returns the directory of a skill.
func (r *Registry) Dir(name string) (string, bool) {
	s, ok := r.skills[name]
	if !ok {
		return "", false
	}
	return s.Dir, true
}

// List returns skills in name order.
func (r *Registry) List() []*Skill {
	out := make([]*Skill, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.skills[n])
	}
	return out
}

// Warnings returns discovery problems.
func (r *Registry) Warnings() []string { return r.warns }
