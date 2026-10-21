package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"

	hex "github.com/crazycatviking/hex/server"
)

// Directory settings are deliberately separate from direct platform profiles.
// Importing or refreshing a directory cannot replace a direct connection.
type workspaceDirectory struct {
	Version        int             `json:"version"`
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	Server         string          `json:"server"`
	Auth           *hex.AuthConfig `json:"auth"`
	WorkspacesPath string          `json:"workspacesPath"`
}

type workspacePin struct {
	Directory string `json:"directory"`
	ID        string `json:"id"`
}

type workspaceContext struct {
	workspacePin
	Account string `json:"account"`
}

type workspaceEntry struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Organization     string          `json:"organization"`
	OrganizationName string          `json:"organizationName"`
	Status           string          `json:"status"`
	Role             string          `json:"role"`
	Connection       json.RawMessage `json:"connection"`
}

type directoryStore struct {
	Version     int                           `json:"version"`
	Default     string                        `json:"default,omitempty"`
	Directories map[string]workspaceDirectory `json:"directories"`
	Active      *workspaceContext             `json:"active,omitempty"`
}

// This context is constructed only after resolving a workspace through its
// trusted directory. It is never accepted from a direct connection document.
type directorySession struct {
	Origin      string
	Account     string
	Target      string
	SiteBaseURL string
}

func readDirectories() (directoryStore, string, error) {
	store := directoryStore{Version: 1, Directories: map[string]workspaceDirectory{}}
	directory, err := profileDirectory()
	if err != nil {
		return store, "", err
	}
	file := filepath.Join(directory, "directories.json")
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return store, file, nil
	}
	if err != nil {
		return store, file, fmt.Errorf("read workspace directories: %w", err)
	}
	if err := decodeStrict(data, &store); err != nil || store.Version != 1 || store.Directories == nil {
		return store, file, errors.New("invalid workspace directory store")
	}
	for name, directory := range store.Directories {
		if !profileNamePattern.MatchString(name) {
			return store, file, errors.New("invalid workspace directory name")
		}
		if err := directory.validate(directory.Server); err != nil {
			return store, file, err
		}
	}
	if store.Active != nil {
		if err := validateWorkspacePin(&store.Active.workspacePin); err != nil {
			return store, file, err
		}
		if _, err := accountAlias(store.Active.Account); err != nil {
			return store, file, err
		}
	}
	return store, file, nil
}

func validateWorkspacePin(pin *workspacePin) error {
	if pin.Directory == "" || pin.ID == "" || strings.ContainsAny(pin.ID, "/\\") || strings.ContainsFunc(pin.ID, unicode.IsControl) {
		return errors.New("workspace pin requires a directory origin and a stable workspace ID")
	}
	_, err := origin(pin.Directory, true)
	return err
}

func (d *workspaceDirectory) validate(expected string) error {
	if d.Version != 1 || d.Kind != "workspace-directory" || strings.TrimSpace(d.Name) == "" || strings.ContainsFunc(d.Name, unicode.IsControl) {
		return errors.New("invalid version, kind or name in workspace directory")
	}
	server, err := origin(d.Server, true)
	if err != nil {
		return err
	}
	if expected != "" {
		want, err := origin(expected, true)
		if err != nil || want.String() != server.String() {
			return errors.New("workspace directory belongs to a different origin")
		}
	}
	if d.Auth == nil || d.Auth.Type != "oidc" {
		return errors.New("workspace directories require explicit OIDC authentication")
	}
	if err := d.Auth.Validate(); err != nil {
		return err
	}
	u, err := url.Parse(d.WorkspacesPath)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(d.WorkspacesPath, "/api/") || path.Clean(d.WorkspacesPath) != d.WorkspacesPath {
		return errors.New("workspacesPath must be a same-origin /api/ path")
	}
	d.Server = server.String()
	return nil
}

func saveDirectory(data []byte, expected, name string) (setupResult, bool, error) {
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &header); err != nil || header.Kind != "workspace-directory" {
		return setupResult{}, false, nil
	}
	var directory workspaceDirectory
	if len(data) > maxConfigBytes {
		return setupResult{}, true, errors.New("directory settings exceed 64 KiB")
	}
	if err := decodeStrict(data, &directory); err != nil {
		return setupResult{}, true, fmt.Errorf("invalid workspace directory: %w", err)
	}
	if err := directory.validate(expected); err != nil {
		return setupResult{}, true, err
	}
	if name == "" {
		u, _ := url.Parse(directory.Server)
		name = strings.ReplaceAll(u.Hostname(), ":", "-")
		if u.Port() != "" {
			name += "-" + u.Port()
		}
	}
	if !profileNamePattern.MatchString(name) {
		return setupResult{}, true, errors.New("invalid directory name")
	}
	store, file, err := readDirectories()
	if err != nil {
		return setupResult{}, true, err
	}
	if old, exists := store.Directories[name]; exists && (old.Server != directory.Server || !reflect.DeepEqual(old.Auth, directory.Auth)) {
		return setupResult{}, true, errors.New("directory origin or authentication changed; use a new directory name")
	}
	for alias, existing := range store.Directories {
		if alias != name && existing.Server == directory.Server && (!reflect.DeepEqual(existing.Auth, directory.Auth) || existing.WorkspacesPath != directory.WorkspacesPath) {
			return setupResult{}, true, fmt.Errorf("directory origin is already registered as %q with different settings", alias)
		}
	}
	store.Directories[name] = directory
	if store.Default == "" {
		store.Default = name
	}
	if err := writeJSONFile(file, store); err != nil {
		return setupResult{}, true, err
	}
	return setupResult{Status: "ready", Kind: directory.Kind, Directory: name, Name: directory.Name, Server: directory.Server, NextCommand: "hex login --directory " + name}, true, nil
}

