package hex

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type adminInventoryCounts struct {
	Sites           int `json:"sites"`
	Apps            int `json:"apps"`
	Artifacts       int `json:"artifacts"`
	Creators        int `json:"creators"`
	UnknownCreators int `json:"unknownCreators"`
}

type adminSiteRow struct {
	siteCard
	Created   string
	CreatorID string
	Owners    string
	OwnerIDs  []string
	Published bool
	CreatedAt time.Time
	TrafficTotals
	LastVisit string
}

type adminUserRow struct {
	AnalyticsPerson
	Label     string
	Created   int
	Owned     int
	First     string
	Last      string
	DetailURL string
	TrafficTotals
}

type AnalyticsView struct {
	Report             AnalyticsReport
	From               string
	Until              string
	Site               string
	Bars               []analyticsBar
	Bytes              string
	ChartWidth         int
	Lifetime           *TrafficTotals
	ShowVisitors       bool
	SiteVisitors       []siteVisitorRow
	AnonymousPageViews int64
	VisitorSearch      string
	VisitorSort        string
	VisitorSortChoices []choice
	VisitorPage        int
	VisitorPages       int
	VisitorTotal       int
	VisitorPrevious    string
	VisitorNext        string
}

type analyticsBar struct {
	Date   string
	Views  int64
	Height string
	X      int
	Y      string
}

type adminView struct {
	Chrome
	AnalyticsView
	Section     string
	Title       string
	QueryText   string
	Sort        string
	SortChoices []choice
	FilterUser  string
	Counts      adminInventoryCounts
	Sites       []adminSiteRow
	Users       []adminUserRow
	Previous    string
	Next        string
	Page        int
	Pages       int
	TotalRows   int
	Collector   *TrafficCollectorStatus
	Failures    uint64
	Global      bool
	SiteDetail  bool
	UserDetail  bool
	FilterURL   string
}

func (s *Server) registerAnalyticsRoutes() {
	if s.config.Analytics == nil || s.config.Identity == nil {
		return
	}
	s.mux.HandleFunc("GET /admin", s.adminPage)
	s.mux.HandleFunc("GET /admin/sites", s.adminPage)
	s.mux.HandleFunc("GET /admin/sites/{site}", s.adminPage)
	s.mux.HandleFunc("GET /admin/users", s.adminPage)
	s.mux.HandleFunc("GET /api/hex/admin/analytics", s.adminAnalytics)
	s.mux.HandleFunc("GET /api/hex/admin/analytics.csv", s.adminAnalyticsCSV)
	s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/analytics", s.siteAnalytics)
}

func (s *Server) adminCaller(w http.ResponseWriter, r *http.Request) (*Identity, bool) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return nil, false
	}
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	if !s.isAdmin(identity) {
		writeError(w, http.StatusForbidden, "platform administrator access required")
		return nil, false
	}
	return identity, true
}

func analyticsQuery(values url.Values, now time.Time) (AnalyticsQuery, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	query := AnalyticsQuery{From: today.AddDate(0, 0, -29), Until: today.AddDate(0, 0, 1), Site: values.Get("site"), User: values.Get("user")}
	if value := values.Get("from"); value != "" {
		at, err := time.Parse(time.DateOnly, value)
		if err != nil {
			return query, errors.New("from must be a date in YYYY-MM-DD format")
		}
		query.From = at
	}
	if value := values.Get("until"); value != "" {
		at, err := time.Parse(time.DateOnly, value)
		if err != nil {
			return query, errors.New("until must be a date in YYYY-MM-DD format")
		}
		query.Until = at.AddDate(0, 0, 1)
	}
	if !query.From.Before(query.Until) || query.Until.Sub(query.From) > 366*24*time.Hour || query.Until.After(today.AddDate(0, 0, 1)) {
		return query, errors.New("select a range of 1–366 days ending no later than today")
	}
	if query.Site != "" && !siteNamePattern.MatchString(query.Site) {
		return query, errors.New("invalid site")
	}
	if len(query.User) > 512 {
		return query, errors.New("invalid user")
	}
	return query, nil
}

func newAnalyticsView(report AnalyticsReport) AnalyticsView {
	view := AnalyticsView{
		Report: report, From: report.Query.From.Format(time.DateOnly), Until: report.Query.Until.AddDate(0, 0, -1).Format(time.DateOnly),
		Site: report.Query.Site, Bytes: formatBytes(report.Traffic.Bytes),
	}
	maximum := int64(1)
	for _, day := range report.Days {
		if day.PageViews > maximum {
			maximum = day.PageViews
		}
	}
	view.ChartWidth = len(report.Days) * 10
	for index, day := range report.Days {
		height := float64(day.PageViews) * 100 / float64(maximum)
		view.Bars = append(view.Bars, analyticsBar{Date: day.Date, Views: day.PageViews, Height: fmt.Sprintf("%.2f", height), X: index * 10, Y: fmt.Sprintf("%.2f", 100-height)})
	}
	return view
}

