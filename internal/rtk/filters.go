package rtk

import (
	"fmt"
	"sort"
	"strings"
)

var filters = map[string]func(string) string{
	"git-status": filterGitStatus,
	"git-diff":   filterGitDiff,
	"git-log":    filterGitLog,
	"listing":    filterListing,
	"grep":       filterGrep,
	"go-test":    filterGoTest,
	"generic":    filterGeneric,
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimRight(ln, " \t\r")
		if ln == "" {
			continue
		}
		out = append(out, ln)
	}
	return out
}

func filterGitStatus(s string) string {
	lines := nonEmptyLines(s)
	var branch string
	counts := map[string]int{}
	files := map[string][]string{}
	var codeOrder []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "##") {
			branch = strings.TrimSpace(ln)
			continue
		}
		if len(ln) < 2 {
			continue
		}
		code := strings.TrimSpace(ln[:2])
		if code == "" {
			code = ln[:2]
		}
		if ln[0] == '?' {
			code = "??"
		}
		path := strings.TrimSpace(ln[2:])
		if _, seen := counts[code]; !seen {
			codeOrder = append(codeOrder, code)
		}
		counts[code]++
		if len(files[code]) < 20 {
			files[code] = append(files[code], path)
		}
	}
	sort.SliceStable(codeOrder, func(i, j int) bool {
		if counts[codeOrder[i]] != counts[codeOrder[j]] {
			return counts[codeOrder[i]] > counts[codeOrder[j]]
		}
		return codeOrder[i] < codeOrder[j]
	})
	var out []string
	if branch != "" {
		out = append(out, branch)
	}
	for _, code := range codeOrder {
		entries := files[code]
		line := fmt.Sprintf("%s %d", code, counts[code])
		if len(entries) > 0 {
			list := strings.Join(entries, ", ")
			if counts[code] > len(entries) {
				list += fmt.Sprintf(", …%d more", counts[code]-len(entries))
			}
			line += ": " + list
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func filterGitDiff(s string) string {
	lines := nonEmptyLines(s)
	var out []string
	var cur string
	var added, removed, hunks, changed int
	flush := func() {
		if cur == "" {
			return
		}
		header := fmt.Sprintf("%s (hunks %d, +%d -%d)", cur, hunks, added, removed)
		out = append(out, header)
		cur, added, removed, hunks, changed = "", 0, 0, 0, 0
	}
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			flush()
			parts := strings.Fields(ln)
			if len(parts) >= 4 {
				cur = strings.TrimPrefix(parts[3], "b/")
			} else {
				cur = "unknown"
			}
		case strings.HasPrefix(ln, "@@"):
			hunks++
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
		case strings.HasPrefix(ln, "+"):
			added++
			if changed < 40 {
				out = append(out, "  "+ln)
				changed++
			}
		case strings.HasPrefix(ln, "-"):
			removed++
			if changed < 40 {
				out = append(out, "  "+ln)
				changed++
			}
		}
	}
	flush()
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n")
}

func filterGitLog(s string) string {
	lines := nonEmptyLines(s)
	var out []string
	var sha string
	subjectDone := false
	for _, ln := range lines {
		if len(out) >= 20 {
			break
		}
		switch {
		case strings.HasPrefix(ln, "commit "):
			sha = strings.TrimPrefix(ln, "commit ")
			if len(sha) > 8 {
				sha = sha[:8]
			}
			subjectDone = false
		case strings.HasPrefix(ln, "Author:"), strings.HasPrefix(ln, "Date:"),
			strings.HasPrefix(ln, "Merge:"), strings.HasPrefix(ln, "    "):
			if sha != "" && !subjectDone && strings.HasPrefix(ln, "    ") {
				out = append(out, strings.TrimSpace(sha+" "+strings.TrimSpace(ln)))
				subjectDone = true
			}
		}
	}
	return strings.Join(out, "\n")
}

func filterListing(s string) string {
	lines := nonEmptyLines(s)
	seen := map[string]bool{}
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		name := strings.TrimSpace(ln)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= 200 {
			break
		}
	}
	return strings.Join(out, "\n")
}

func filterGrep(s string) string {
	lines := nonEmptyLines(s)
	if len(lines) <= 100 {
		return strings.Join(lines, "\n")
	}
	kept := append([]string{}, lines[:100]...)
	kept = append(kept, fmt.Sprintf("[…%d more matches…]", len(lines)-100))
	return strings.Join(kept, "\n")
}

func filterGoTest(s string) string {
	lines := nonEmptyLines(s)
	var out []string
	okPkgs := 0
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(trimmed, "ok "), strings.HasPrefix(trimmed, "ok\t"):
			okPkgs++
		case strings.HasPrefix(trimmed, "?"):
			continue
		default:
			out = append(out, ln)
		}
	}
	if okPkgs > 0 {
		out = append([]string{fmt.Sprintf("ok: %d packages", okPkgs)}, out...)
	}
	return strings.Join(out, "\n")
}

func filterGeneric(s string) string {
	lines := nonEmptyLines(s)
	counts := map[string]int{}
	var order []string
	for _, ln := range lines {
		if counts[ln] == 0 {
			order = append(order, ln)
		}
		counts[ln]++
	}
	var collapsed []string
	for _, ln := range order {
		if c := counts[ln]; c > 1 {
			collapsed = append(collapsed, fmt.Sprintf("%s (x%d)", ln, c))
			continue
		}
		collapsed = append(collapsed, ln)
	}
	if len(collapsed) <= 60 {
		return strings.Join(collapsed, "\n")
	}
	head := collapsed[:40]
	tail := collapsed[len(collapsed)-20:]
	joined := append(append([]string{}, head...), fmt.Sprintf("[…%d lines elided…]", len(collapsed)-60))
	joined = append(joined, tail...)
	return strings.Join(joined, "\n")
}
