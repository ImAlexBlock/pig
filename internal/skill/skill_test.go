package skill

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/tool"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDiscover(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(home, "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: Ship the branch\n---\n\nUse it.\n")
	writeFile(t, filepath.Join(cwd, ".pig", "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: shadowed\n---\nbody\n")
	writeFile(t, filepath.Join(home, "skills", "broken", "SKILL.md"), "no frontmatter\n")

	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	reg := Discover(cfg, home, cwd)

	sk, ok := reg.Get("deploy")
	if !ok {
		t.Fatal("deploy skill missing")
	}
	if sk.Description != "shadowed" {
		t.Fatalf("description = %q, want the cwd skill to win", sk.Description)
	}
	if _, ok := reg.Get("broken"); ok {
		t.Fatal("skill without a description was accepted")
	}
	if len(reg.Warnings()) == 0 {
		t.Fatal("missing description produced no warning")
	}
	if dir, ok := reg.Dir("deploy"); !ok || dir == "" {
		t.Fatal("Dir(deploy) failed")
	}
}

func TestHostToolCall(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	home := t.TempDir()
	dir := filepath.Join(home, "skills", "weather")
	writeFile(t, filepath.Join(dir, "SKILL.md"),
		"---\nname: weather\ndescription: Get weather\n---\nbody\n")
	writeFile(t, filepath.Join(dir, "tools.json"), `{
  "protocol": 1,
  "command": ["python3", "server.py"],
  "tools": [{"name":"weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]
}`)
	writeFile(t, filepath.Join(dir, "server.py"), `
import json, sys

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    msg = json.loads(line)
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"name":"weather","tools":[
            {"name":"weather","description":"Get weather","input_schema":{"type":"object"}}]}}), flush=True)
    elif method == "tools/call":
        city = msg["params"]["arguments"].get("city","?")
        print(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"content":[
            {"type":"text","text":"12C in " + city}],"isError":False}}), flush=True)
    elif method == "shutdown":
        sys.exit(0)
`)

	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	reg := Discover(cfg, home, home)
	sk, ok := reg.Get("weather")
	if !ok || sk.Manifest == nil {
		t.Fatal("weather skill with manifest missing")
	}
	host := NewHost(cfg, sk, home)
	defer host.Close()

	text, isErr, err := host.Call(context.Background(), "weather", json.RawMessage(`{"city":"Paris"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if isErr {
		t.Fatal("tool reported an error")
	}
	if text != "12C in Paris" {
		t.Fatalf("text = %q", text)
	}

	// The registry wrapper must expose the tool under <skill>__<tool>.
	regTool := tool.NewRegistry()
	reg.RegisterTools(regTool, cfg, home)
	if _, ok := regTool.Get("weather__weather"); !ok {
		t.Fatalf("registered tools = %v", regTool.Names())
	}
}

func TestHostMissingBinary(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "skills", "broken")
	writeFile(t, filepath.Join(dir, "SKILL.md"),
		"---\nname: broken\ndescription: fails to start\n---\nbody\n")
	writeFile(t, filepath.Join(dir, "tools.json"), `{"protocol":1,"command":["/nonexistent/binary"],"tools":[{"name":"x","description":"x","input_schema":{"type":"object"}}]}`)
	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	reg := Discover(cfg, home, home)
	sk, ok := reg.Get("broken")
	if !ok {
		t.Fatal("skill missing")
	}
	host := NewHost(cfg, sk, home)
	defer host.Close()
	if _, _, err := host.Call(context.Background(), "x", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected a start failure")
	}
}
