package hex

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"hash/fnv"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
)

//go:embed portal/* installers/*
var platformAssets embed.FS

var portalTemplates = template.Must(template.ParseFS(platformAssets, "portal/*.html"))

const (
	homeSectionSize    = 6
	portalPolicyHeader = "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
)

// Chrome is what every portal page shows around its content: the platform,
// the active section and the signed-in viewer.
type Chrome struct {
	Platform    string
	Active      string
	Viewer      *viewerView
	Manage      bool
	Analytics   bool
	Connections bool
}

type viewerView struct {
	Name      string
	FirstName string
	Email     string
	Initials  string
	Tone      string
	Groups    []string
	Admin     bool
}

// siteCard is how sites and artifacts appear across the portal.
type siteCard struct {
	Traffic     *TrafficTotals `json:"traffic,omitempty"`
	IconURL     string         `json:"iconURL,omitempty"`
	Name        string         `json:"name"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Kind        string         `json:"kind"`
	Access      string         `json:"audience"`
	AccessTone  string         `json:"-"`
	CreatedBy   string         `json:"createdBy,omitempty"`
	PublishedBy string         `json:"publishedBy,omitempty"`
	Timestamp   time.Time      `json:"publishedAt,omitzero"`
	Updated     string         `json:"-"`
	UpdatedISO  string         `json:"-"`
	Initial     string         `json:"-"`
	Tone        string         `json:"-"`
	Owned       bool           `json:"-"`
}

type siteStatistics struct {
	Sites           int `json:"sites"`
	Authors         int `json:"authors"`
	UpdatedRecently int `json:"updatedRecently"`
}

type catalogView struct {
	Sites          []siteCard
	Statistics     siteStatistics
	Search         string
	Sort           string
	SortChoices    []choice
	TrafficEnabled bool
	TrafficFilter  string
	TrafficChoices []choice
	Filtered       bool
}

// choice is an option in a select. Attr carries "selected" so templates need
// no conditions inside tags.
type choice struct {
	Value string
	Label string
	Attr  template.HTMLAttr
}

func choices(selected string, values ...[2]string) []choice {
	options := make([]choice, 0, len(values))
	for _, value := range values {
		option := choice{Value: value[0], Label: value[1]}
		if value[0] == selected {
			option.Attr = "selected"
		}
		options = append(options, option)
	}
	return options
}

// flag renders a boolean attribute, such as hidden or open, when set.
func flag(name template.HTMLAttr, set bool) template.HTMLAttr {
	if set {
		return name
	}
	return ""
}

type homeView struct {
	Chrome
	Shared              []siteCard
	SharedMore          int
	Mine                []siteCard
	MineMore            int
	Catalog             catalogView
	InstallersAvailable bool
}

type startView struct {
	Chrome
	InstallersAvailable bool
}

func (s *Server) chromeFor(identity *Identity, active string) Chrome {
	view := Chrome{
		Platform: s.platformName(), Active: active, Manage: s.manageEnabled() && identity != nil,
		Analytics: s.config.Analytics != nil, Connections: s.connectionsEnabled() && identity != nil,
	}
	if identity == nil {
		return view
	}

	name := identity.Name
	if name == "" {
		name = identity.Email
	}
	if name == "" {
		name = identity.ID
	}
	viewer := &viewerView{
		Name:      name,
		FirstName: strings.Fields(name + " ")[0],
		Email:     identity.Email,
		Initials:  initials(name),
		Tone:      tone(identity.ID),
		Admin:     s.isAdmin(identity),
	}
	for _, group := range s.config.Groups {
		if identity.matchesAny([]string{"group:" + group.ID}) {
			viewer.Groups = append(viewer.Groups, group.Name)
		}
	}
	view.Viewer = viewer
	return view
}

func (s *Server) writePortalPage(w http.ResponseWriter, name string, view any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", portalPolicyHeader)
	w.Header().Set("Referrer-Policy", "same-origin")
	s.renderFragment(w, name, view)
}

// landingPage is the platform home: what is shared with the viewer, what
// they own, and the catalogue of apps for everyone.
func (s *Server) landingPage(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	identity := s.requestIdentity(r)
	catalog, err := s.catalogView(r.Context(), identity, r.URL.Query())
	if err != nil {
		writeServerError(w, err)
		return
	}

	view := homeView{Chrome: s.chromeFor(identity, "home"), Catalog: catalog, InstallersAvailable: s.installersAvailable()}
	if view.Manage {
		mine, shared, err := s.personalSites(r.Context(), identity, false)
		if err != nil {
			writeServerError(w, err)
			return
		}
		view.Mine, view.MineMore = firstCards(mine)
		view.Shared, view.SharedMore = firstCards(shared)
	}
	s.writePortalPage(w, "home", view)
}

func firstCards(cards []siteCard) ([]siteCard, int) {
	if len(cards) <= homeSectionSize {
		return cards, 0
	}
	return cards[:homeSectionSize], len(cards) - homeSectionSize
}

// startPage explains how to build and publish, with the CLI installers.
func (s *Server) startPage(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	view := startView{Chrome: s.chromeFor(s.requestIdentity(r), "start"), InstallersAvailable: s.installersAvailable()}
	s.writePortalPage(w, "start", view)
}

func (s *Server) installersAvailable() bool {
	_, connectionError := s.connectionSettings()
	_, releaseError := s.cliReleaseURL()
	return connectionError == nil && releaseError == nil
}

// catalog renders the catalogue for HTMX search and sorting.
func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	view, err := s.catalogView(r.Context(), s.requestIdentity(r), r.URL.Query())
	if err != nil {
		writeServerError(w, err)
		return
	}
	s.renderFragment(w, "catalog", view)
}

func (s *Server) catalogView(ctx context.Context, viewer *Identity, query url.Values) (catalogView, error) {
	sites, err := s.discoverSites(ctx, viewer)
	if err != nil {
		return catalogView{}, err
	}
	view := catalogView{
		Statistics:     summarizeSites(sites, time.Now()),
		Search:         query.Get("search"),
		Sort:           query.Get("sort"),
		TrafficEnabled: s.cardTrafficEnabled(),
		TrafficFilter:  query.Get("traffic"),
	}
	if view.Sort == "" {
		view.Sort = "recent"
	}
	if view.TrafficFilter == "" {
		view.TrafficFilter = "all"
	}
	view.SortChoices = s.cardSortChoices(view.Sort)
	view.TrafficChoices = cardTrafficChoices(view.TrafficFilter)
	view.Filtered = strings.TrimSpace(view.Search) != "" || view.TrafficEnabled && view.TrafficFilter != "all"
	search := strings.ToLower(strings.TrimSpace(view.Search))
	now := time.Now()
	for _, site := range sites {
		card := s.cardForSite(site.Name, site.URL, site.Metadata, now)
		if card.Description == "" {
			card.Description = "A tool built by your team."
		}
		text := strings.ToLower(strings.Join([]string{card.Name, card.Title, card.Description, card.CreatedBy}, " "))
		if strings.Contains(text, search) {
			view.Sites = append(view.Sites, card)
		}
	}
	s.fillCardTraffic(ctx, view.Sites)
	if view.TrafficEnabled {
		view.Sites = slices.DeleteFunc(view.Sites, func(card siteCard) bool { return !matchesTraffic(card, view.TrafficFilter) })
	}
	sortSiteCards(view.Sites, view.Sort)
	return view, nil
}

func cardFromMetadata(name, siteURL string, metadata *SiteMetadata, now time.Time) siteCard {
	card := siteCard{Name: name, URL: siteURL, Title: name, Kind: "App", Tone: tone(name)}
	if metadata != nil {
		if metadata.Title != "" {
			card.Title = metadata.Title
		}
		card.Description = metadata.Description
		if metadata.Kind == KindArtifact {
			card.Kind = "File"
		}
		card.CreatedBy = metadata.DisplayAuthor()
		card.PublishedBy = personName(metadata.PublishedBy)
		card.Timestamp = metadata.PublishedAt
	}
	card.Initial = string([]rune(initials(card.Title))[:1])
	if !card.Timestamp.IsZero() {
		card.Updated = relativeTime(card.Timestamp, now)
		card.UpdatedISO = card.Timestamp.Format(time.RFC3339)
	}
	return card
}

// personalSites lists the sites the viewer owns (or every site, for admins
// asking for all) and the sites explicitly shared with them, newest first.
func (s *Server) personalSites(ctx context.Context, identity *Identity, all bool) ([]siteCard, []siteCard, error) {
	names, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		return nil, nil, err
	}
	reader, _ := s.config.Sites.(SiteMetadataReader)
	now := time.Now()

	var mine, shared []siteCard
	for _, name := range names {
		if !siteNamePattern.MatchString(name) {
			continue
		}
		access, exists, err := s.sitePolicy(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		owned := exists && identity.matchesAny(access.Owners)
		sharedWithViewer := exists && !owned &&
			((len(access.Viewers) > 0 && identity.matchesAny(access.Viewers)) || identity.matchesAny(access.Editors))
		if !owned && !sharedWithViewer && !all {
			continue
		}

		var metadata *SiteMetadata
		if reader != nil {
			if metadata, err = reader.ReadSiteMetadata(ctx, name); err != nil {
				slog.Warn("unreadable site metadata", "site", name, "error", err)
			}
		}
		siteURL, _ := s.siteURL(name)
		card := s.cardForSite(name, siteURL, metadata, now)
		card.Access, card.AccessTone = audience(access, exists)
		card.Owned = owned || s.isAdmin(identity)

		if owned || all {
			mine = append(mine, card)
		} else {
			shared = append(shared, card)
		}
	}

	s.fillCardTraffic(ctx, mine, shared)
	sortSiteCards(mine, "recent")
	sortSiteCards(shared, "recent")
	return mine, shared, nil
}

// audience summarizes who can open a site.
func audience(access SiteAccess, exists bool) (string, string) {
	switch {
	case !exists || len(access.Viewers) == 0:
		return "Everyone", "everyone"
	case !slices.ContainsFunc(access.Viewers, func(viewer string) bool { return !slices.Contains(access.Owners, viewer) }):
		return "Only owners", "private"
	default:
		others := 0
		for _, viewer := range access.Viewers {
			if !slices.Contains(access.Owners, viewer) {
				others++
			}
		}
		return fmt.Sprintf("Shared with %d", others), "shared"
	}
}

// initials are up to two letters for avatars: first and last word.
func initials(name string) string {
	name = strings.TrimSpace(name)
	if at := strings.Index(name, "@"); at > 0 && !strings.Contains(name, " ") {
		name = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(name[:at])
	}
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		return "?"
	}
	first := []rune(words[0])[:1]
	if len(words) == 1 {
		return strings.ToUpper(string(first))
	}
	last := []rune(words[len(words)-1])[:1]
	return strings.ToUpper(string(first) + string(last))
}

// tone picks one of the avatar colours, stable per value.
func tone(value string) string {
	hash := fnv.New32a()
	hash.Write([]byte(value))
	return fmt.Sprintf("tone-%d", hash.Sum32()%6)
}

func relativeTime(t, now time.Time) string {
	elapsed := now.Sub(t)
	switch {
	case elapsed < 0:
		return t.Format("Jan 2, 2006")
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return plural(int(elapsed.Minutes()), "minute") + " ago"
	case elapsed < 24*time.Hour:
		return plural(int(elapsed.Hours()), "hour") + " ago"
	case elapsed < 48*time.Hour:
		return "yesterday"
	case elapsed < 30*24*time.Hour:
		return plural(int(elapsed.Hours()/24), "day") + " ago"
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan 2, 2006")
	}
}

func plural(count int, unit string) string {
	if count == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", count, unit)
}

func (s *Server) platformName() string {
	if s.config.Connection != nil && strings.TrimSpace(s.config.Connection.Name) != "" {
		return s.config.Connection.Name
	}
	return "Hex"
}

func (s *Server) platformHost(host string) bool {
	origins := []string{s.config.SiteBaseURL}
	if s.config.Connection != nil {
		origins = append(origins, s.config.Connection.Server)
	}
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err == nil && strings.EqualFold(host, parsed.Host) {
			return true
		}
	}
	return false
}

func (s *Server) portalAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/hex/")
	http.ServeFileFS(w, r, platformAssets, "portal/"+name)
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	sites, err := s.discoverSites(r.Context(), s.requestIdentity(r))
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Name                string         `json:"name"`
		Sites               []Site         `json:"sites"`
		Statistics          siteStatistics `json:"statistics"`
		InstallersAvailable bool           `json:"installersAvailable"`
	}{s.platformName(), sites, summarizeSites(sites, time.Now()), s.installersAvailable()})
}

func summarizeSites(sites []Site, now time.Time) siteStatistics {
	statistics := siteStatistics{Sites: len(sites)}
	authors := make(map[string]bool)
	since := now.AddDate(0, 0, -30)
	for _, site := range sites {
		if site.Metadata == nil {
			continue
		}
		author := strings.ToLower(strings.TrimSpace(site.Metadata.DisplayAuthor()))
		if site.Metadata.CreatedBy != nil {
			author = site.Metadata.CreatedBy.ID
		}
		if author != "" {
			authors[author] = true
		}
		published := site.Metadata.PublishedAt
		if !published.Before(since) && !published.After(now) {
			statistics.UpdatedRecently++
		}
	}
	statistics.Authors = len(authors)
	return statistics
}

// renderFragment executes a portal template; pages add their headers first.
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
