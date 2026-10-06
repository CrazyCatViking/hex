package hex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
)

// AnalyticsStore owns platform statistics, independently of app documents.
// Writes must be concurrency-safe; event IDs make retries idempotent. Traffic
// is reduced to daily per-site/per-user buckets, not retained request bodies.
// Additional analytics sources can implement their own optional interfaces.
type AnalyticsStore interface {
	ObservePerson(context.Context, Person, time.Time) error
	RecordSiteEvents(context.Context, []SiteEvent) error
	RecordTraffic(context.Context, []TrafficEvent) error
	QueryAnalytics(context.Context, AnalyticsQuery) (AnalyticsReport, error)
}

// SiteTrafficReader supplies all recorded traffic for specific sites, without
// returning visitor identities. Portals call it only for authorized listings.
type SiteTrafficReader interface {
	SiteTraffic(context.Context, []string) ([]AnalyticsRow, error)
}

// SiteRecordReader reads server-owned metadata/history without granting writes.
// Site directories and publishers may implement it independently.
type SiteRecordReader interface {
	ReadSiteFile(context.Context, string, string) (io.ReadCloser, error)
}

// AnalyticsSummaryReader omits inventories, visitor identities and event payloads.
// It retains headline counts and daily trends for comparisons and site reports.
type AnalyticsSummaryReader interface {
	QueryAnalyticsSummary(context.Context, AnalyticsQuery) (AnalyticsReport, error)
}

type AnalyticsVisitorsQuery struct {
	AnalyticsQuery
	Search string
	Sort   string
	Page   int
	Limit  int
}

type AnalyticsVisitorsPage struct {
	Rows               []AnalyticsRow
	Total              int
	Page               int
	AnonymousPageViews int64
	PeakPageViews      int64
}

// AnalyticsVisitorReader filters and paginates actual page visitors in storage.
type AnalyticsVisitorReader interface {
	QueryAnalyticsVisitors(context.Context, AnalyticsVisitorsQuery) (AnalyticsVisitorsPage, error)
}

func (s *Server) analyticsSummary(ctx context.Context, query AnalyticsQuery) (AnalyticsReport, error) {
	if reader, ok := s.config.Analytics.(AnalyticsSummaryReader); ok {
		return reader.QueryAnalyticsSummary(ctx, query)
	}
	return s.config.Analytics.QueryAnalytics(ctx, query)
}

type AnalyticsPerson struct {
	Person
	FirstSeen time.Time `json:"firstSeen,omitzero"`
	LastSeen  time.Time `json:"lastSeen"`
}

type SiteEvent struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Site  string    `json:"site"`
	Kind  string    `json:"kind"`
	Title string    `json:"title,omitempty"`
	Actor *Person   `json:"actor,omitempty"`
}

type TrafficEvent struct {
	ID             string
	At             time.Time
	Site           string
	UserID         string
	PageView       bool
	Status         int
	Bytes          int64
	DurationMillis int64
}

// AnalyticsQuery uses UTC calendar days and an exclusive Until bound. User
// filters concern the actor/visitor, not the site's current owners.
type AnalyticsQuery struct {
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
	Site  string    `json:"site,omitempty"`
	User  string    `json:"user,omitempty"`
}

type TrafficTotals struct {
	Requests       int64     `json:"requests"`
	PageViews      int64     `json:"pageViews"`
	Visits         int64     `json:"visits"`
	Visitors       int       `json:"visitors"`
	Errors         int64     `json:"errors"`
	Bytes          int64     `json:"bytes"`
	DurationMillis int64     `json:"durationMillis"`
	LastVisited    time.Time `json:"lastVisited,omitzero"`
}

func (t TrafficTotals) AverageMillis() int64 {
	if t.Requests == 0 {
		return 0
	}
	return t.DurationMillis / t.Requests
}

type TrafficBucket struct {
	Day    time.Time
	Site   string
	UserID string
	TrafficTotals
}

type AnalyticsRow struct {
	Key    string  `json:"key"`
	Person *Person `json:"person,omitempty"`
	TrafficTotals
}

