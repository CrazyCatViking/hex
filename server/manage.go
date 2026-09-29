package hex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The management portal lets signed-in owners manage their sites and
// artifacts from the platform's landing page: publication history, access
// policies, the site's data, and unpublishing. Pages are rendered here and
// enhanced with HTMX; every change goes through /api/hex/manage/ routes, so
// the API's same-origin and request-marker checks apply. Owners and platform
// admins pass, as for the API.

const managePageSize = 25

type manageView struct {
	Platform string
	Viewer   *Person
	Admin    bool
	All      bool
	Sites    []manageSite
	Site     *manageSite
	History  []historyEntry
	Access   accessView
	Data     bool
	Files    bool
}

type manageSite struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Kind        string    `json:"kind"`
	CreatedBy   string    `json:"createdBy,omitempty"`
	PublishedBy string    `json:"publishedBy,omitempty"`
	Published   string    `json:"-"`
	Audience    string    `json:"audience"`
	Timestamp   time.Time `json:"publishedAt,omitzero"`
}

type historyEntry struct {
	When  string
	ISO   string
	By    string
	Files int
	Size  string
}

type accessView struct {
	Site     string
	Lists    []principalList
	Rules    string
	Message  string
	Error    string
	Artifact bool
}

// principalList is one of the owner, viewer and editor lists in the access
// form; Name is the form field.
type principalList struct {
	Name   string
	Title  string
	Help   string
	Labels []principalLabel
}

// manageEnabled reports whether the portal has what it needs: identities to
// know the viewer, policies to know what they own, and site storage.
func (s *Server) manageEnabled() bool {
	return s.config.Identity != nil && s.config.Access != nil && s.config.Sites != nil
}

func (s *Server) registerManageRoutes() {
	if !s.manageEnabled() {
		return
	}
	s.mux.HandleFunc("GET /manage", s.managePage)
	s.mux.HandleFunc("GET /api/hex/my-sites", s.mySites)
	s.mux.HandleFunc("GET /manage/{site}", s.manageSitePage)
	s.mux.HandleFunc("GET /api/hex/manage.js", s.portalAsset)
	s.mux.HandleFunc("GET /api/hex/manage/directory", s.manageDirectory)
	s.mux.HandleFunc("PUT /api/hex/manage/sites/{site}/access", s.manageUpdateAccess)
	if s.config.Publisher != nil {
		s.mux.HandleFunc("POST /api/hex/manage/sites/{site}/unpublish", s.manageUnpublish)
	}
	if s.config.Database != nil {
		s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/collections", s.manageCollections)
		s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/collections/{collection}", s.manageDocuments)
		s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/collections/{collection}/{id}", s.manageDocument)
		s.mux.HandleFunc("DELETE /api/hex/manage/sites/{site}/collections/{collection}/{id}", s.manageDeleteDocument)
	}
	if s.config.Files != nil {
		s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/files", s.manageFiles)
		s.mux.HandleFunc("DELETE /api/hex/manage/sites/{site}/files/{key...}", s.manageDeleteFile)
	}
}

// managePage lists the sites and artifacts the viewer owns; admins can list
// every site.
func (s *Server) managePage(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	view := s.baseManageView(identity)
	view.All = view.Admin && r.URL.Query().Get("all") == "1"
	sites, err := s.ownedSites(r.Context(), identity, view.All)
	if err != nil {
		writeServerError(w, err)
		return
	}
	view.Sites = sites
	s.renderPage(w, "manage", view)
}

// mySites lists the sites and artifacts the caller owns, newest first, for
// hex sites --mine.
func (s *Server) mySites(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	sites, err := s.ownedSites(r.Context(), identity, false)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sites)
}

func (s *Server) baseManageView(identity *Identity) manageView {
	return manageView{
		Platform: s.platformName(),
		Viewer:   personOf(identity),
		Admin:    s.isAdmin(identity),
		Data:     s.config.Database != nil,
		Files:    s.config.Files != nil,
	}
}

func (s *Server) ownedSites(ctx context.Context, identity *Identity, all bool) ([]manageSite, error) {
	names, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		return nil, err
	}

	sites := []manageSite{}
	for _, name := range names {
		if !siteNamePattern.MatchString(name) {
			continue
		}
		access, exists, err := s.sitePolicy(ctx, name)
		if err != nil {
			return nil, err
		}
		if !all && (!exists || !identity.matchesAny(access.Owners)) {
			continue
		}
		sites = append(sites, s.describeSite(ctx, name, access, exists))
	}

	slices.SortFunc(sites, func(left, right manageSite) int {
		return right.Timestamp.Compare(left.Timestamp)
	})
	return sites, nil
}

