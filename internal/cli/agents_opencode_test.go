package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

func TestOpenCodeSetupPreservesJSONCAndInstructions(t *testing.T) {
	home := agentHome(t)
	configHome := filepath.Join(home, "custom config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	directory := filepath.Join(configHome, "opencode")
	path := filepath.Join(directory, "opencode.jsonc")
	original := []byte(`{
  // Keep my model and MCP settings.
  "model": "provider/model",
  "mcp": {
    "timeout": {"execution": 90000},
    "servers": {
      "other": {"type": "local", "command": ["other"],},
    },
  },
}`)
	if err := atomicWrite(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(directory, "AGENTS.md"), []byte("Keep my global instructions.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	output := run(t, home, "agents", "install", "--target", "opencode")
	if !strings.Contains(output, `"mcpCurrent": true`) {
		t.Fatal(output)
	}
	first, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(first, []byte("// Keep my model and MCP settings.")) {
		t.Fatalf("lost comments: %s %v", first, err)
	}
	value, err := hujson.Parse(bytes.Clone(first))
	if err != nil {
		t.Fatal(err)
	}
	value.Standardize()
	var config struct {
		Model string
		MCP   struct {
			Timeout map[string]int
			Servers map[string]struct {
				Type    string
				Command []string
			}
		}
	}
	if err := json.Unmarshal(value.Pack(), &config); err != nil {
		t.Fatal(err)
	}
	hex := config.MCP.Servers["hex"]
	if config.Model != "provider/model" || config.MCP.Timeout["execution"] != 90000 || config.MCP.Servers["other"].Type != "local" || hex.Type != "local" || len(hex.Command) != 3 || !filepath.IsAbs(hex.Command[0]) || hex.Command[1] != "mcp" || hex.Command[2] != "serve" {
		t.Fatalf("unexpected OpenCode V2 configuration: %s", first)
	}
	run(t, home, "agents", "install", "--target", "opencode")
	second, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("non-idempotent JSONC update: %s %v", second, err)
	}
	run(t, home, "agents", "refresh")
	instructions, err := os.ReadFile(filepath.Join(directory, "AGENTS.md"))
	if err != nil || !strings.HasPrefix(string(instructions), "Keep my global instructions.\n") || strings.Count(string(instructions), instructionStart) != 1 {
		t.Fatalf("lost global instructions: %s %v", instructions, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "opencode.json")); !os.IsNotExist(err) {
		t.Fatal("created a second configuration file")
	}
}

func TestOpenCodeProjectSetupAndConflict(t *testing.T) {
	agentHome(t)
	project := t.TempDir()
	path := filepath.Join(project, "opencode.json")
	if err := os.WriteFile(path, []byte(`{"model":"keep-this"}`), 0600); err != nil {
		t.Fatal(err)
	}
	run(t, project, "agents", "install", "--project", "--target", "opencode")
	for _, name := range []string{"AGENTS.md", ".opencode/skills/hex/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	for _, config := range []string{
		`{"mcp":{"servers":{"hex":{"type":"remote","url":"https://example.test"}}}}`,
		`{"mcp":{"hex":{"command":["custom-hex"]}}}`,
		`{"mcp":null}`,
		`{"mcp":{"servers":[]}}`,
		`{"invalid":`,
	} {
		if err := os.WriteFile(path, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		app, _ := testApp(t, project)
		if err := app.Execute(context.Background(), []string{"agents", "install", "--project", "--target", "opencode"}, "test"); err == nil {
			t.Fatalf("accepted conflicting or invalid configuration: %s", config)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != config {
			t.Fatalf("changed conflicting config: %s %v", data, err)
		}
	}
}
