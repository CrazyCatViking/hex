package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/tailscale/hujson"
)

func openCodePaths(home, project string) (agentInstallation, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	directory := filepath.Join(configHome, "opencode")
	result := agentInstallation{Target: "opencode"}
	if project != "" {
		directory = filepath.Join(project, ".opencode")
		result.Instructions = filepath.Join(project, "AGENTS.md")
	} else {
		result.Instructions = filepath.Join(directory, "AGENTS.md")
	}
	result.Skill = filepath.Join(directory, "skills", "hex", "SKILL.md")
	// Prefer an existing config, including a project's direct-root config.
	// Multiple documents can override each other, so require an explicit merge
	// rather than reporting successful setup that another file could shadow.
	candidates := []string{filepath.Join(directory, "opencode.json"), filepath.Join(directory, "opencode.jsonc")}
	if project != "" {
		candidates = append(candidates, filepath.Join(project, "opencode.json"), filepath.Join(project, "opencode.jsonc"))
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return result, err
		}
		if result.MCP != "" {
			return result, fmt.Errorf("multiple OpenCode configuration files (%s and %s); consolidate this scope's config before installing Hex", result.MCP, path)
		}
		result.MCP = path
	}
	if result.MCP == "" {
		result.MCP = candidates[0]
	}
	return result, nil
}

// OpenCode V2 uses mcp.servers and command arrays. HuJSON lets us add that
// entry without discarding users' JSONC comments or unrelated settings.
func openCodeMCPConfig(executable string, original []byte) ([]byte, error) {
	input := original
	if len(bytes.TrimSpace(input)) == 0 {
		input = []byte(`{"$schema":"https://opencode.ai/config.json"}`)
	}
	document, err := hujson.Parse(bytes.Clone(input))
	if err != nil {
		return nil, fmt.Errorf("parse OpenCode configuration: %w", err)
	}
	if _, ok := document.Value.(*hujson.Object); !ok {
		return nil, errors.New("OpenCode configuration must be an object")
	}
	entry := map[string]any{"type": "local", "command": []string{executable, "mcp", "serve"}}
	for _, path := range []string{"/mcp", "/mcp/servers"} {
		value := document.Find(path)
		if value != nil {
			if _, ok := value.Value.(*hujson.Object); !ok {
				return nil, fmt.Errorf("OpenCode %s must be an object", path)
			}
			continue
		}
		if err := addOpenCodeValue(&document, path, map[string]any{}); err != nil {
			return nil, err
		}
	}
	if document.Find("/mcp/hex") != nil {
		return nil, errors.New("existing mcp.hex entry uses another configuration shape; configure Hex under OpenCode V2 mcp.servers.hex before retrying")
	}
	if existing := document.Find("/mcp/servers/hex"); existing != nil {
		standard := existing.Clone()
		standard.Standardize()
		var actual, expected any
		if err := json.Unmarshal(standard.Pack(), &actual); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &expected); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(actual, expected) {
			return nil, errors.New("existing OpenCode mcp.servers.hex entry differs; keep it or remove it before installing")
		}
		return original, nil
	}
	if err := addOpenCodeValue(&document, "/mcp/servers/hex", entry); err != nil {
		return nil, err
	}
	document.Format()
	return document.Pack(), nil
}

func addOpenCodeValue(document *hujson.Value, path string, value any) error {
	patch, err := json.Marshal([]map[string]any{{"op": "add", "path": path, "value": value}})
	if err != nil {
		return err
	}
	return document.Patch(patch)
}
