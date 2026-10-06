package hex

import (
	"context"
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

type siteVisitorRow struct {
	ID       string
	Name     string
	Email    string
	Last     string
	LastISO  string
	Initials string
	Tone     string
	Views    string
	ViewsBar string
	TrafficTotals
}

type siteAnalyticsInputError struct{ error }

func writeSiteAnalyticsError(w http.ResponseWriter, err error) {
	var inputError *siteAnalyticsInputError
	if errors.As(err, &inputError) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeServerError(w, err)
}

// Authorization happens in manageCaller before this helper. Site and user query
// parameters cannot expand the report beyond the managed site.
func (s *Server) loadSiteAnalytics(ctx context.Context, site string, values url.Values) (AnalyticsView, error) {
	values.Set("site", site)
	values.Del("user")
	query, err := analyticsQuery(values, time.Now())
	if err != nil {
		return AnalyticsView{}, &siteAnalyticsInputError{err}
	}
	visitorReader, paged := s.config.Analytics.(AnalyticsVisitorReader)
	var report AnalyticsReport
	if paged {
		report, err = s.analyticsSummary(ctx, query)
	} else {
		report, err = s.config.Analytics.QueryAnalytics(ctx, query)
	}
	if err != nil {
		return AnalyticsView{}, err
	}
	// These contain platform sign-in information, which is not part of a site's
	// visitor report. Visitors below use only site-scoped page-view rows.
	report.People = nil
	now := time.Now()
	view := newAnalyticsView(report, now)
	view.ShowVisitors = true
	view.VisitorSearch = strings.TrimSpace(values.Get("visitor-q"))
	view.VisitorSort = values.Get("visitor-sort")
	if !slices.Contains([]string{"views", "visits", "recent", "name"}, view.VisitorSort) {
		view.VisitorSort = "views"
	}
	view.VisitorSortChoices = choices(view.VisitorSort, [2]string{"views", "Most page views"}, [2]string{"visits", "Most visits"}, [2]string{"recent", "Last visited"}, [2]string{"name", "Name A–Z"})
	view.AnonymousPageViews = report.Traffic.PageViews
	var peakViews int64
	if paged {
		pageNumber := 1
		if value := values.Get("visitor-page"); value != "" {
			pageNumber, err = strconv.Atoi(value)
			if err != nil || pageNumber < 1 {
				return AnalyticsView{}, &siteAnalyticsInputError{errors.New("visitor-page must be a positive integer")}
			}
		}
		page, err := visitorReader.QueryAnalyticsVisitors(ctx, AnalyticsVisitorsQuery{AnalyticsQuery: query, Search: view.VisitorSearch, Sort: view.VisitorSort, Page: pageNumber, Limit: managePageSize})
		if err != nil {
			return AnalyticsView{}, err
		}
		report.Users = page.Rows
		view.AnonymousPageViews, peakViews = page.AnonymousPageViews, page.PeakPageViews
		view.VisitorTotal, view.VisitorPage = page.Total, page.Page
	}
	for _, visitor := range report.Users {
		peakViews = max(peakViews, visitor.PageViews)
	}
	for _, visitor := range report.Users {
		if !paged {
			view.AnonymousPageViews -= visitor.PageViews
		}
		if visitor.PageViews == 0 || visitor.Key == "" {
			continue
		}
		row := siteVisitorRow{ID: visitor.Key, Name: visitor.Key, TrafficTotals: visitor.TrafficTotals}
		if visitor.Person != nil {
			row.Email = visitor.Person.Email
			if visitor.Person.Name != "" {
				row.Name = visitor.Person.Name
			} else if visitor.Person.Email != "" {
				row.Name = visitor.Person.Email
			}
		}
		if !paged && view.VisitorSearch != "" && !containsFold(row.Name+" "+row.Email+" "+row.ID, view.VisitorSearch) {
			continue
		}
		row.Last = relativeTime(visitor.LastVisited, now)
		row.LastISO = visitor.LastVisited.UTC().Format(time.RFC3339)
		row.Initials = initials(row.Name)
		row.Tone = tone(row.ID)
		row.Views = formatCount(visitor.PageViews)
		row.ViewsBar = barPercent(visitor.PageViews, peakViews)
		view.SiteVisitors = append(view.SiteVisitors, row)
	}
	slices.SortFunc(view.SiteVisitors, func(a, b siteVisitorRow) int {
		switch view.VisitorSort {
		case "views":
			if a.PageViews != b.PageViews {
				return compareDescending(a.PageViews, b.PageViews)
			}
		case "visits":
			if a.Visits != b.Visits {
				return compareDescending(a.Visits, b.Visits)
			}
		case "recent":
			if !a.LastVisited.Equal(b.LastVisited) {
				return b.LastVisited.Compare(a.LastVisited)
			}
		}
		if name := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); name != 0 {
			return name
		}
		return strings.Compare(a.ID, b.ID)
	})
	if err := paginateSiteVisitorsPage(&view, values, paged); err != nil {
		return AnalyticsView{}, &siteAnalyticsInputError{err}
	}
	s.describeSiteReport(ctx, &view, report, values, now)
	if reader, ok := s.config.Analytics.(SiteTrafficReader); ok {
		rows, err := reader.SiteTraffic(ctx, []string{site})
		if err != nil {
			slog.Error("load lifetime site traffic", "site", site, "error", err)
		} else {
			view.Lifetime = &TrafficTotals{}
			for _, row := range rows {
				if row.Key == site {
					total := row.TrafficTotals
					view.Lifetime = &total
				}
			}
			view.Metrics[0].Hint = formatCount(view.Lifetime.PageViews) + " all time"
			view.Metrics[1].Hint = formatCount(int64(view.Lifetime.Visitors)) + " all time"
			view.Metrics[2].Hint = formatCount(view.Lifetime.Visits) + " all time"
			if !view.Lifetime.LastVisited.IsZero() {
				view.LastVisit = relativeTime(view.Lifetime.LastVisited, now)
			}
		}
	}
	return view, nil
}