// SummarizeSiteTraffic unions visitors across daily buckets for each site.
func SummarizeSiteTraffic(buckets []TrafficBucket) []AnalyticsRow {
	sites := make(map[string]*AnalyticsRow)
	visitors := make(map[string]map[string]bool)
	for _, bucket := range buckets {
		if sites[bucket.Site] == nil {
			sites[bucket.Site] = &AnalyticsRow{Key: bucket.Site}
			visitors[bucket.Site] = make(map[string]bool)
		}
		addTraffic(&sites[bucket.Site].TrafficTotals, bucket.TrafficTotals)
		if bucket.PageViews > 0 && bucket.UserID != "" {
			visitors[bucket.Site][bucket.UserID] = true
		}
	}
	rows := make([]AnalyticsRow, 0, len(sites))
	for site, row := range sites {
		row.Visitors = len(visitors[site])
		rows = append(rows, *row)
	}
	slices.SortFunc(rows, func(a, b AnalyticsRow) int { return strings.Compare(a.Key, b.Key) })
	return rows
}

type AnalyticsDay struct {
	Date         string `json:"date"`
	Created      int    `json:"created"`
	Publications int    `json:"publications"`
	Unpublished  int    `json:"unpublished"`
	NewUsers     int    `json:"newUsers"`
	TrafficTotals
}

type AnalyticsReport struct {
	Query         AnalyticsQuery    `json:"query"`
	KnownUsers    int               `json:"knownUsers"`
	Active7Days   int               `json:"active7Days"`
	Active30Days  int               `json:"active30Days"`
	NewUsers      int               `json:"newUsers"`
	Created       int               `json:"created"`
	Publications  int               `json:"publications"`
	Unpublished   int               `json:"unpublished"`
	Traffic       TrafficTotals     `json:"traffic"`
	People        []AnalyticsPerson `json:"people"`
	Sites         []AnalyticsRow    `json:"sites"`
	Users         []AnalyticsRow    `json:"users"`
	Days          []AnalyticsDay    `json:"days"`
	Events        []SiteEvent       `json:"events"`
	LastCollected time.Time         `json:"lastCollected,omitzero"`
}

// SummarizeAnalytics is shared by storage implementations. Buckets and events
// are already filtered by the query. Distinct visitors are unioned across days
// and sites, rather than summing daily unique counts.
func SummarizeAnalytics(query AnalyticsQuery, people []AnalyticsPerson, events []SiteEvent, buckets []TrafficBucket, lastCollected time.Time) AnalyticsReport {
	return summarizeAnalytics(query, people, events, buckets, lastCollected, false)
}

func SummarizeAnalyticsSummary(query AnalyticsQuery, people []AnalyticsPerson, events []SiteEvent, buckets []TrafficBucket, lastCollected time.Time) AnalyticsReport {
	return summarizeAnalytics(query, people, events, buckets, lastCollected, true)
}