func (s *Server) adminInventory(ctx context.Context) ([]adminSiteRow, adminInventoryCounts, error) {
	rows := []adminSiteRow{}
	counts := adminInventoryCounts{}
	if s.config.Sites == nil {
		return rows, counts, nil
	}
	names, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		return nil, counts, err
	}
	slices.Sort(names)
	names = slices.Compact(names)
	reader, _ := s.config.Sites.(SiteMetadataReader)
	creators := make(map[string]bool)
	now := time.Now()
	for _, name := range names {
		if !siteNamePattern.MatchString(name) {
			continue
		}
		var metadata *SiteMetadata
		if reader != nil {
			metadata, err = reader.ReadSiteMetadata(ctx, name)
			if err != nil {
				return nil, counts, err
			}
		}
		siteURL, err := s.siteURL(name)
		if err != nil {
			return nil, counts, err
		}
		row := adminSiteRow{siteCard: s.cardForSite(name, siteURL, metadata, now), Published: true}
		if metadata != nil {
			row.CreatedAt = metadata.CreatedAt
			row.Created = analyticsDate(metadata.CreatedAt)
			if metadata.CreatedBy != nil && metadata.CreatedBy.ID != "" {
				row.CreatorID = metadata.CreatedBy.ID
				creators[row.CreatorID] = true
			}
		}
		if row.CreatorID == "" {
			counts.UnknownCreators++
		}
		access, exists, err := s.sitePolicy(ctx, name)
		if err != nil {
			return nil, counts, err
		}
		row.Access, row.AccessTone = audience(access, exists)
		var owners []string
		for _, owner := range s.describePrincipals(ctx, access.Owners) {
			owners = append(owners, owner.Name)
			if owner.Kind == "user" {
				_, id, _ := strings.Cut(owner.Principal, ":")
				row.OwnerIDs = append(row.OwnerIDs, id)
			}
		}
		row.Owners = strings.Join(owners, ", ")
		counts.Sites++
		if metadata != nil && metadata.Kind == KindArtifact {
			counts.Artifacts++
		} else {
			counts.Apps++
		}
		rows = append(rows, row)
	}
	counts.Creators = len(creators)
	// Import server-recorded metadata/history once. Older sites without a
	// verified creator remain unknown; deleted history cannot be reconstructed.
	s.analyticsBootstrap.Lock()
	defer s.analyticsBootstrap.Unlock()
	if !s.analyticsBootstrapped {
		failures := s.analyticsFailures.Load()
		for _, row := range rows {
			if s.config.Publisher == nil {
				break
			}
			s.recordPublication(ctx, row.Name)
			var history []Publication
			if err := s.readRecord(ctx, row.Name, siteHistoryFile, &history); err != nil {
				s.analyticsFailures.Add(1)
				slog.Warn("import site analytics history", "site", row.Name, "error", err)
				continue
			}
			events := make([]SiteEvent, 0, len(history))
			for _, publication := range history {
				events = append(events, analyticsEvent(row.Name, "published", publication.PublishedAt, row.Title, publication.PublishedBy))
			}
			s.recordAnalyticsEvents(ctx, events)
		}
		s.analyticsBootstrapped = failures == s.analyticsFailures.Load()
	}
	return rows, counts, nil
}

func analyticsDate(at time.Time) string {
	if at.IsZero() {
		return "Unknown"
	}
	return at.UTC().Format(time.DateOnly)
}

func analyticsLast(at time.Time) string {
	if at.IsZero() {
		return "Never recorded"
	}
	return relativeTime(at, time.Now())
}

