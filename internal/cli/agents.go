package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

const instructionStart = "<!-- hex:managed:start -->"
const instructionEnd = "<!-- hex:managed:end -->"

// Harnesses configured by --target auto. The first group is always set up,
// because people often install Hex before the harness. The desktop
// applications are set up only when their configuration directory exists on
// this machine, so an installation never seeds configuration for software the
// person does not use.
var (
	alwaysInstalledHarnesses = []string{"claude-code", "codex", "opencode"}
	detectedHarnesses        = []string{"claude-desktop", "cursor", "vscode", "windsurf", "gemini"}
	userOnlyHarnesses        = map[string]bool{"claude-desktop": true, "windsurf": true}
)

func hexDefaults() string {
	data, err := assets.ReadFile("assets/DEFAULTS.md")
	if err != nil {
		panic(err) // Embedded at build time.
	}
	return string(data)
}

func readOptionalFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// managedText preserves everything outside the explicitly Hex-owned block.
func managedText(original, start, end, content string) (string, error) {
	if strings.Count(original, start) > 1 || strings.Count(original, end) > 1 {
		return "", errors.New("multiple Hex managed blocks; resolve them before installing")
	}
	first, last := strings.Index(original, start), strings.Index(original, end)
	block := start + "\n" + strings.TrimSpace(content) + "\n" + end
	if first < 0 && last < 0 {
		if original != "" && !strings.HasSuffix(original, "\n") {
			original += "\n"
		}
		return original + "\n" + block + "\n", nil
	}
	if first < 0 || last < first {
		return "", errors.New("incomplete Hex managed block; resolve it before installing")
	}
	return original[:first] + block + original[last+len(end):], nil
}

func installInstructions(path, content string) error {
	data, err := readOptionalFile(path)
	if err != nil {
		return err
	}
	updated, err := managedText(string(data), instructionStart, instructionEnd, content)
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	return atomicWrite(path, []byte(updated), 0644)
}

func installProjectInstructions(directory string) error {
	content := "This is a Hex app. Load the Hex skill before working on it. " + hexDefaults()
	for _, path := range []string{"AGENTS.md", ".claude/rules/hex.md"} {
		if err := installInstructions(filepath.Join(directory, filepath.FromSlash(path)), content); err != nil {
			return err
		}
	}
	return nil
}