func (s directoryStore) selectDirectory(selector string) (workspaceDirectory, error) {
	if selector == "" {
		selector = s.Default
	}
	if directory, exists := s.Directories[selector]; exists {
		return directory, nil
	}
	for _, directory := range s.Directories {
		if directory.Server == selector {
			return directory, nil
		}
	}
	return workspaceDirectory{}, errors.New("unknown workspace directory; run hex setup <directory-url> --name <name>")
}

func accountAlias(value string) (string, error) {
	if value == "" {
		value = "default"
	}
	if !profileNamePattern.MatchString(value) {
		return "", errors.New("invalid account alias")
	}
	return value, nil
}

func directoryProject(directory workspaceDirectory, account string) Project {
	return Project{Server: directory.Server, Auth: directory.Auth, directorySession: &directorySession{Origin: directory.Server, Account: account, Target: directory.Server}}
}

func validateDirectoryTarget(project Project) error {
	if session := project.directorySession; session != nil {
		u, err := origin(project.Server, true)
		if err != nil || u.String() != session.Target || project.SiteBaseURL != session.SiteBaseURL {
			return errors.New("directory credentials cannot be sent to an overridden destination; select another workspace or a direct --platform profile")
		}
	}
	return nil
}

func (a *App) directoryHTTPClient() *http.Client {
	client := *a.HTTP
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func (a *App) workspaceEntries(ctx context.Context, directory workspaceDirectory, account string) ([]workspaceEntry, error) {
	data, err := a.apiRequest(ctx, directoryProject(directory, account), directory.WorkspacesPath)
	if err != nil {
		return nil, err
	}
	var entries []workspaceEntry
	if err := json.Unmarshal(data, &entries); err != nil || len(entries) > 1000 {
		return nil, errors.New("invalid workspace directory response (maximum 1000 workspaces)")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.ID == "" || strings.ContainsAny(entry.ID, "/\\") || strings.ContainsFunc(entry.ID+entry.Name+entry.Organization+entry.OrganizationName+entry.Status+entry.Role, unicode.IsControl) || seen[entry.ID] {
			return nil, errors.New("invalid or duplicate workspace identity")
		}
		seen[entry.ID] = true
		connection, err := parseConnection(entry.Connection, "")
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", entry.ID, err)
		}
		if !reflect.DeepEqual(connection.Auth, directory.Auth) {
			return nil, fmt.Errorf("workspace %q does not use its directory's authentication", entry.ID)
		}
	}
	if entries == nil {
		entries = []workspaceEntry{}
	}
	return entries, nil
}

func selectWorkspace(entries []workspaceEntry, reference string) (workspaceEntry, error) {
	var selected *workspaceEntry
	for i := range entries {
		entry := &entries[i]
		if entry.ID != reference && entry.Organization+"/"+entry.ID != reference {
			continue
		}
		if selected != nil {
			return workspaceEntry{}, errors.New("ambiguous workspace; use its stable ID")
		}
		selected = entry
	}
	if selected == nil {
		return workspaceEntry{}, fmt.Errorf("workspace %q is unavailable to this account; run hex workspace list", reference)
	}
	return *selected, nil
}

func (a *App) resolveWorkspace(directory workspaceDirectory, account, reference string, project Project) (Project, error) {
	ctx := a.commandContext
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := a.workspaceEntries(ctx, directory, account)
	if err != nil {
		return project, err
	}
	entry, err := selectWorkspace(entries, reference)
	if err != nil {
		return project, err
	}
	connection, err := parseConnection(entry.Connection, "")
	if err != nil {
		return project, err
	}
	project.Server, project.SiteBaseURL = connection.Server, connection.SiteBaseURL
	project.Auth, project.Capabilities = directory.Auth, connection.Capabilities
	project.Resource, project.ClientID, project.TenantID, project.Platform = "", "", "", ""
	project.Workspace = &workspacePin{Directory: directory.Server, ID: entry.ID}
	project.directorySession = &directorySession{Origin: directory.Server, Account: account, Target: connection.Server, SiteBaseURL: connection.SiteBaseURL}
	return project, nil
}