func analyticsLastVisit(at time.Time) string {
	if at.IsZero() {
		return "None in range"
	}
	return relativeTime(at, time.Now())
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	if site := r.PathValue("site"); site != "" {
		values.Set("site", site)
	}
	query, err := analyticsQuery(values, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sites, counts, err := s.adminInventory(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}
	report, err := s.config.Analytics.QueryAnalytics(r.Context(), query)
	if err != nil {
		writeServerError(w, err)
		return
	}
	view := adminView{
		Chrome: s.chromeFor(identity, "admin"), AnalyticsView: newAnalyticsView(report), Counts: counts,
		Section: "overview", Title: "Platform overview", QueryText: strings.TrimSpace(values.Get("q")), Sort: values.Get("sort"),
		FilterUser: query.User, Failures: s.analyticsFailures.Load(), Global: query.Site == "" && query.User == "",
		SiteDetail: query.Site != "", UserDetail: query.User != "",
		FilterURL: r.URL.Path,
	}
	if s.config.TrafficCollector != nil {
		status := s.config.TrafficCollector.Status()
		view.Collector = &status
	}
	if view.Sort == "" {
		view.Sort = "views"
	}
	view.SortChoices = choices(view.Sort, [2]string{"views", "Most page views"}, [2]string{"name", "Name A–Z"}, [2]string{"recent", "Recent activity"})
	if r.URL.Path == "/admin/sites" {
		view.Section, view.Title = "sites", "Sites"
	} else if r.URL.Path == "/admin/users" {
		view.Section, view.Title = "users", "People"
	}
	if query.Site != "" {
		view.Title = "Site analytics · " + query.Site
	}
	view.Sites, view.Users = buildAdminRows(sites, report, values)
	if query.User != "" {
		view.Title = "User analytics · " + query.User
		for _, person := range view.Users {
			if person.ID == query.User {
				view.Title = "User analytics · " + person.Label
			}
		}
	}
	if view.Section == "sites" || view.Section == "users" && query.User == "" {
		total := len(view.Sites)
		if view.Section == "users" {
			total = len(view.Users)
		}
		start, end, err := paginateAdmin(&view, values, r.URL.Path, total)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if view.Section == "sites" {
			view.Sites = view.Sites[start:end]
		} else {
			view.Users = view.Users[start:end]
		}
	} else if view.Global {
		if len(view.Sites) > 10 {
			view.Sites = view.Sites[:10]
		}
		if len(view.Users) > 10 {
			view.Users = view.Users[:10]
		}
	}
	s.writePortalPage(w, "admin", view)
}

func buildAdminRows(sites []adminSiteRow, report AnalyticsReport, values url.Values) ([]adminSiteRow, []adminUserRow) {
	people := make(map[string]*adminUserRow)
	personRow := func(id string) *adminUserRow {
		if people[id] == nil {
			people[id] = &adminUserRow{AnalyticsPerson: AnalyticsPerson{Person: Person{ID: id}}, Label: id}
		}
		return people[id]
	}
	for _, person := range report.People {
		row := personRow(person.ID)
		row.AnalyticsPerson = person
		if person.Name != "" {
			row.Label = person.Name
		} else if person.Email != "" {
			row.Label = person.Email
		}
	}
	siteTraffic := make(map[string]TrafficTotals)
	for _, traffic := range report.Sites {
		siteTraffic[traffic.Key] = traffic.TrafficTotals
	}
	for _, traffic := range report.Users {
		personRow(traffic.Key).TrafficTotals = traffic.TrafficTotals
	}
	knownSites := make(map[string]bool)
	for index := range sites {
		row := &sites[index]
		knownSites[row.Name] = true
		row.TrafficTotals = siteTraffic[row.Name]
		row.LastVisit = analyticsLastVisit(row.LastVisited)
		if row.CreatorID != "" {
			personRow(row.CreatorID).Created++
		}
		for _, id := range row.OwnerIDs {
			personRow(id).Owned++
		}
	}
	for _, traffic := range report.Sites {
		if !knownSites[traffic.Key] {
			sites = append(sites, adminSiteRow{siteCard: siteCard{Name: traffic.Key, Title: traffic.Key}, TrafficTotals: traffic.TrafficTotals, LastVisit: analyticsLastVisit(traffic.LastVisited)})
			knownSites[traffic.Key] = true
		}
	}
	for _, event := range report.Events {
		if !knownSites[event.Site] {
			title := event.Title
			if title == "" {
				title = event.Site
			}
			sites = append(sites, adminSiteRow{siteCard: siteCard{Name: event.Site, Title: title}, LastVisit: "None in range"})
			knownSites[event.Site] = true
		}
		if event.Actor != nil {
			row := personRow(event.Actor.ID)
			if row.Name == "" && event.Actor.Name != "" {
				row.Label = event.Actor.Name
			}
		}
	}
	filteredSites := []adminSiteRow{}
	search := values.Get("q")
	for _, site := range sites {
		if report.Query.Site != "" && report.Query.Site != site.Name {
			continue
		}
		if report.Query.User != "" && site.CreatorID != report.Query.User && !slices.Contains(site.OwnerIDs, report.Query.User) && site.Requests == 0 {
			continue
		}
		if search != "" && !containsFold(site.Title+" "+site.Name+" "+site.CreatedBy+" "+site.Owners, search) {
			continue
		}
		filteredSites = append(filteredSites, site)
	}
	users := []adminUserRow{}
	for id, row := range people {
		if report.Query.User != "" && report.Query.User != id || search != "" && !containsFold(row.Label+" "+row.Email+" "+id, search) {
			continue
		}
		row.First, row.Last = analyticsDate(row.FirstSeen), analyticsLast(row.LastSeen)
		link := url.Values{"user": {id}, "from": {report.Query.From.Format(time.DateOnly)}, "until": {report.Query.Until.AddDate(0, 0, -1).Format(time.DateOnly)}}
		row.DetailURL = "/admin/users?" + link.Encode()
		users = append(users, *row)
	}
	sort := values.Get("sort")
	slices.SortFunc(filteredSites, func(a, b adminSiteRow) int {
		if sort == "name" {
			return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		}
		if sort == "recent" {
			return b.Timestamp.Compare(a.Timestamp)
		}
		if a.PageViews != b.PageViews {
			return compareDescending(a.PageViews, b.PageViews)
		}
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(users, func(a, b adminUserRow) int {
		if sort == "name" {
			return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
		}
		if sort == "recent" {
			return b.LastSeen.Compare(a.LastSeen)
		}
		if a.PageViews != b.PageViews {
			return compareDescending(a.PageViews, b.PageViews)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return filteredSites, users
}

func compareDescending(a, b int64) int {
	if a > b {
		return -1
	}
	return 1
}

func paginateAdmin(view *adminView, values url.Values, path string, total int) (int, int, error) {
	page := 1
	if value := values.Get("page"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return 0, 0, errors.New("page must be a positive integer")
		}
		page = parsed
	}
	pages := max(1, (total+managePageSize-1)/managePageSize)
	page = min(page, pages)
	view.Page, view.Pages, view.TotalRows = page, pages, total
	link := func(number int) string {
		values.Set("page", strconv.Itoa(number))
		return path + "?" + values.Encode()
	}
	if page > 1 {
		view.Previous = link(page - 1)
	}
	if page < pages {
		view.Next = link(page + 1)
	}
	start := (page - 1) * managePageSize
	return start, min(total, start+managePageSize), nil
}

func (s *Server) adminAnalytics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminCaller(w, r); !ok {
		return
	}
	query, err := analyticsQuery(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_, counts, err := s.adminInventory(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}
	report, err := s.config.Analytics.QueryAnalytics(r.Context(), query)
	if err != nil {
		writeServerError(w, err)
		return
	}
	var collector *TrafficCollectorStatus
	if s.config.TrafficCollector != nil {
		status := s.config.TrafficCollector.Status()
		collector = &status
	}
	writeJSON(w, http.StatusOK, struct {
		AnalyticsReport
		Inventory     adminInventoryCounts    `json:"inventory"`
		Collector     *TrafficCollectorStatus `json:"collector,omitempty"`
		WriteFailures uint64                  `json:"writeFailures"`
	}{report, counts, collector, s.analyticsFailures.Load()})
}

func (s *Server) siteAnalytics(w http.ResponseWriter, r *http.Request) {
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	view, err := s.loadSiteAnalytics(r.Context(), site, r.URL.Query())
	if err != nil {
		writeSiteAnalyticsError(w, err)
		return
	}
	s.renderFragment(w, "site-analytics", view)
}

func (s *Server) adminAnalyticsCSV(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminCaller(w, r); !ok {
		return
	}
	query, err := analyticsQuery(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, _, err := s.adminInventory(r.Context()); err != nil {
		writeServerError(w, err)
		return
	}
	report, err := s.config.Analytics.QueryAnalytics(r.Context(), query)
	if err != nil {
		writeServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hex-analytics.csv"`)
	writer := csv.NewWriter(w)
	rows := [][]string{{"date_utc", "page_views", "unique_visitors", "visits", "requests", "http_errors", "bytes", "sites_created", "publications", "unpublished", "new_users"}}
	for _, day := range report.Days {
		rows = append(rows, []string{
			day.Date, strconv.FormatInt(day.PageViews, 10), strconv.Itoa(day.Visitors), strconv.FormatInt(day.Visits, 10),
			strconv.FormatInt(day.Requests, 10), strconv.FormatInt(day.Errors, 10), strconv.FormatInt(day.Bytes, 10),
			strconv.Itoa(day.Created), strconv.Itoa(day.Publications), strconv.Itoa(day.Unpublished), strconv.Itoa(day.NewUsers),
		})
	}
	if err := writer.WriteAll(rows); err != nil {
		slog.Error("write analytics CSV", "error", err)
	}
}