// agentInstallation describes one harness integration. Skill and
// Instructions are empty for harnesses without file-based guidance; their
// Current flags are then true because nothing can be stale.
type agentInstallation struct {
	Target       string `json:"target"`
	Skill        string `json:"skill,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	MCP          string `json:"mcp"`
	SkillCurrent bool   `json:"skillCurrent"`
	RulesCurrent bool   `json:"rulesCurrent"`
	MCPCurrent   bool   `json:"mcpCurrent"`
	MCPError     string `json:"mcpError,omitempty"`
}

// desktopConfigDirectory is where desktop applications such as Claude Desktop
// and VS Code keep per-user configuration on each operating system.
func desktopConfigDirectory(goos, home, application string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", application)
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, application)
		}
		return filepath.Join(home, "AppData", "Roaming", application)
	default:
		if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
			return filepath.Join(configHome, application)
		}
		return filepath.Join(home, ".config", application)
	}
}

func agentPaths(target, home, project string) (agentInstallation, error) {
	base := home
	if project != "" {
		base = project
	}
	result := agentInstallation{Target: target}
	switch target {
	case "claude-code":
		claude := filepath.Join(base, ".claude")
		if project == "" && os.Getenv("CLAUDE_CONFIG_DIR") != "" {
			claude = os.Getenv("CLAUDE_CONFIG_DIR")
		}
		result.Skill = filepath.Join(claude, "skills", "hex", "SKILL.md")
		result.Instructions = filepath.Join(claude, "rules", "hex.md")
		result.MCP = filepath.Join(base, ".mcp.json")
		if project == "" {
			result.MCP = filepath.Join(home, ".claude.json")
			if os.Getenv("CLAUDE_CONFIG_DIR") != "" {
				result.MCP = filepath.Join(claude, ".claude.json")
			}
		}
	case "codex":
		codex := filepath.Join(home, ".codex")
		if os.Getenv("CODEX_HOME") != "" {
			codex = os.Getenv("CODEX_HOME")
		}
		result.Skill = filepath.Join(base, ".agents", "skills", "hex", "SKILL.md")
		result.Instructions = filepath.Join(codex, "AGENTS.md")
		result.MCP = filepath.Join(codex, "config.toml")
		if project != "" {
			result.Instructions = filepath.Join(project, "AGENTS.md")
			result.MCP = filepath.Join(project, ".codex", "config.toml")
		}
	case "opencode":
		return openCodePaths(home, project)
	case "claude-desktop":
		if project != "" {
			return result, errors.New("claude-desktop setup is per user: Claude Desktop has no project configuration, so omit --project")
		}
		// Claude Desktop has no skill or instruction files. The MCP tool
		// descriptions and hex_get_docs carry the guidance instead.
		result.MCP = filepath.Join(desktopConfigDirectory(runtime.GOOS, home, "Claude"), "claude_desktop_config.json")
	case "cursor":
		if project != "" {
			result.Skill = filepath.Join(project, ".agents", "skills", "hex", "SKILL.md")
			result.Instructions = filepath.Join(project, ".cursor", "rules", "hex.mdc")
			result.MCP = filepath.Join(project, ".cursor", "mcp.json")
			break
		}
		// Cursor keeps user rules in its settings rather than a file, so the
		// user-level default relies on the skill's description to trigger.
		cursor := filepath.Join(home, ".cursor")
		result.Skill = filepath.Join(cursor, "skills", "hex", "SKILL.md")
		result.MCP = filepath.Join(cursor, "mcp.json")
	case "vscode":
		if project != "" {
			result.Skill = filepath.Join(project, ".agents", "skills", "hex", "SKILL.md")
			result.Instructions = filepath.Join(project, ".github", "copilot-instructions.md")
			result.MCP = filepath.Join(project, ".vscode", "mcp.json")
			break
		}
		user := filepath.Join(desktopConfigDirectory(runtime.GOOS, home, "Code"), "User")
		result.Skill = filepath.Join(home, ".copilot", "skills", "hex", "SKILL.md")
		result.Instructions = filepath.Join(user, "prompts", "hex.instructions.md")
		result.MCP = filepath.Join(user, "mcp.json")
	case "windsurf":
		if project != "" {
			return result, errors.New("windsurf setup is per user: Windsurf reads MCP servers from its user configuration only, so omit --project")
		}
		windsurf := filepath.Join(home, ".codeium", "windsurf")
		result.Skill = filepath.Join(windsurf, "skills", "hex", "SKILL.md")
		result.Instructions = filepath.Join(windsurf, "memories", "global_rules.md")
		result.MCP = filepath.Join(windsurf, "mcp_config.json")
	case "gemini":
		gemini := filepath.Join(home, ".gemini")
		result.Instructions = filepath.Join(gemini, "GEMINI.md")
		if project != "" {
			gemini = filepath.Join(project, ".gemini")
			result.Instructions = filepath.Join(project, "GEMINI.md")
		}
		result.Skill = filepath.Join(gemini, "skills", "hex", "SKILL.md")
		result.MCP = filepath.Join(gemini, "settings.json")
	default:
		known := strings.Join(slices.Concat(alwaysInstalledHarnesses, detectedHarnesses), ", ")
		return result, fmt.Errorf("unknown harness %q; use auto or one of %s", target, known)
	}
	return result, nil
}

// harnessInstalled reports whether a harness's user configuration directory
// exists, which is the evidence --target auto needs before configuring it.
func harnessInstalled(target, home string) (bool, error) {
	paths, err := agentPaths(target, home, "")
	if err != nil {
		return false, err
	}
	info, err := os.Stat(filepath.Dir(paths.MCP))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// autoTargets lists the harnesses --target auto configures in this scope,
// and the detected-only harnesses it skipped because they are not installed.
func autoTargets(home, project string) (targets, skipped []string, err error) {
	targets = slices.Clone(alwaysInstalledHarnesses)
	for _, name := range detectedHarnesses {
		if project != "" && userOnlyHarnesses[name] {
			continue
		}
		installed, err := harnessInstalled(name, home)
		if err != nil {
			return nil, nil, err
		}
		if installed {
			targets = append(targets, name)
		} else {
			skipped = append(skipped, name)
		}
	}
	return targets, skipped, nil
}

func agentMCPConfig(installation agentInstallation, executable string, original []byte) ([]byte, error) {
	if installation.Target == "opencode" {
		return openCodeMCPConfig(executable, original)
	}
	entry := map[string]any{"command": executable, "args": []string{"mcp", "serve"}}
	if installation.Target == "codex" {
		var config map[string]any
		if err := toml.Unmarshal(original, &config); err != nil {
			return nil, err
		}
		start, end := "# hex:managed:start", "# hex:managed:end"
		if servers, ok := config["mcp_servers"].(map[string]any); ok && servers["hex"] != nil && !strings.Contains(string(original), start) {
			return nil, errors.New("existing mcp_servers.hex is not Hex-managed; keep it or remove it before installing")
		}
		block, err := toml.Marshal(entry)
		if err != nil {
			return nil, err
		}
		updated, err := managedText(string(original), start, end, "[mcp_servers.hex]\n"+string(block))
		if err != nil {
			return nil, err
		}
		if err := toml.Unmarshal([]byte(updated), &config); err != nil {
			return nil, err
		}
		return []byte(updated), nil
	}
	config := map[string]any{}
	if len(bytes.TrimSpace(original)) > 0 {
		if err := json.Unmarshal(original, &config); err != nil {
			return nil, fmt.Errorf("configuration must be valid JSON (comments are not rewritten): %w", err)
		}
		if config == nil {
			return nil, errors.New("configuration must be a JSON object")
		}
	}
	key := "mcpServers"
	if installation.Target == "vscode" {
		key = "servers"
		entry["type"] = "stdio"
	}
	servers, ok := config[key].(map[string]any)
	if !ok {
		if config[key] != nil {
			return nil, fmt.Errorf("%s must be an object", key)
		}
		servers = map[string]any{}
		config[key] = servers
	}
	if existing, exists := servers["hex"]; exists {
		old, _ := json.Marshal(existing)
		want, _ := json.Marshal(entry)
		if !bytes.Equal(old, want) {
			return nil, errors.New("existing hex MCP entry differs; keep it or remove it before installing")
		}
	}
	servers["hex"] = entry
	data, err := json.MarshalIndent(config, "", "  ")
	return append(data, '\n'), err
}

func configureAgent(paths agentInstallation, executable string, skill []byte, install, useMCP bool) (agentInstallation, error) {
	configuration, err := readOptionalFile(paths.MCP)
	if err != nil {
		return paths, err
	}
	updated, configError := agentMCPConfig(paths, executable, configuration)
	if configError != nil {
		paths.MCPError = configError.Error()
	}
	if install {
		if useMCP && configError != nil {
			return paths, fmt.Errorf("%s: %w", paths.MCP, configError)
		}
		if err := installAgentGuidance(paths, skill); err != nil {
			return paths, err
		}
		if useMCP {
			if err := atomicWrite(paths.MCP, updated, 0600); err != nil {
				return paths, err
			}
			configuration = updated
		}
	}
	paths.SkillCurrent = true
	if paths.Skill != "" {
		installedSkill, err := readOptionalFile(paths.Skill)
		if err != nil {
			return paths, err
		}
		paths.SkillCurrent = bytes.Equal(installedSkill, skill)
	}
	paths.RulesCurrent = true
	if paths.Instructions != "" {
		instructions, err := readOptionalFile(paths.Instructions)
		if err != nil {
			return paths, err
		}
		paths.RulesCurrent = strings.Contains(string(instructions), strings.TrimSpace(hexDefaults()))
	}
	paths.MCPCurrent = configError == nil && bytes.Equal(configuration, updated)
	return paths, nil
}

// instructionHeader is the frontmatter some harnesses require at the very
// beginning of a rule file before the rule applies everywhere.
func instructionHeader(path string) string {
	switch {
	case strings.HasSuffix(path, ".mdc"):
		return "---\nalwaysApply: true\n---\n"
	case strings.HasSuffix(path, ".instructions.md"):
		return "---\napplyTo: \"**\"\n---\n"
	}
	return ""
}

func installAgentGuidance(paths agentInstallation, skill []byte) error {
	if paths.Skill != "" {
		if err := atomicWrite(paths.Skill, skill, 0644); err != nil {
			return err
		}
	}
	if paths.Instructions == "" {
		return nil
	}
	if header := instructionHeader(paths.Instructions); header != "" {
		existing, err := readOptionalFile(paths.Instructions)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			if err := atomicWrite(paths.Instructions, []byte(header), 0644); err != nil {
				return err
			}
		}
	}
	return installInstructions(paths.Instructions, hexDefaults())
}

// agentManaged reports whether Hex guidance was installed for this harness
// before, which is what refresh updates without adding MCP registrations.
func agentManaged(paths agentInstallation) (bool, error) {
	if paths.Instructions != "" {
		instructions, err := readOptionalFile(paths.Instructions)
		if err != nil {
			return false, err
		}
		return strings.Contains(string(instructions), instructionStart), nil
	}
	if paths.Skill == "" {
		return false, nil
	}
	_, err := os.Stat(paths.Skill)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (a *App) agentsCommand() *cobra.Command {
	root := &cobra.Command{Use: "agents", Short: "Install and diagnose Hex integrations for coding harnesses and desktop assistants"}
	for _, operation := range []string{"install", "status", "refresh"} {
		var target string
		var projectScope, useMCP bool
		command := &cobra.Command{
			Use: operation, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				executable, err := os.Executable()
				if err != nil {
					return err
				}
				project := ""
				if projectScope {
					project = a.Dir
				}
				targets := []string{target}
				var skipped []string
				if target == "auto" {
					targets, skipped, err = autoTargets(home, project)
					if err != nil {
						return err
					}
				}
				skill, err := assets.ReadFile("assets/SKILL.md")
				if err != nil {
					return err
				}
				var results []agentInstallation
				for _, name := range targets {
					paths, err := agentPaths(name, home, project)
					if err != nil {
						return err
					}
					if operation == "refresh" {
						managed, err := agentManaged(paths)
						if err != nil {
							return err
						}
						if !managed {
							continue
						}
					}
					paths, err = configureAgent(paths, executable, skill, operation != "status", useMCP && operation != "refresh")
					if err != nil {
						return err
					}
					results = append(results, paths)
				}
				return a.printJSON(struct {
					Integrations []agentInstallation `json:"integrations"`
					Skipped      []string            `json:"skipped,omitempty"`
					Next         string              `json:"next"`
				}{results, skipped, "Start a new harness session; restart desktop applications so they load the MCP server. Ask the assistant to sign into Hex when needed; complete company sign-in in your browser. The harness must provide file editing, build tools and a filesystem shared with Hex. Skipped harnesses are not installed on this machine; configure one anyway with --target."})
			},
		}
		command.Flags().StringVar(&target, "target", "auto", "Harness: auto (Claude Code, Codex and OpenCode, plus Claude Desktop, Cursor, VS Code, Windsurf and Gemini CLI when installed), claude-code, codex, opencode, claude-desktop, cursor, vscode, windsurf or gemini")
		command.Flags().BoolVar(&projectScope, "project", false, "Install in this project instead of user configuration (Claude Desktop and Windsurf are user-level only)")
		command.Flags().BoolVar(&useMCP, "mcp", true, "Register local MCP as well as skills and default instructions")
		root.AddCommand(command)
	}
	return root
}
