package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopConfigDirectoryFollowsOperatingSystem(t *testing.T) {
	home := agentHome(t)
	t.Setenv("APPDATA", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	cases := map[string]string{
		"darwin":  filepath.Join(home, "Library", "Application Support", "Claude"),
		"windows": filepath.Join(home, "AppData", "Roaming", "Claude"),
		"linux":   filepath.Join(home, ".config", "Claude"),
	}
	for goos, want := range cases {
		if got := desktopConfigDirectory(goos, home, "Claude"); got != want {
			t.Fatalf("%s: %s, want %s", goos, got, want)
		}
	}
	t.Setenv("APPDATA", filepath.Join(home, "roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if got := desktopConfigDirectory("windows", home, "Code"); got != filepath.Join(home, "roaming", "Code") {
		t.Fatal(got)
	}
	if got := desktopConfigDirectory("linux", home, "Code"); got != filepath.Join(home, "xdg", "Code") {
		t.Fatal(got)
	}
}

// desktopHomes creates the configuration directories that make auto detect
// every desktop harness, and returns the Claude Desktop configuration path.
func desktopHomes(t *testing.T, home string) agentInstallation {
	t.Helper()
	for _, target := range detectedHarnesses {
		paths, err := agentPaths(target, home, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(paths.MCP), 0755); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := agentPaths("claude-desktop", home, "")
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestAutoSkipsDesktopHarnessesThatAreNotInstalled(t *testing.T) {
	home := agentHome(t)
	output := run(t, home, "agents", "install")
	var result struct {
		Integrations []agentInstallation
		Skipped      []string
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Integrations) != 3 || strings.Join(result.Skipped, ",") != "claude-desktop,cursor,vscode,windsurf,gemini" {
		t.Fatal(output)
	}
	for _, target := range detectedHarnesses {
		paths, err := agentPaths(target, home, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(paths.MCP); !os.IsNotExist(err) {
			t.Fatalf("configured %s although it is not installed", target)
		}
	}
}

func TestAutoConfiguresInstalledDesktopHarnesses(t *testing.T) {
	home := agentHome(t)
	claude := desktopHomes(t, home)
	original := `{"preferences":{"theme":"dark"},"mcpServers":{"other":{"command":"other"}}}`
	if err := os.WriteFile(claude.MCP, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	var first string
	for range 2 {
		first = run(t, home, "agents", "install")
		if strings.Contains(first, `"skipped"`) || strings.Count(first, `"mcpCurrent": true`) != 8 || strings.Count(first, `"skillCurrent": true`) != 8 || strings.Count(first, `"rulesCurrent": true`) != 8 {
			t.Fatal(first)
		}
	}

	data, err := os.ReadFile(claude.MCP)
	if err != nil {
		t.Fatal(err)
	}
	var desktop struct {
		Preferences map[string]string
		MCPServers  map[string]struct {
			Command string
			Args    []string
		}
	}
	if err := json.Unmarshal(data, &desktop); err != nil {
		t.Fatal(err)
	}
	hex := desktop.MCPServers["hex"]
	if desktop.Preferences["theme"] != "dark" || desktop.MCPServers["other"].Command != "other" || !filepath.IsAbs(hex.Command) || strings.Join(hex.Args, " ") != "mcp serve" {
		t.Fatalf("Claude Desktop configuration: %s", data)
	}
	if claude.Skill != "" || claude.Instructions != "" {
		t.Fatalf("Claude Desktop has no guidance files: %+v", claude)
	}

	vscode, err := agentPaths("vscode", home, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(vscode.MCP)
	if err != nil || !strings.Contains(string(data), `"servers"`) || !strings.Contains(string(data), `"type": "stdio"`) {
		t.Fatalf("VS Code user MCP configuration: %s %v", data, err)
	}
	instructions, err := os.ReadFile(vscode.Instructions)
	if err != nil || !strings.HasPrefix(string(instructions), "---\napplyTo: \"**\"\n---\n") || strings.Count(string(instructions), instructionStart) != 1 {
		t.Fatalf("VS Code user instructions: %s %v", instructions, err)
	}
	if filepath.Base(filepath.Dir(filepath.Dir(vscode.Skill))) != "skills" || !strings.Contains(vscode.Skill, ".copilot") {
		t.Fatal(vscode.Skill)
	}

	cursor, err := agentPaths("cursor", home, "")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Instructions != "" {
		t.Fatalf("Cursor keeps user rules in settings, not %s", cursor.Instructions)
	}
	for _, path := range []string{cursor.Skill, cursor.MCP} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}

	for _, target := range []string{"windsurf", "gemini"} {
		paths, err := agentPaths(target, home, "")
		if err != nil {
			t.Fatal(err)
		}
		instructions, err := os.ReadFile(paths.Instructions)
		if err != nil || !strings.Contains(string(instructions), strings.TrimSpace(hexDefaults())) {
			t.Fatalf("%s instructions: %s %v", target, instructions, err)
		}
		data, err := os.ReadFile(paths.MCP)
		if err != nil || !strings.Contains(string(data), `"mcpServers"`) {
			t.Fatalf("%s MCP configuration: %s %v", target, data, err)
		}
		if _, err := os.Stat(paths.Skill); err != nil {
			t.Fatal(err)
		}
	}

	status := run(t, home, "agents", "status")
	if strings.Count(status, `"mcpCurrent": true`) != 8 {
		t.Fatal(status)
	}
}

func TestRefreshUpdatesSkillOnlyHarnessesAndSkipsUnmanagedOnes(t *testing.T) {
	home := agentHome(t)
	desktopHomes(t, home)
	run(t, home, "agents", "install", "--target", "cursor")
	cursor, err := agentPaths("cursor", home, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cursor.Skill, []byte("old skill"), 0644); err != nil {
		t.Fatal(err)
	}
	output := run(t, home, "agents", "refresh")
	if !strings.Contains(output, `"target": "cursor"`) || strings.Contains(output, `"target": "windsurf"`) || strings.Contains(output, `"target": "claude-desktop"`) {
		t.Fatal(output)
	}
	skill, err := os.ReadFile(cursor.Skill)
	if err != nil || string(skill) == "old skill" {
		t.Fatalf("refresh left a stale skill: %v", err)
	}
	windsurf, err := agentPaths("windsurf", home, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(windsurf.MCP); !os.IsNotExist(err) {
		t.Fatal("refresh registered an MCP server for an unmanaged harness")
	}
}

func TestUserOnlyHarnessesRejectProjectScope(t *testing.T) {
	agentHome(t)
	project := t.TempDir()
	for _, target := range []string{"claude-desktop", "windsurf"} {
		app, _ := testApp(t, project)
		err := app.Execute(context.Background(), []string{"agents", "install", "--project", "--target", target}, "test")
		if err == nil || !strings.Contains(err.Error(), "omit --project") {
			t.Fatalf("%s: %v", target, err)
		}
	}
	run(t, project, "agents", "install", "--project", "--target", "gemini")
	for _, name := range []string{"GEMINI.md", ".gemini/settings.json", ".gemini/skills/hex/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	output := run(t, project, "agents", "install", "--project")
	if strings.Contains(output, "claude-desktop") || strings.Contains(output, "windsurf") {
		t.Fatalf("project auto must not mention user-only harnesses: %s", output)
	}
}
