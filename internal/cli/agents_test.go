package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func agentHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HEX_CONFIG_DIR", filepath.Join(home, "hex"))
	return home
}

func TestAgentInstallationPreservesConfigurationAndIsIdempotent(t *testing.T) {
	home := agentHome(t)
	original := `{"theme":"dark","mcpServers":{"other":{"command":"other"}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("# Keep my comment\nmodel = \"my-model\"\n[mcp_servers]\n[mcp_servers.other]\ncommand = \"other\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		output := run(t, home, "agents", "install")
		if strings.Count(output, `"mcpCurrent": true`) != 3 || strings.Count(output, `"rulesCurrent": true`) != 3 {
			t.Fatal(output)
		}
	}
	claude, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil || !strings.Contains(string(claude), `"other"`) || !strings.Contains(string(claude), `"dark"`) {
		t.Fatalf("lost Claude settings: %s %v", claude, err)
	}
	codex, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil || !strings.HasPrefix(string(codex), "# Keep my comment\nmodel = \"my-model\"\n") || strings.Count(string(codex), "[mcp_servers.hex]") != 1 {
		t.Fatalf("lost Codex settings: %s %v", codex, err)
	}
	output := run(t, home, "agents", "status")
	if strings.Count(output, `"skillCurrent": true`) != 3 {
		t.Fatal(output)
	}
}

func TestAgentInstallationRefusesConflictingMCP(t *testing.T) {
	home := agentHome(t)
	path := filepath.Join(home, ".claude.json")
	original := `{"mcpServers":{"hex":{"command":"user-owned-hex"}}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	app, _ := testApp(t, home)
	if err := app.Execute(context.Background(), []string{"agents", "install", "--target", "claude-code"}, "test"); err == nil {
		t.Fatal("replaced conflicting MCP entry")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatalf("modified user config: %s %v", data, err)
	}
}

func TestProjectGuidancePreservesUserInstructions(t *testing.T) {
	project := t.TempDir()
	original := "# Project rules\nKeep this instruction.\n"
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := installSkills(project); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(project, "AGENTS.md"))
	if err != nil || !strings.HasPrefix(string(data), original) || strings.Count(string(data), instructionStart) != 1 {
		t.Fatalf("project instructions: %s %v", data, err)
	}
	for _, target := range []string{"cursor", "vscode"} {
		run(t, project, "agents", "install", "--project", "--target", target)
	}
}