func (a *App) workspaceConfigIn(directory, profile string, needsProject bool) (Project, bool, error) {
	if profile != "" {
		if a.workspace != "" || a.directory != "" || a.account != "" {
			return Project{}, true, errors.New("use --platform or directory workspace selection, not both")
		}
		return Project{}, false, nil
	}
	project, err := readProjectFile(directory, !needsProject)
	if err != nil {
		return project, true, err
	}
	if a.workspace == "" && project.Workspace == nil && (project.Platform != "" || project.Server != "") {
		if a.directory != "" || a.account != "" {
			return project, true, errors.New("this project pins a direct connection; use --workspace to explicitly override it")
		}
		return project, false, nil
	}
	store, _, err := readDirectories()
	if err != nil {
		return project, true, err
	}
	selector, reference, account := a.directory, a.workspace, a.account
	if reference == "" && project.Workspace != nil {
		if err := validateWorkspacePin(project.Workspace); err != nil {
			return project, true, err
		}
		if project.Platform != "" || project.Server != "" || project.SiteBaseURL != "" {
			return project, true, errors.New("a workspace pin cannot be combined with direct platform/server settings")
		}
		if selector == "" {
			selector = project.Workspace.Directory
		}
		reference = project.Workspace.ID
	}
	if reference == "" && store.Active != nil {
		if selector != "" {
			selected, err := store.selectDirectory(selector)
			if err != nil {
				return project, true, err
			}
			if selected.Server != store.Active.Directory {
				return project, true, errors.New("the active workspace belongs to another directory; pass --workspace explicitly")
			}
		}
		if selector == "" {
			selector = store.Active.Directory
		}
		reference = store.Active.ID
		if account == "" {
			account = store.Active.Account
		}
	}
	if reference == "" {
		if a.directory != "" || a.account != "" {
			return project, true, errors.New("select a workspace with hex workspace use, or pass --workspace")
		}
		if len(store.Directories) > 0 {
			connection, _, err := loadProfile("", true)
			if err != nil {
				return project, true, err
			}
			if connection == nil {
				return project, true, errors.New("no workspace selected; run hex workspace list and hex workspace use <id>, or pass --workspace")
			}
		}
		return project, false, nil
	}
	if selector == "" && project.Workspace != nil {
		selector = project.Workspace.Directory
	}
	if selector == "" && store.Active != nil {
		selector = store.Active.Directory
	}
	dir, err := store.selectDirectory(selector)
	if err != nil {
		return project, true, err
	}
	if a.workspace == "" && project.Workspace != nil && dir.Server != project.Workspace.Directory {
		return project, true, errors.New("the project pins another workspace directory; pass --workspace to explicitly override it")
	}
	if account == "" && store.Active != nil && store.Active.Directory == dir.Server {
		account = store.Active.Account
	}
	account, err = accountAlias(account)
	if err != nil {
		return project, true, err
	}
	project, err = a.resolveWorkspace(dir, account, reference, project)
	return project, true, err
}

// Authentication commands must work before workspace selection and after access
// is revoked. They never need an authenticated workspace listing just to log out.
func (a *App) authCommandConfig(profile string) (Project, error) {
	if profile != "" {
		if a.workspace != "" || a.directory != "" || a.account != "" {
			return Project{}, errors.New("use --platform or --directory, not both")
		}
		return a.directCommandConfigIn(a.Dir, profile, false)
	}
	project, err := readProjectFile(a.Dir, true)
	if err != nil {
		return project, err
	}
	if a.workspace == "" && a.directory == "" && a.account != "" && project.Workspace == nil && (project.Platform != "" || project.Server != "") {
		return project, errors.New("--account is for directory connections; select --directory explicitly for account login")
	}
	if a.workspace == "" && a.directory == "" && a.account == "" && project.Workspace == nil && (project.Platform != "" || project.Server != "") {
		return a.directCommandConfigIn(a.Dir, "", false)
	}
	store, _, err := readDirectories()
	if err != nil {
		return project, err
	}
	selector, account := a.directory, a.account
	if selector == "" && project.Workspace != nil {
		if err := validateWorkspacePin(project.Workspace); err != nil {
			return project, err
		}
		selector = project.Workspace.Directory
	}
	if selector == "" && store.Active != nil {
		selector = store.Active.Directory
	}
	if selector == "" && a.workspace == "" && a.account == "" {
		connection, _, err := loadProfile("", true)
		if err != nil {
			return project, err
		}
		if connection != nil {
			return a.directCommandConfigIn(a.Dir, "", false)
		}
	}
	dir, err := store.selectDirectory(selector)
	if err != nil {
		if len(store.Directories) == 0 && a.directory == "" && a.account == "" && a.workspace == "" {
			return a.directCommandConfigIn(a.Dir, "", false)
		}
		return project, err
	}
	if account == "" && store.Active != nil && store.Active.Directory == dir.Server {
		account = store.Active.Account
	}
	account, err = accountAlias(account)
	if err != nil {
		return project, err
	}
	return directoryProject(dir, account), nil
}

func clearActiveWorkspace() error {
	store, file, err := readDirectories()
	if err != nil {
		return err
	}
	if store.Active == nil {
		return nil
	}
	store.Active = nil
	return writeJSONFile(file, store)
}
