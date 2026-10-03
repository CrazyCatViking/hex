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
	ID      string
	Name    string
	Email   string
	Last    string
	LastISO string
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
	report, err := s.config.Analytics.QueryAnalytics(ctx, query)
	if err != nil {
		return AnalyticsView{}, err
	}
	// These contain platform sign-in information, which is not part of a site's
	// visitor report. Visitors below use only site-scoped page-view rows.
	report.People = nil
	view := newAnalyticsView(report)
	view.ShowVisitors = true
	view.VisitorSearch = strings.TrimSpace(values.Get("visitor-q"))
	view.VisitorSort = values.Get("visitor-sort")
	if !slices.Contains([]string{"views", "visits", "recent", "name"}, view.VisitorSort) {
		view.VisitorSort = "views"
	}
	view.VisitorSortChoices = choices(view.VisitorSort, [2]string{"views", "Most page views"}, [2]string{"visits", "Most visits"}, [2]string{"recent", "Last visited"}, [2]string{"name", "Name A–Z"})
	view.AnonymousPageViews = report.Traffic.PageViews
	for _, visitor := range report.Users {
		view.AnonymousPageViews -= visitor.PageViews
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
		if view.VisitorSearch != "" && !containsFold(row.Name+" "+row.Email+" "+row.ID, view.VisitorSearch) {
			continue
		}
		row.Last = visitor.LastVisited.UTC().Format("Jan 2, 2006 15:04 UTC")
		row.LastISO = visitor.LastVisited.UTC().Format(time.RFC3339)
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
	if err := paginateSiteVisitors(&view, values); err != nil {
		return AnalyticsView{}, &siteAnalyticsInputError{err}
	}
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
		}
	}
	return view, nil
}

func paginateSiteVisitors(view *AnalyticsView, values url.Values) error {
	page := 1
	if value := values.Get("visitor-page"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return fmt.Errorf("visitor-page must be a positive integer")
		}
		page = parsed
	}
	view.VisitorTotal = len(view.SiteVisitors)
	view.VisitorPages = max(1, (view.VisitorTotal+managePageSize-1)/managePageSize)
	view.VisitorPage = min(page, view.VisitorPages)
	link := func(page int) string {
		query := url.Values{
			"tab": {"analytics"}, "from": {view.From}, "until": {view.Until},
			"visitor-q": {view.VisitorSearch}, "visitor-sort": {view.VisitorSort}, "visitor-page": {strconv.Itoa(page)},
		}
		return "/manage/" + view.Site + "?" + query.Encode()
	}
	if view.VisitorPage > 1 {
		view.VisitorPrevious = link(view.VisitorPage - 1)
	}
	if view.VisitorPage < view.VisitorPages {
		view.VisitorNext = link(view.VisitorPage + 1)
	}
	start := (view.VisitorPage - 1) * managePageSize
	view.SiteVisitors = view.SiteVisitors[start:min(start+managePageSize, view.VisitorTotal)]
	return nil
}
