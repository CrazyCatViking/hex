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
// artifacts: who can open them, their data, their history, and unpublishing.
// Pages are rendered here and enhanced with HTMX; every change goes through
// /api/hex/manage/ routes, so the API's same-origin and request-marker checks
// apply. Owners and platform admins pass, as for the API.

const managePageSize = 25

type manageListView struct {
	Chrome
	Sites          []siteCard
	Filter         string
	Query          string
	All            bool
	Counts         struct{ All, Apps, Files int }
	Sort           string
	SortChoices    []choice
	TrafficEnabled bool
	TrafficFilter  string
	TrafficChoices []choice
}

type sitePageView struct {
	Chrome
	Site      siteCard
	Tab       string
	Tabs      []siteTab
	Facts     siteFacts
	People    []shareEntry
	History   []historyEntry
	Sharing   sharingView
	Data      bool
	Files     bool
	Actions   []actionSummary
	Analytics *AnalyticsView
}

// actionSummary describes one of a site's actions on its overview.
type actionSummary struct {
	Name        string
	Description string
	Effect      string
	Who         string
}

type siteTab struct {
	ID    string
	Label string
}

type siteFacts struct {
	CreatedBy   string
	Created     string
	CreatedISO  string
	PublishedBy string
	Published   string
	Files       int
	Size        string
}

type historyEntry struct {
	When  string
	ISO   string
	By    string
	Files int
	Size  string
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
	s.mux.HandleFunc("GET /manage/{site}", s.manageSitePage)
	s.mux.HandleFunc("GET /api/hex/my-sites", s.mySites)
	s.mux.HandleFunc("GET /api/hex/manage.js", s.portalAsset)
	s.mux.HandleFunc("PUT /api/hex/manage/sites/{site}/sharing", s.manageUpdateSharing)
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

// managePage lists the sites and files the viewer owns, filtered by kind and
// search text; admins can list every site.
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

	view := manageListView{Chrome: s.chromeFor(identity, "manage")}
	query := r.URL.Query()
	view.All = view.Viewer.Admin && query.Get("all") == "1"
	view.Filter = query.Get("kind")
	view.Query = strings.TrimSpace(query.Get("q"))
	view.Sort = query.Get("sort")
	if view.Sort == "" {
		view.Sort = "recent"
	}
	view.SortChoices = s.cardSortChoices(view.Sort)
	view.TrafficEnabled = s.cardTrafficEnabled()
	view.TrafficFilter = query.Get("traffic")
	if view.TrafficFilter == "" {
		view.TrafficFilter = "all"
	}
	view.TrafficChoices = cardTrafficChoices(view.TrafficFilter)

	mine, _, err := s.personalSites(r.Context(), identity, view.All)
	if err != nil {
		writeServerError(w, err)
		return
	}
	for _, card := range mine {
		view.Counts.All++
		if card.Kind == "File" {
			view.Counts.Files++
		} else {
			view.Counts.Apps++
		}
		if !matchesFilter(card, view.Filter, view.Query) || view.TrafficEnabled && !matchesTraffic(card, view.TrafficFilter) {
			continue
		}
		view.Sites = append(view.Sites, card)
	}
	sortSiteCards(view.Sites, view.Sort)
	s.writePortalPage(w, "manage", view)
}

func matchesFilter(card siteCard, kind, query string) bool {
	switch kind {
	case "apps":
		if card.Kind != "App" {
			return false
		}
	case "files":
		if card.Kind != "File" {
			return false
		}
	}
	text := strings.ToLower(strings.Join([]string{card.Name, card.Title, card.Description}, " "))
	return strings.Contains(text, strings.ToLower(query))
}

// mySites lists the sites and artifacts the caller owns, newest first, for
// hex sites --mine.
func (s *Server) mySites(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	mine, _, err := s.personalSites(r.Context(), identity, false)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if mine == nil {
		mine = []siteCard{}
	}
	writeJSON(w, http.StatusOK, mine)
}

var siteTabs = []siteTab{
	{"overview", "Overview"},
	{"sharing", "Sharing"},
	{"data", "Data"},
	{"history", "History"},
	{"settings", "Settings"},
}

