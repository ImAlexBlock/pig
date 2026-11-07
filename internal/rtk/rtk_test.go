package rtk

import (
	"os"
	"strings"
	"testing"
)

func TestGitStatusGolden(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/rtk/git-status.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	out, rule, saved := Apply("git status", string(raw), 0)
	if rule != "git-status" {
		t.Fatalf("rule = %q, want git-status", rule)
	}
	if saved <= 0 {
		t.Fatalf("saved = %d, want > 0", saved)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > 6 {
		t.Fatalf("output has %d lines, want <= 6:\n%s", len(lines), out)
	}
	for _, want := range []string{"M 3", "?? 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRewriteOnlySimpleCommands(t *testing.T) {
	if got, ok := Rewrite("git status"); !ok || got != "git status --porcelain=v1 --branch" {
		t.Fatalf("Rewrite(git status) = %q, %v", got, ok)
	}
	if got, ok := Rewrite("git status | head"); ok {
		t.Fatalf("Rewrite piped command = %q, %v; want unchanged", got, ok)
	}
	if _, ok := Rewrite("echo hi"); ok {
		t.Fatal("Rewrite(echo hi) matched a rule")
	}
}

func TestNonZeroExitKeepsTail(t *testing.T) {
	var input strings.Builder
	for i := 0; i < 80; i++ {
		input.WriteString("line\n")
	}
	input.WriteString("boom\n")
	out, rule, _ := Apply("git status", input.String(), 2)
	if rule != "" {
		t.Fatalf("rule = %q, want empty for failing command", rule)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("tail lost the last line:\n%s", out)
	}
	if strings.Count(out, "line") > 50 {
		t.Fatalf("tail kept %d lines, want <= 50", strings.Count(out, "line"))
	}
}

func TestStripANSI(t *testing.T) {
	if got := StripANSI("\x1b[31mred\x1b[0m"); got != "red" {
		t.Fatalf("StripANSI = %q", got)
	}
}

func TestProgressLines(t *testing.T) {
	out, _, _ := Apply("npm install", "10%\r20%\r100% done\n", 0)
	if strings.Contains(out, "\r") {
		t.Fatalf("carriage returns survived: %q", out)
	}
	if !strings.Contains(out, "100% done") {
		t.Fatalf("progress line lost: %q", out)
	}
}
