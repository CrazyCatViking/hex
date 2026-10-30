package hex

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

type workspaceAPIEntry struct {
	workspaceShareRow
	Site       string
	Owners     string
	OwnerNames []string
	Owned      bool
}

func (s *Server) workspaceAPIsPage(w http.ResponseWriter, r *http.Request) {
	if !s.platformRequest(r) {
		http.NotFound(w, r)
		return
	}
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	view := struct {
		Chrome
		Entries []workspaceAPIEntry
		Query   string
	}{Chrome: s.chromeFor(identity, "workspace-apis"), Query: r.URL.Query().Get("q")}
	var err error
	if view.Entries, err = s.workspaceAPIEntries(r.Context(), identity, view.Query); err != nil {
		writeServerError(w, err)
		return
	}
	s.writePortalPage(w, "workspace-apis", view)
}

// workspaceAPIsHTTP is the machine-readable workspace catalog, so agents can
// find exported actions and events and the owners to ask for access.
func (s *Server) workspaceAPIsHTTP(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	entries, err := s.workspaceAPIEntries(r.Context(), identity, r.URL.Query().Get("q"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	type workspaceAPI struct {
		Site           string          `json:"site"`
		Kind           string          `json:"kind"`
		Name           string          `json:"name"`
		Description    string          `json:"description"`
		Version        int             `json:"version,omitempty"`
		Sharing        string          `json:"sharing"`
		WorkspaceModes []string        `json:"workspaceModes,omitzero"`
		Owners         []string        `json:"owners"`
		Owned          bool            `json:"owned"`
		ReadOnly       bool            `json:"readOnly,omitempty"`
		InputSchema    json.RawMessage `json:"inputSchema,omitempty"`
		OutputSchema   json.RawMessage `json:"outputSchema,omitempty"`
	}
	result := make([]workspaceAPI, 0, len(entries))
	for _, entry := range entries {
		api := workspaceAPI{Site: entry.Site, Kind: entry.Kind, Name: entry.Name, Description: entry.Description, Version: entry.Version, Sharing: entry.Mode, WorkspaceModes: entry.Modes, Owners: entry.OwnerNames, Owned: entry.Owned}
		if api.Owners == nil {
			api.Owners = []string{}
		}
		if definition := entry.definition; definition != nil {
			api.ReadOnly, api.InputSchema, api.OutputSchema = definition.ReadOnly, definition.InputSchema, definition.OutputSchema
		}
		result = append(result, api)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) workspaceAPIEntries(ctx context.Context, identity *Identity, query string) ([]workspaceAPIEntry, error) {
	var entries []workspaceAPIEntry
	names, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	for _, site := range slices.Compact(names) {
		if !siteNamePattern.MatchString(site) {
			continue
		}
		if reader, ok := s.config.Sites.(SiteMetadataReader); ok {
			metadata, err := reader.ReadSiteMetadata(ctx, site)
			if err != nil {
				return nil, err
			}
			if metadata != nil && (metadata.Kind == KindArtifact || metadata.Discoverable != nil && !*metadata.Discoverable) {
				continue
			}
		}
		access, exists, err := s.sitePolicy(ctx, site)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		rows, err := s.workspaceShareRows(ctx, site, access)
		if err != nil {
			return nil, err
		}
		visible := s.siteRole(identity, access, true) != roleNone
		var owners []string
		for _, owner := range s.describePrincipals(ctx, access.Owners) {
			owners = append(owners, owner.Name)
		}
		for _, row := range rows {
			// Public API contracts can be found independently of page admission.
			if !visible && row.Mode != "workspace" {
				continue
			}
			if !strings.Contains(strings.ToLower(site+" "+row.Name+" "+row.Description), strings.ToLower(query)) {
				continue
			}
			entries = append(entries, workspaceAPIEntry{workspaceShareRow: row, Site: site, Owners: strings.Join(owners, ", "), OwnerNames: owners, Owned: identity.matchesAny(access.Owners)})
		}
	}
	return entries, nil
}