func summarizeAnalytics(query AnalyticsQuery, people []AnalyticsPerson, events []SiteEvent, buckets []TrafficBucket, lastCollected time.Time, summary bool) AnalyticsReport {
	report := AnalyticsReport{Query: query, People: people, LastCollected: lastCollected}
	if summary {
		report.People = nil
	}
	days := make(map[string]*AnalyticsDay)
	for at := query.From; at.Before(query.Until); at = at.AddDate(0, 0, 1) {
		date := at.Format(time.DateOnly)
		days[date] = &AnalyticsDay{Date: date}
	}
	now := time.Now().UTC()
	for _, person := range people {
		report.KnownUsers++
		if !person.LastSeen.Before(now.AddDate(0, 0, -7)) {
			report.Active7Days++
		}
		if !person.LastSeen.Before(now.AddDate(0, 0, -30)) {
			report.Active30Days++
		}
		if day := days[person.FirstSeen.UTC().Format(time.DateOnly)]; day != nil {
			report.NewUsers++
			day.NewUsers++
		}
	}
	for _, event := range events {
		day := days[event.At.UTC().Format(time.DateOnly)]
		if day == nil {
			continue
		}
		switch event.Kind {
		case "created":
			report.Created++
			day.Created++
		case "published":
			report.Publications++
			day.Publications++
		case "unpublished":
			report.Unpublished++
			day.Unpublished++
		}
	}
	sites := make(map[string]*AnalyticsRow)
	users := make(map[string]*AnalyticsRow)
	visitors := make(map[string]map[string]bool)
	addVisitor := func(key, user string) {
		if user == "" {
			return
		}
		if visitors[key] == nil {
			visitors[key] = make(map[string]bool)
		}
		visitors[key][user] = true
	}
	for _, bucket := range buckets {
		date := bucket.Day.UTC().Format(time.DateOnly)
		day := days[date]
		if day == nil {
			continue
		}
		if !summary && sites[bucket.Site] == nil {
			sites[bucket.Site] = &AnalyticsRow{Key: bucket.Site}
		}
		addTraffic(&report.Traffic, bucket.TrafficTotals)
		addTraffic(&day.TrafficTotals, bucket.TrafficTotals)
		if !summary {
			addTraffic(&sites[bucket.Site].TrafficTotals, bucket.TrafficTotals)
		}
		if !summary && bucket.UserID != "" {
			if users[bucket.UserID] == nil {
				users[bucket.UserID] = &AnalyticsRow{Key: bucket.UserID}
			}
			addTraffic(&users[bucket.UserID].TrafficTotals, bucket.TrafficTotals)
		}
		if bucket.PageViews > 0 {
			addVisitor("all", bucket.UserID)
			addVisitor("day:"+date, bucket.UserID)
			if !summary {
				addVisitor("site:"+bucket.Site, bucket.UserID)
			}
		}
	}
	report.Traffic.Visitors = len(visitors["all"])
	for key, row := range sites {
		row.Visitors = len(visitors["site:"+key])
		report.Sites = append(report.Sites, *row)
	}
	for _, row := range users {
		if row.PageViews > 0 {
			row.Visitors = 1
		}
		report.Users = append(report.Users, *row)
	}
	for date, day := range days {
		day.Visitors = len(visitors["day:"+date])
		report.Days = append(report.Days, *day)
	}
	slices.SortFunc(report.Days, func(a, b AnalyticsDay) int { return strings.Compare(a.Date, b.Date) })
	sortRows := func(a, b AnalyticsRow) int {
		if a.PageViews != b.PageViews {
			if a.PageViews > b.PageViews {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	}
	slices.SortFunc(report.Sites, sortRows)
	slices.SortFunc(report.Users, sortRows)
	if !summary {
		slices.SortFunc(events, func(a, b SiteEvent) int {
			if at := b.At.Compare(a.At); at != 0 {
				return at
			}
			return strings.Compare(a.ID, b.ID)
		})
		report.Events = events
	}
	if len(report.Events) > 50 {
		report.Events = report.Events[:50]
	}
	return report
}

// PaginateAnalyticsVisitors is shared by in-memory stores and custom providers.
func PaginateAnalyticsVisitors(report AnalyticsReport, query AnalyticsVisitorsQuery) AnalyticsVisitorsPage {
	page := AnalyticsVisitorsPage{AnonymousPageViews: report.Traffic.PageViews}
	label := func(row AnalyticsRow) string {
		if row.Person != nil {
			if row.Person.Name != "" {
				return row.Person.Name
			}
			if row.Person.Email != "" {
				return row.Person.Email
			}
		}
		return row.Key
	}
	for _, row := range report.Users {
		page.AnonymousPageViews -= row.PageViews
		page.PeakPageViews = max(page.PeakPageViews, row.PageViews)
		if row.PageViews == 0 || row.Key == "" {
			continue
		}
		email := ""
		if row.Person != nil {
			email = row.Person.Email
		}
		if query.Search != "" && !strings.Contains(strings.ToLower(label(row)+" "+email+" "+row.Key), strings.ToLower(query.Search)) {
			continue
		}
		page.Rows = append(page.Rows, row)
	}
	slices.SortFunc(page.Rows, func(a, b AnalyticsRow) int {
		switch query.Sort {
		case "recent":
			if !a.LastVisited.Equal(b.LastVisited) {
				return b.LastVisited.Compare(a.LastVisited)
			}
		case "visits":
			if a.Visits != b.Visits {
				return compareDescending(a.Visits, b.Visits)
			}
		case "name":
		default:
			if a.PageViews != b.PageViews {
				return compareDescending(a.PageViews, b.PageViews)
			}
		}
		if name := strings.Compare(strings.ToLower(label(a)), strings.ToLower(label(b))); name != 0 {
			return name
		}
		return strings.Compare(a.Key, b.Key)
	})
	page.Total = len(page.Rows)
	limit := max(1, min(query.Limit, 100))
	page.Page = min(max(1, query.Page), max(1, (page.Total+limit-1)/limit))
	start := (page.Page - 1) * limit
	page.Rows = page.Rows[start:min(start+limit, page.Total)]
	return page
}

func addTraffic(total *TrafficTotals, value TrafficTotals) {
	total.Requests += value.Requests
	total.PageViews += value.PageViews
	total.Visits += value.Visits
	total.Errors += value.Errors
	total.Bytes += value.Bytes
	total.DurationMillis += value.DurationMillis
	if value.LastVisited.After(total.LastVisited) {
		total.LastVisited = value.LastVisited
	}
}

func analyticsEvent(site, kind string, at time.Time, title string, actor *Person) SiteEvent {
	id := sha256.Sum256([]byte(site + "\x00" + kind + "\x00" + at.UTC().Format(time.RFC3339Nano)))
	return SiteEvent{ID: fmt.Sprintf("%x", id), Site: site, Kind: kind, At: at, Title: title, Actor: actor}
}

func (s *Server) recordPublication(ctx context.Context, site string) {
	if s.config.Analytics == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.recoverPublicationHistory(ctx, site); err != nil {
		s.analyticsFailures.Add(1)
		slog.Error("recover publication analytics", "site", site, "error", err)
	}
}

// recoverPublicationHistory must succeed before deleting the site's records.
// Stable event IDs make retries and repeated publication imports idempotent.
func (s *Server) recoverPublicationHistory(ctx context.Context, site string) error {
	if s.config.Analytics == nil {
		return nil
	}
	var metadata SiteMetadata
	reader, _ := s.config.Publisher.(SiteRecordReader)
	if reader != nil {
		if err := readAnalyticsRecord(ctx, reader, site, siteMetadataFile, &metadata); err != nil {
			return err
		}
	} else if metadataReader, ok := s.config.Sites.(SiteMetadataReader); ok {
		value, err := metadataReader.ReadSiteMetadata(ctx, site)
		if err != nil {
			return fmt.Errorf("read analytics metadata: %w", err)
		}
		if value != nil {
			metadata = *value
		}
	} else if siteReader, ok := s.config.Sites.(SiteRecordReader); ok {
		reader = siteReader
		if err := readAnalyticsRecord(ctx, reader, site, siteMetadataFile, &metadata); err != nil {
			return err
		}
	}
	if reader == nil {
		reader, _ = s.config.Sites.(SiteRecordReader)
	}
	var history []Publication
	if reader != nil {
		if err := readAnalyticsRecord(ctx, reader, site, siteHistoryFile, &history); err != nil {
			return err
		}
	}
	events := []SiteEvent{}
	if !metadata.CreatedAt.IsZero() {
		events = append(events, analyticsEvent(site, "created", metadata.CreatedAt, metadata.Title, metadata.CreatedBy))
	}
	if !metadata.PublishedAt.IsZero() {
		events = append(events, analyticsEvent(site, "published", metadata.PublishedAt, metadata.Title, metadata.PublishedBy))
	}
	for _, publication := range history {
		if !publication.PublishedAt.IsZero() {
			events = append(events, analyticsEvent(site, "published", publication.PublishedAt, metadata.Title, publication.PublishedBy))
		}
	}
	if len(events) == 0 {
		return nil
	}
	if err := s.config.Analytics.RecordSiteEvents(ctx, events); err != nil {
		return fmt.Errorf("recover publication history for %s: %w", site, err)
	}
	return nil
}

func readAnalyticsRecord(ctx context.Context, reader SiteRecordReader, site, path string, value any) error {
	file, err := reader.ReadSiteFile(ctx, site, path)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read analytics %s: %w", path, err)
	}
	defer closeReader(file, path)
	if err := json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(value); err != nil {
		return fmt.Errorf("decode analytics %s: %w", path, err)
	}
	return nil
}

func (s *Server) recordAnalyticsEvents(ctx context.Context, events []SiteEvent) {
	if s.config.Analytics == nil || len(events) == 0 {
		return
	}
	// The publication/deletion already completed. Preserve its event even when
	// the client disconnected while storage was being updated.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.config.Analytics.RecordSiteEvents(ctx, events); err != nil {
		s.analyticsFailures.Add(1)
		slog.Error("record site analytics", "error", err)
	}
}

// The identity is encoded for internal proxy headers, and hidden by NGINX.
// Analytics never accepts a caller-supplied user ID.
func (s *Server) analyticsIdentity(w http.ResponseWriter, identity *Identity) {
	if s.config.Analytics != nil && identity != nil && identity.ID != "" {
		w.Header().Set("X-Hex-Analytics-User", base64.RawURLEncoding.EncodeToString([]byte(identity.ID)))
	}
}
