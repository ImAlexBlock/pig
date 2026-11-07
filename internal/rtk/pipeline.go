// Package rtk compresses bash output using an embedded rule table.
package rtk

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed rules.toml
var rulesTOML string

// Rule is one command match rule.
type Rule struct {
	ID      string `toml:"id"`
	Match   string `toml:"match"`
	Rewrite string `toml:"rewrite"`
	Filter  string `toml:"filter"`

	re *regexp.Regexp
}

type ruleFile struct {
	Rule []Rule `toml:"rule"`
}

var (
	rules         []Rule
	simpleCommand = regexp.MustCompile(`^[A-Za-z0-9_./-]+(\s+[^|;&><]*)?$`)
	ansiSeq       = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]|\\x1b\\][^\x07]*\x07")
)

func init() {
	rules = loadRules()
}

func loadRules() []Rule {
	var rf ruleFile
	if err := toml.Unmarshal([]byte(rulesTOML), &rf); err != nil {
		return nil
	}
	compiled := make([]Rule, 0, len(rf.Rule))
	for _, r := range rf.Rule {
		re, err := regexp.Compile(r.Match)
		if err != nil {
			continue
		}
		r.re = re
		compiled = append(compiled, r)
	}
	return compiled
}

// Rules returns the loaded rule table.
func Rules() []Rule { return rules }

// MatchRule returns the first rule matching a command.
func MatchRule(command string) (Rule, bool) {
	cmd := strings.TrimSpace(command)
	for _, r := range rules {
		if r.re != nil && r.re.MatchString(cmd) {
			return r, true
		}
	}
	return Rule{}, false
}

// Rewrite applies the rule rewrite when the command is simple (no pipes,
// redirects or command substitution).
func Rewrite(command string) (string, bool) {
	r, ok := MatchRule(command)
	if !ok || r.Rewrite == "" {
		return command, false
	}
	if !simpleCommand.MatchString(strings.TrimSpace(command)) {
		return command, false
	}
	return r.Rewrite, true
}

// StripANSI removes escape sequences.
func StripANSI(s string) string { return ansiSeq.ReplaceAllString(s, "") }

// clean normalises carriage returns and strips escape sequences.
func clean(s string) string {
	s = StripANSI(s)
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		ln = strings.TrimSuffix(ln, "\r")
		if idx := strings.LastIndex(ln, "\r"); idx >= 0 {
			ln = ln[idx+1:]
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n")
}

func tailLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// Apply filters stdout. ruleID is empty when nothing matched. The returned
// text includes the savings annotation. saved is the byte count removed.
func Apply(command, stdout string, exitCode int) (out string, ruleID string, saved int) {
	cleaned := clean(stdout)
	if exitCode != 0 {
		res := tailLines(cleaned, 50)
		return res, "", len(stdout) - len(res)
	}
	rule, ok := MatchRule(command)
	if !ok {
		return cleaned, "", len(stdout) - len(cleaned)
	}
	fn, ok := filters[rule.Filter]
	if !ok {
		return cleaned, rule.ID, len(stdout) - len(cleaned)
	}
	filtered := fn(cleaned)
	if filtered == "" {
		filtered = cleaned
	}
	saved = len(stdout) - len(filtered)
	if saved <= 0 {
		return filtered, rule.ID, 0
	}
	pct := saved * 100 / max(len(stdout), 1)
	annotated := fmt.Sprintf("%s\n[rtk %s saved %d bytes (%d%%)]", filtered, rule.ID, saved, pct)
	return annotated, rule.ID, saved
}
