package hex

import (
	"net/http"
	"slices"
	"strings"
)

type workspaceAPIEntry struct {
	workspaceShareRow
	Site   string
	Owners string
	Owned  bool
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
	names, err := s.config.Sites.ListSites(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}
	slices.Sort(names)
	for _, site := range slices.Compact(names) {
		if !siteNamePattern.MatchString(site) {
			continue
		}
		if reader, ok := s.config.Sites.(SiteMetadataReader); ok {
			metadata, err := reader.ReadSiteMetadata(r.Context(), site)
			if err != nil {
				writeServerError(w, err)
				return
			}
			if metadata != nil && (metadata.Kind == KindArtifact || metadata.Discoverable != nil && !*metadata.Discoverable) {
				continue
			}
		}
		access, exists, err := s.sitePolicy(r.Context(), site)
		if err != nil {
			writeServerError(w, err)
			return
		}
		if !exists {
			continue
		}
		rows, err := s.workspaceShareRows(r.Context(), site, access)
		if err != nil {
			writeServerError(w, err)
			return
		}
		visible := s.siteRole(identity, access, true) != roleNone
		var owners []string
		for _, owner := range s.describePrincipals(r.Context(), access.Owners) {
			owners = append(owners, owner.Name)
		}
		for _, row := range rows {
			// Public API contracts can be found independently of page admission.
			if !visible && row.Mode != "workspace" {
				continue
			}
			if !strings.Contains(strings.ToLower(site+" "+row.Name+" "+row.Description), strings.ToLower(view.Query)) {
				continue
			}
			view.Entries = append(view.Entries, workspaceAPIEntry{workspaceShareRow: row, Site: site, Owners: strings.Join(owners, ", "), Owned: identity.matchesAny(access.Owners)})
		}
	}
	s.writePortalPage(w, "workspace-apis", view)
}
