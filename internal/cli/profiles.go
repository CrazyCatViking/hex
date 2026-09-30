package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type profiles struct {
	Version        int                        `json:"version"`
	DefaultProfile string                     `json:"defaultProfile,omitempty"`
	Profiles       map[string]json.RawMessage `json:"profiles"`
}

func readProfiles() (profiles, string, error) {
	store := profiles{Version: 1, Profiles: make(map[string]json.RawMessage)}
	directory, err := profileDirectory()
	if err != nil {
		return store, "", err
	}
	path := filepath.Join(directory, "profiles.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return store, path, nil
	}
	if err != nil {
		return store, path, fmt.Errorf("read Hex profiles: %w", err)
	}
	var decoded profiles
	if err := decodeStrict(data, &decoded); err != nil || decoded.Version != 1 || decoded.Profiles == nil {
		return store, path, errors.New("invalid Hex profile store")
	}
	changed, err := migrateProfileAuth(&decoded)
	if err != nil {
		return store, path, fmt.Errorf("migrate Hex profiles: %w", err)
	}
	if changed {
		if err := writeJSONFile(path, decoded); err != nil {
			return store, path, fmt.Errorf("save migrated Hex profiles: %w", err)
		}
	}
	return decoded, path, nil
}

func loadProfile(name string, optional bool) (*Connection, string, error) {
	store, _, err := readProfiles()
	if err != nil {
		return nil, "", err
	}
	if name == "" {
		name = store.DefaultProfile
	}
	if name == "" && optional {
		return nil, "", nil
	}
	if name == "" {
		return nil, "", errors.New("no platform configured; run hex setup first")
	}
	if !profileNamePattern.MatchString(name) {
		return nil, "", errors.New("invalid profile name")
	}
	data, ok := store.Profiles[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown profile %q; run hex setup to configure it", name)
	}
	connection, err := parseConnection(data, "")
	return &connection, name, err
}

func saveProfile(connection Connection, name string) (string, error) {
	connection, _ = migrateConnectionAuth(connection)
	if name == "" {
		server, err := url.Parse(connection.Server)
		if err != nil {
			return "", err
		}
		name = strings.Trim(strings.ReplaceAll(server.Hostname(), ":", "-"), "-")
		if server.Port() != "" {
			name += "-" + server.Port()
		}
	}
	if !profileNamePattern.MatchString(name) {
		return "", errors.New("profile names must use lowercase letters, digits, dots or hyphens")
	}
	store, path, err := readProfiles()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(connection)
	if err != nil {
		return "", err
	}
	store.Profiles[name] = data
	store.DefaultProfile = name
	if err := writeJSONFile(path, store); err != nil {
		return "", fmt.Errorf("save platform profile: %w", err)
	}
	return name, nil
}

func (a *App) readProject(optional bool) (Project, error) {
	return a.readProjectIn(a.Dir, optional)
}

// readProjectIn reads the hex.json in directory, resolving its platform
// profile.
func (a *App) readProjectIn(directory string, optional bool) (Project, error) {
	var project Project
	data, err := os.ReadFile(filepath.Join(directory, "hex.json"))
	if optional && errors.Is(err, fs.ErrNotExist) {
		return project, nil
	}
	if err != nil {
		return project, fmt.Errorf("read project configuration: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return project, errors.New("project configuration must be an object")
	}
	if err := json.Unmarshal(data, &project); err != nil {
		return project, fmt.Errorf("parse project configuration: %w", err)
	}
	if project.Platform != "" {
		connection, _, err := loadProfile(project.Platform, false)
		if err != nil {
			return project, err
		}
		if project.Server == "" {
			project.Server = connection.Server
		}
		if project.SiteBaseURL == "" {
			project.SiteBaseURL = connection.SiteBaseURL
		}
		if project.Resource == "" {
			project.Resource = connection.Resource
		}
		if project.Resource == connection.Resource || authMatchesResource(connection.Auth, project.Resource) {
			project.ClientID = connection.ClientID
			project.TenantID = connection.TenantID
			project.Auth = connection.Auth
		}
		project.Capabilities = connection.Capabilities
	}
	return project, nil
}

func (a *App) commandConfig(profile string, needsProject bool) (Project, error) {
	return a.commandConfigIn(a.Dir, profile, needsProject)
}

// commandConfigIn resolves the platform for a command run against
// directory: its hex.json when present or needed, then the saved profile.
func (a *App) commandConfigIn(directory, profile string, needsProject bool) (Project, error) {
	var project Project
	var err error
	if profile == "" || needsProject {
		project, err = a.readProjectIn(directory, !needsProject)
		if err != nil {
			return project, err
		}
	}
	if profile != "" || project.Server == "" {
		connection, name, err := loadProfile(profile, false)
		if err != nil {
			return project, err
		}
		project.Platform = name
		project.Server = connection.Server
		project.SiteBaseURL = connection.SiteBaseURL
		project.Resource = connection.Resource
		project.ClientID = connection.ClientID
		project.TenantID = connection.TenantID
		project.Auth = connection.Auth
		project.Capabilities = connection.Capabilities
	}
	return project, nil
}
