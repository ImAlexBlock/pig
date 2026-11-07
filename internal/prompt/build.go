// Package prompt assembles the system prompt from templates, tool docs, skills
// and context files.
package prompt

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexblock/pig/internal/compact"
	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/store"
)

//go:embed system.md
var systemTemplate string

//go:embed subagent.md
var subagentTemplate string

// Limits for context file collection.
const (
	MaxContextFileBytes = 32768
	MaxContextTotal     = 65536
	MaxSkills           = 40
	MaxSystemTokens     = 8000
)

// SkillRef is one discovered skill advertised to the model.
type SkillRef struct {
	Name        string
	Description string
}

// ToolDoc is one tool line in the prompt.
type ToolDoc struct {
	Name        string
	Description string
}

// Input configures prompt assembly.
type Input struct {
	Config     *config.Config
	Session    *store.Session
	Skills     []SkillRef
	Tools      []ToolDoc
	Facts      []string
	Extra      string
	Depth      int
	Task       string
	SubContext string
}

// Build renders the system prompt and returns non-fatal notices.
func Build(in Input) (string, []string) {
	var notices []string
	toolLines := renderTools(in.Tools)
	skillLines := renderSkills(in.Skills, &notices)
	contextFiles := ""
	if cwd := sessionCWD(in.Session); cwd != "" {
		contextFiles = collectContextFiles(cwd, in.Config)
	}
	var out string
	if in.Depth > 0 {
		task := strings.TrimSpace(in.Task)
		if task == "" {
			task = "Continue the assigned task."
		}
		out = strings.NewReplacer(
			"{{TASK}}", task,
			"{{CONTEXT}}", strings.TrimSpace(in.SubContext),
			"{{TOOLS}}", toolLines,
		).Replace(subagentTemplate)
		return out, notices
	}

	// The rendered prompt must not contain session-specific text before the
	// context-files section: a single varying byte invalidates the provider
	// prompt cache for every later request. cwd is therefore constant prose
	// plus a `pwd` hint, never the path itself.
	out = strings.NewReplacer(
		"{{TOOLS}}", toolLines,
		"{{SKILLS}}", skillLines,
		"{{FACTS}}", factsOrNone(in.Facts),
		"{{CONTEXT}}", contextFiles,
	).Replace(systemTemplate)

	if extra := strings.TrimSpace(in.Extra); extra != "" {
		out += "\n# Additional instructions\n" + extra + "\n"
	}
	if compact.EstimateTokens(out) > MaxSystemTokens {
		trimmed := compact.EstimateTokens(out)
		out = trimToTokens(out, MaxSystemTokens)
		notices = append(notices, fmt.Sprintf("system prompt truncated (%d → %d tokens)", trimmed, MaxSystemTokens))
	}
	return out, notices
}

func sessionCWD(s *store.Session) string {
	if s == nil {
		return ""
	}
	return s.CWD
}

func renderTools(tools []ToolDoc) string {
	if len(tools) == 0 {
		return "(no tools)"
	}
	sorted := append([]ToolDoc{}, tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var sb strings.Builder
	for _, t := range sorted {
		fmt.Fprintf(&sb, "- %s: %s\n", t.Name, t.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderSkills(skills []SkillRef, notices *[]string) string {
	if len(skills) == 0 {
		return "(none)"
	}
	limit := MaxSkills
	if len(skills) > limit {
		*notices = append(*notices, fmt.Sprintf("skills list truncated (%d of %d shown)", limit, len(skills)))
	}
	var sb strings.Builder
	for i, s := range skills {
		if i >= limit {
			break
		}
		desc := s.Description
		if len(desc) > 160 {
			desc = desc[:160] + "…"
		}
		fmt.Fprintf(&sb, "- %s: %s (read skill://%s for details)\n", s.Name, desc, s.Name)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// factsOrNone renders remembered facts, or a placeholder.
func factsOrNone(facts []string) string {
	if out := renderFacts(facts); out != "" {
		return out
	}
	return "(none)"
}

// renderFacts renders remembered facts for the system prompt.
func renderFacts(facts []string) string {
	if len(facts) == 0 {
		return ""
	}
	var sb strings.Builder
	bytes := 0
	for _, f := range facts {
		if bytes+len(f) > 2048 {
			sb.WriteString("- …\n")
			break
		}
		sb.WriteString("- " + f + "\n")
		bytes += len(f) + 3
	}
	return strings.TrimRight(sb.String(), "\n")
}

// collectContextFiles gathers AGENTS.md from cwd up to $HOME (or root).
func collectContextFiles(cwd string, cfg *config.Config) string {
	names := []string{"AGENTS.md"}
	if cfg != nil && len(cfg.Agent.ContextFiles) > 0 {
		names = cfg.Agent.ContextFiles
	}
	home, _ := os.UserHomeDir()
	var collected []string
	total := 0
	dir := filepath.Clean(cwd)
	for {
		for _, name := range names {
			p := filepath.Join(dir, name)
			info, err := os.Stat(p)
			if err != nil || info.IsDir() {
				continue
			}
			if info.Size() > MaxContextFileBytes {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if total+len(b) > MaxContextTotal {
				continue
			}
			total += len(b)
			collected = append(collected, fmt.Sprintf("<file path=%q>\n%s\n</file>", p, strings.TrimRight(string(b), "\n")))
		}
		if dir == home || dir == "/" || dir == "." {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Far to near: collected is near-to-far, so reverse.
	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}
	if len(collected) == 0 {
		return "(none)"
	}
	return strings.Join(collected, "\n")
}

// trimToTokens cuts text to roughly n tokens, preferring a paragraph boundary.
func trimToTokens(s string, n int) string {
	runes := []rune(s)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if compact.EstimateTokens(string(runes[:mid])) <= n {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo >= len(runes) {
		return s
	}
	head := string(runes[:lo])
	if idx := strings.LastIndex(head, "\n\n"); idx > lo/2 {
		head = head[:idx]
	}
	return head + "\n\n[system prompt truncated]\n"
}