// manageSitePage shows one site on the selected tab.
func (s *Server) manageSitePage(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	identity, site, access, exists, ok := s.manageCaller(w, r)
	if !ok {
		return
	}

	tabs := slices.Clone(siteTabs)
	if s.config.Analytics != nil {
		tabs = append(tabs, siteTab{"analytics", "Analytics"})
	}
	view := sitePageView{
		Chrome: s.chromeFor(identity, "manage"),
		Tab:    r.URL.Query().Get("tab"),
		Tabs:   tabs,
		Data:   s.config.Database != nil,
		Files:  s.config.Files != nil,
	}
	if !slices.ContainsFunc(tabs, func(tab siteTab) bool { return tab.ID == view.Tab }) {
		view.Tab = "overview"
	}

	var metadata *SiteMetadata
	if reader, ok := s.config.Sites.(SiteMetadataReader); ok {
		var err error
		if metadata, err = reader.ReadSiteMetadata(r.Context(), site); err != nil {
			slog.Warn("unreadable site metadata", "site", site, "error", err)
		}
	}
	siteURL, _ := s.siteURL(site)
	view.Site = s.cardForSite(site, siteURL, metadata, time.Now())
	view.Site.Access, view.Site.AccessTone = audience(access, exists)
	view.History = s.historyEntries(r.Context(), site)
	view.Facts = factsFrom(metadata, view.History)
	view.Sharing = s.sharingView(r.Context(), site, access, exists)
	view.People = view.Sharing.Entries
	view.Actions = s.actionSummaries(r.Context(), site)
	if view.Tab == "analytics" {
		analytics, err := s.loadSiteAnalytics(r.Context(), site, r.URL.Query())
		if err != nil {
			writeSiteAnalyticsError(w, err)
			return
		}
		view.Analytics = &analytics
	}
	s.writePortalPage(w, "site", view)
}

// actionSummaries lists every action of the site, whoever may run it, so
// owners see what their app exposes.
func (s *Server) actionSummaries(ctx context.Context, site string) []actionSummary {
	if !s.actionsEnabled() {
		return nil
	}
	actions, err := s.siteActions(ctx, site)
	if err != nil {
		slog.Warn("unreadable site actions", "site", site, "error", err)
		return nil
	}
	summaries := make([]actionSummary, 0, len(actions))
	for _, action := range actions {
		effect := action.effect
		if effect == "" {
			effect = "Provided by the platform"
		}
		who := "Editors"
		switch action.audience.Level {
		case LevelViewers:
			who = "Everyone who can open it"
		case LevelOwners:
			who = "Owners"
		case "":
			if len(action.audience.Principals) > 0 {
				who = "Selected people"
			}
		}
		summaries = append(summaries, actionSummary{
			Name: action.definition.Name, Description: action.definition.Description,
			Effect: effect, Who: who,
		})
	}
	return summaries
}

func factsFrom(metadata *SiteMetadata, history []historyEntry) siteFacts {
	var facts siteFacts
	if metadata != nil {
		facts.CreatedBy = personName(metadata.CreatedBy)
		if facts.CreatedBy == "" {
			facts.CreatedBy = metadata.Author
		}
		if !metadata.CreatedAt.IsZero() {
			facts.Created = metadata.CreatedAt.Format("Jan 2, 2006")
			facts.CreatedISO = metadata.CreatedAt.Format(time.RFC3339)
		}
		facts.PublishedBy = personName(metadata.PublishedBy)
		if !metadata.PublishedAt.IsZero() {
			facts.Published = relativeTime(metadata.PublishedAt, time.Now())
		}
	}
	if len(history) > 0 {
		facts.Files = history[0].Files
		facts.Size = history[0].Size
	}
	return facts
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

	now := time.Now()
	entries := make([]historyEntry, 0, len(history))
	for _, publication := range history {
		entries = append(entries, historyEntry{
			When:  relativeTime(publication.PublishedAt, now),
			ISO:   publication.PublishedAt.Format(time.RFC3339),
			By:    personName(publication.PublishedBy),
			Files: publication.Files,
			Size:  formatBytes(publication.Bytes),
		})
	}
	return entries
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
	s.recordPublication(r.Context(), site)
	if err := s.config.Publisher.DeleteSite(r.Context(), site); err != nil {
		writeServerError(w, err)
		return
	}
	slog.Info("site unpublished in the portal", "site", site, "by", identityName(identity))
	s.recordAnalyticsEvents(r.Context(), []SiteEvent{analyticsEvent(site, "unpublished", time.Now().UTC(), "", personOf(identity))})
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
