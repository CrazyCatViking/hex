package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

const instructionStart = "<!-- hex:managed:start -->"
const instructionEnd = "<!-- hex:managed:end -->"

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

type agentInstallation struct {
	Target       string `json:"target"`
	Skill        string `json:"skill"`
	Instructions string `json:"instructions"`
	MCP          string `json:"mcp"`
	SkillCurrent bool   `json:"skillCurrent"`
	RulesCurrent bool   `json:"rulesCurrent"`
	MCPCurrent   bool   `json:"mcpCurrent"`
	MCPError     string `json:"mcpError,omitempty"`
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
	case "cursor", "vscode":
		if project == "" {
			return result, fmt.Errorf("%s setup requires --project; its Hex default is scoped to that project", target)
		}
		result.Skill = filepath.Join(base, ".agents", "skills", "hex", "SKILL.md")
		if target == "cursor" {
			result.Instructions = filepath.Join(base, ".cursor", "rules", "hex.mdc")
			result.MCP = filepath.Join(base, ".cursor", "mcp.json")
		} else {
			result.Instructions = filepath.Join(base, ".github", "copilot-instructions.md")
			result.MCP = filepath.Join(base, ".vscode", "mcp.json")
		}
	default:
		return result, fmt.Errorf("unknown harness %q; use claude-code, codex, opencode, cursor or vscode", target)
	}
	return result, nil
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
	installedSkill, err := readOptionalFile(paths.Skill)
	if err != nil {
		return paths, err
	}
	instructions, err := readOptionalFile(paths.Instructions)
	if err != nil {
		return paths, err
	}
	paths.SkillCurrent = bytes.Equal(installedSkill, skill)
	paths.RulesCurrent = strings.Contains(string(instructions), strings.TrimSpace(hexDefaults()))
	paths.MCPCurrent = configError == nil && bytes.Equal(configuration, updated)
	return paths, nil
}

func installAgentGuidance(paths agentInstallation, skill []byte) error {
	if err := atomicWrite(paths.Skill, skill, 0644); err != nil {
		return err
	}
	if paths.Target == "cursor" {
		// Cursor frontmatter must be at the beginning of its rule file.
		existing, err := readOptionalFile(paths.Instructions)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			if err := atomicWrite(paths.Instructions, []byte("---\nalwaysApply: true\n---\n"), 0644); err != nil {
				return err
			}
		}
	}
	return installInstructions(paths.Instructions, hexDefaults())
}

func (a *App) agentsCommand() *cobra.Command {
	root := &cobra.Command{Use: "agents", Short: "Install and diagnose Hex integrations for coding harnesses"}
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
				if target == "auto" {
					targets = []string{"claude-code", "codex", "opencode"}
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
						instructions, err := readOptionalFile(paths.Instructions)
						if err != nil {
							return err
						}
						if !strings.Contains(string(instructions), instructionStart) {
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
					Next         string              `json:"next"`
				}{results, "Start a new harness session. Ask the assistant to sign into Hex when needed; complete company sign-in in your browser. The harness must provide file editing, build tools and a filesystem shared with Hex."})
			},
		}
		command.Flags().StringVar(&target, "target", "auto", "Harness: auto (Claude Code, Codex and OpenCode), claude-code, codex, opencode, cursor or vscode")
		command.Flags().BoolVar(&projectScope, "project", false, "Install in this project instead of user configuration (required for Cursor and VS Code)")
		command.Flags().BoolVar(&useMCP, "mcp", true, "Register local MCP as well as skills and default instructions")
		root.AddCommand(command)
	}
	return root
}