// describeSiteReport adds headline numbers compared with the previous
// period, and period links that refresh only the analytics panel.
func (s *Server) describeSiteReport(ctx context.Context, view *AnalyticsView, report AnalyticsReport, values url.Values, now time.Time) {
	previous, comparable := s.previousReport(ctx, report.Query)
	metric := func(label string, current, before int64) metricCard {
		return newMetric(label, current, before, comparable, view.Comparison)
	}
	busiest := metricCard{Label: "Busiest day", Value: "—", Hint: "No page views in this period"}
	if !view.Chart.Empty {
		day, views, _ := strings.Cut(view.Chart.Peak, " · ")
		busiest.Value, busiest.Hint = day, views
	}
	view.Metrics = []metricCard{
		metric("Page views", report.Traffic.PageViews, previous.Traffic.PageViews),
		metric("People", int64(report.Traffic.Visitors), int64(previous.Traffic.Visitors)),
		metric("Visits", report.Traffic.Visits, previous.Traffic.Visits),
		busiest,
	}
	if !report.Traffic.LastVisited.IsZero() {
		view.LastVisit = relativeTime(report.Traffic.LastVisited, now)
	}

	keep := url.Values{"tab": {"analytics"}}
	for _, name := range []string{"visitor-q", "visitor-sort"} {
		if value := values.Get(name); value != "" {
			keep.Set(name, value)
		}
	}
	view.Periods, view.CustomPeriod = periodChoices(report.Query, now, keep, "/manage/"+view.Site, "/api/hex/manage/sites/"+view.Site+"/analytics")
	view.CustomAttr = flag("open", view.CustomPeriod)
	if report.Publications > 0 {
		view.UpdateSummary = "Updated " + plural(report.Publications, "time") + " in this period"
	}
}

func paginateSiteVisitors(view *AnalyticsView, values url.Values) error {
	return paginateSiteVisitorsPage(view, values, false)
}

func paginateSiteVisitorsPage(view *AnalyticsView, values url.Values, paged bool) error {
	page := 1
	if value := values.Get("visitor-page"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return fmt.Errorf("visitor-page must be a positive integer")
		}
		page = parsed
	}
	if !paged {
		view.VisitorTotal = len(view.SiteVisitors)
	} else {
		page = view.VisitorPage
	}
	view.VisitorSummary = peopleCount(view.VisitorTotal) + " opened this site in the period."
	if view.VisitorSearch != "" {
		view.VisitorSummary = peopleCount(view.VisitorTotal) + " match your search."
	}
	view.VisitorPages = max(1, (view.VisitorTotal+managePageSize-1)/managePageSize)
	view.VisitorPage = min(page, view.VisitorPages)
	link := func(page int) string {
		query := url.Values{
			"tab": {"analytics"}, "from": {view.From}, "until": {view.Until},
			"visitor-q": {view.VisitorSearch}, "visitor-sort": {view.VisitorSort}, "visitor-page": {strconv.Itoa(page)},
		}
		return query.Encode()
	}
	pageURL := "/manage/" + view.Site + "?"
	fragment := "/api/hex/manage/sites/" + view.Site + "/analytics?"
	if view.VisitorPage > 1 {
		view.VisitorPrevious = pageURL + link(view.VisitorPage-1)
		view.VisitorPreviousFragment = fragment + link(view.VisitorPage-1)
	}
	if view.VisitorPage < view.VisitorPages {
		view.VisitorNext = pageURL + link(view.VisitorPage+1)
		view.VisitorNextFragment = fragment + link(view.VisitorPage+1)
	}
	if !paged {
		start := (view.VisitorPage - 1) * managePageSize
		view.SiteVisitors = view.SiteVisitors[start:min(start+managePageSize, view.VisitorTotal)]
	}
	return nil
}