func (s *Server) describeSite(ctx context.Context, name string, access SiteAccess, exists bool) manageSite {
	site := manageSite{Name: name, Title: name, Kind: "App", Audience: "Everyone signed in"}
	site.URL, _ = s.siteURL(name)

	if reader, ok := s.config.Sites.(SiteMetadataReader); ok {
		metadata, err := reader.ReadSiteMetadata(ctx, name)
		if err != nil {
			slog.Warn("unreadable site metadata", "site", name, "error", err)
		}
		if metadata != nil {
			if metadata.Title != "" {
				site.Title = metadata.Title
			}
			if metadata.Kind == KindArtifact {
				site.Kind = "Artifact"
			}
			site.CreatedBy = personName(metadata.CreatedBy)
			site.PublishedBy = personName(metadata.PublishedBy)
			site.Timestamp = metadata.PublishedAt
			if !metadata.PublishedAt.IsZero() {
				site.Published = metadata.PublishedAt.Format("Jan 2, 2006 15:04")
			}
		}
	}

	switch {
	case !exists || len(access.Viewers) == 0:
		site.Audience = "Everyone signed in"
	case len(access.Viewers) == 1 && slices.Equal(access.Viewers, access.Owners):
		site.Audience = "Only owners"
	default:
		site.Audience = fmt.Sprintf("%d people or groups", len(access.Viewers))
	}
	return site
}

func personName(person *Person) string {
	if person == nil {
		return ""
	}
	if person.Name != "" {
		return person.Name
	}
	return person.Email
}

// manageSitePage shows one site: its details and history, and the access
// and data sections the viewer may manage.
func (s *Server) manageSitePage(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	identity, site, access, exists, ok := s.manageCaller(w, r)
	if !ok {
		return
	}

	view := s.baseManageView(identity)
	described := s.describeSite(r.Context(), site, access, exists)
	view.Site = &described
	view.History = s.historyEntries(r.Context(), site)
	view.Access = s.accessView(r.Context(), site, access)
	view.Access.Artifact = described.Kind == "Artifact"
	s.renderPage(w, "site", view)
}

// manageCaller authorizes a management request: the caller must own the
// site or administer the platform.
func (s *Server) manageCaller(w http.ResponseWriter, r *http.Request) (*Identity, string, SiteAccess, bool, bool) {
	site := r.PathValue("site")
	if !siteNamePattern.MatchString(site) {
		writeError(w, http.StatusBadRequest, "invalid site")
		return nil, "", SiteAccess{}, false, false
	}
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, "", SiteAccess{}, false, false
	}

	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return nil, "", SiteAccess{}, false, false
	}
	if s.siteRole(identity, access, exists) != roleOwner {
		writeError(w, http.StatusForbidden, "only the site's owners can manage it")
		return nil, "", SiteAccess{}, false, false
	}
	return identity, site, access, exists, true
}

func (s *Server) historyEntries(ctx context.Context, site string) []historyEntry {
	if s.config.Publisher == nil {
		return nil
	}
	history := []Publication{}
	if err := s.readRecord(ctx, site, siteHistoryFile, &history); err != nil {
		slog.Warn("unreadable publication history", "site", site, "error", err)
	}

	entries := make([]historyEntry, 0, len(history))
	for _, publication := range history {
		entries = append(entries, historyEntry{
			When:  publication.PublishedAt.Format("Jan 2, 2006 15:04"),
			ISO:   publication.PublishedAt.Format(time.RFC3339),
			By:    personName(publication.PublishedBy),
			Files: publication.Files,
			Size:  formatBytes(publication.Bytes),
		})
	}
	return entries
}

func (s *Server) accessView(ctx context.Context, site string, access SiteAccess) accessView {
	rules := SiteAccess{Paths: access.Paths, Collections: access.Collections, Files: access.Files, Channels: access.Channels}
	encoded, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		encoded = []byte("{}")
	}
	return accessView{
		Site: site,
		Lists: []principalList{
			{"owner", "Owners", "Manage this page, publish, and see all data.", s.describePrincipals(ctx, access.Owners)},
			{"viewer", "Viewers", "Can open the site and read its data. Leave empty to let everyone signed in open it.", s.describePrincipals(ctx, access.Viewers)},
			{"editor", "Editors", "Can change the site's data. Leave empty to let every viewer edit.", s.describePrincipals(ctx, access.Editors)},
		},
		Rules: rulesText(encoded),
	}
}

// rulesText drops the empty owner, editor and viewer lists from the rule
// editor, which edits them separately.
func rulesText(encoded []byte) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return "{}"
	}
	for _, name := range []string{"owners", "editors", "viewers"} {
		delete(fields, name)
	}
	if len(fields) == 0 {
		return "{}"
	}
	pretty, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(pretty)
}

// manageUpdateAccess replaces the site's policy from the access form and
// re-renders the form with the outcome.
func (s *Server) manageUpdateAccess(w http.ResponseWriter, r *http.Request) {
	identity, site, access, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}

	view := s.accessView(r.Context(), site, access)
	view.Artifact = r.PostForm.Get("artifact") == "1"
	requested, err := accessFromForm(r)
	if err != nil {
		view.Error = err.Error()
		view.Rules = r.PostForm.Get("rules")
		s.renderFragment(w, "access", view)
		return
	}

	saved, status, err := s.replaceSiteAccess(r.Context(), identity, site, requested)
	if err != nil {
		if status == 0 {
			writeServerError(w, err)
			return
		}
		view.Error = err.Error()
		view.Rules = r.PostForm.Get("rules")
		s.renderFragment(w, "access", view)
		return
	}

	slog.Info("site access changed in the portal", "site", site, "by", identityName(identity))
	view = s.accessView(r.Context(), site, saved)
	view.Artifact = r.PostForm.Get("artifact") == "1"
	view.Message = "Saved. Changes apply within a few seconds."
	s.renderFragment(w, "access", view)
}

// accessFromForm builds a policy from the form's principal lists and the
// JSON rules text.
func accessFromForm(r *http.Request) (SiteAccess, error) {
	var access SiteAccess
	rules := strings.TrimSpace(r.PostForm.Get("rules"))
	if rules != "" {
		decoder := json.NewDecoder(strings.NewReader(rules))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&access); err != nil {
			return SiteAccess{}, fmt.Errorf("the rules are not valid: %v", err)
		}
	}
	access.Owners = cleanPrincipals(r.PostForm["owner"])
	access.Editors = cleanPrincipals(r.PostForm["editor"])
	access.Viewers = cleanPrincipals(r.PostForm["viewer"])
	if len(access.Owners) == 0 {
		return SiteAccess{}, errors.New("a site needs at least one owner")
	}
	return access, nil
}

func cleanPrincipals(values []string) []string {
	principals := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(principals, value) {
			principals = append(principals, value)
		}
	}
	return principals
}

// manageDirectory renders picker suggestions: configured groups, people who
// have used the platform, and a typed email address.
func (s *Server) manageDirectory(w http.ResponseWriter, r *http.Request) {
	if s.requestIdentity(r) == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	result, err := s.searchDirectory(r.Context(), query, 8)
	if err != nil {
		writeServerError(w, err)
		return
	}

	var suggestions []principalLabel
	for _, group := range result.Groups {
		suggestions = append(suggestions, principalLabel{Principal: "group:" + group.ID, Kind: "group", Name: group.Name, Detail: "group"})
	}
	for _, person := range result.People {
		suggestions = append(suggestions, principalLabel{Principal: "user:" + person.ID, Kind: "user", Name: person.Name, Detail: person.Email})
	}
	if strings.Contains(query, "@") && principalPattern.MatchString("user:"+query) {
		suggestions = append(suggestions, principalLabel{Principal: "user:" + query, Kind: "user", Name: query, Detail: "by email"})
	}
	s.renderFragment(w, "suggestions", struct {
		List        string
		Suggestions []principalLabel
		Query       string
	}{r.URL.Query().Get("list"), suggestions, query})
}

// manageUnpublish removes the site's files; the policy stays, keeping the
// name reserved for its owners.
func (s *Server) manageUnpublish(w http.ResponseWriter, r *http.Request) {
	identity, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	if r.FormValue("confirm") != site {
		writeError(w, http.StatusBadRequest, "type the site name to confirm")
		return
	}
	if err := s.config.Publisher.DeleteSite(r.Context(), site); err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("site unpublished in the portal", "site", site, "by", identityName(identity))
	w.Header().Set("HX-Redirect", "/manage")
	w.WriteHeader(http.StatusNoContent)
}

type collectionsView struct {
	Site        string
	Collections []Collection
	Unsupported bool
}

func (s *Server) manageCollections(w http.ResponseWriter, r *http.Request) {
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	lister, supported := s.config.Database.(CollectionLister)
	view := collectionsView{Site: site, Unsupported: !supported}
	if supported {
		collections, err := lister.ListCollections(r.Context(), site)
		if err != nil {
			writeServerError(w, err)
			return
		}
		view.Collections = collections
	}
	s.renderFragment(w, "collections", view)
}

type documentsView struct {
	Site       string
	Collection string
	Documents  []documentRow
	Next       string
}

type documentRow struct {
	ID        string
	CreatedBy string
	Preview   string
}

func (s *Server) manageDocuments(w http.ResponseWriter, r *http.Request) {
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok || !validateIdentifiers(w, r) {
		return
	}
	collection := r.PathValue("collection")
	after := r.URL.Query().Get("after")
	documents, err := s.config.Database.List(r.Context(), site, collection, ListOptions{After: after, Limit: managePageSize + 1})
	if err != nil {
		writeServerError(w, err)
		return
	}

	view := documentsView{Site: site, Collection: collection}
	if len(documents) > managePageSize {
		documents = documents[:managePageSize]
		view.Next = documents[len(documents)-1].ID
	}
	creators := s.creatorNames(r.Context(), documents)
	for _, document := range documents {
		view.Documents = append(view.Documents, documentRow{
			ID:        document.ID,
			CreatedBy: creators[document.CreatedBy],
			Preview:   preview(document.Data, 160),
		})
	}
	s.renderFragment(w, "documents", view)
}

func (s *Server) creatorNames(ctx context.Context, documents []Document) map[string]string {
	var principals []string
	for _, document := range documents {
		if document.CreatedBy != "" && !slices.Contains(principals, "user:"+document.CreatedBy) {
			principals = append(principals, "user:"+document.CreatedBy)
		}
	}
	names := map[string]string{}
	for _, label := range s.describePrincipals(ctx, principals) {
		names[strings.TrimPrefix(label.Principal, "user:")] = label.Name
	}
	return names
}

func preview(data json.RawMessage, limit int) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return string(data)
	}
	text := compact.String()
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}

func (s *Server) manageDocument(w http.ResponseWriter, r *http.Request) {
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok || !validateIdentifiers(w, r) {
		return
	}
	document, err := s.config.Database.Get(r.Context(), site, r.PathValue("collection"), r.PathValue("id"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, document.Data, "", "  "); err != nil {
		pretty.Write(document.Data)
	}
	s.renderFragment(w, "document", struct {
		ID   string
		JSON string
	}{document.ID, pretty.String()})
}

func (s *Server) manageDeleteDocument(w http.ResponseWriter, r *http.Request) {
	identity, site, _, _, ok := s.manageCaller(w, r)
	if !ok || !validateIdentifiers(w, r) {
		return
	}
	collection := r.PathValue("collection")
	id := r.PathValue("id")
	if err := s.config.Database.Delete(r.Context(), site, collection, id, WriteOptions{}); err != nil && !errors.Is(err, ErrNotFound) {
		writeServerError(w, err)
		return
	}
	slog.Info("document deleted in the portal", "site", site, "collection", collection, "id", id, "by", identityName(identity))
	w.WriteHeader(http.StatusOK)
}

type filesView struct {
	Site  string
	Files []fileRow
}

type fileRow struct {
	Key        string
	Link       string
	DeleteLink string
	Size       string
}

func (s *Server) manageFiles(w http.ResponseWriter, r *http.Request) {
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	prefix := site + "/"
	objects, err := s.config.Files.List(r.Context(), prefix)
	if err != nil {
		writeServerError(w, err)
		return
	}

	view := filesView{Site: site}
	for _, object := range objects {
		key := strings.TrimPrefix(object.Key, prefix)
		view.Files = append(view.Files, fileRow{
			Key:        key,
			Link:       "/api/sites/" + site + "/files/" + escapePath(key),
			DeleteLink: "/api/hex/manage/sites/" + site + "/files/" + escapePath(key),
			Size:       formatBytes(object.Size),
		})
	}
	slices.SortFunc(view.Files, func(left, right fileRow) int { return strings.Compare(left.Key, right.Key) })
	s.renderFragment(w, "files", view)
}

func (s *Server) manageDeleteFile(w http.ResponseWriter, r *http.Request) {
	identity, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if !validKey(key) {
		writeError(w, http.StatusBadRequest, "invalid file key")
		return
	}
	if err := s.config.Files.Delete(r.Context(), site+"/"+key); err != nil && !errors.Is(err, ErrNotFound) {
		writeServerError(w, err)
		return
	}
	slog.Info("file deleted in the portal", "site", site, "key", key, "by", identityName(identity))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) renderPage(w http.ResponseWriter, name string, view any) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.renderFragment(w, name, view)
}

func (s *Server) renderFragment(w http.ResponseWriter, name string, view any) {
	var output bytes.Buffer
	if err := portalTemplates.ExecuteTemplate(&output, name, view); err != nil {
		writeServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(output.Bytes()); err != nil {
		slog.Error("write portal page", "error", err)
	}
}

func formatBytes(size int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return strconv.FormatInt(size, 10) + " B"
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}
