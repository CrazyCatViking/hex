package memory

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

type Analytics struct {
	mu            sync.Mutex
	people        map[string]hex.AnalyticsPerson
	events        map[string]hex.SiteEvent
	buckets       map[bucketKey]hex.TrafficBucket
	receipts      map[string]time.Time
	sessions      map[visitorKey]time.Time
	lastByVisitor map[visitorKey]time.Time
	lifetime      map[string]map[string]hex.TrafficBucket
}

type visitorKey struct{ site, user string }
type bucketKey struct {
	visitorKey
	day time.Time
}

func NewAnalytics() *Analytics {
	return &Analytics{
		people: make(map[string]hex.AnalyticsPerson), events: make(map[string]hex.SiteEvent),
		buckets: make(map[bucketKey]hex.TrafficBucket), receipts: make(map[string]time.Time),
		sessions:      make(map[visitorKey]time.Time),
		lastByVisitor: make(map[visitorKey]time.Time),
		lifetime:      make(map[string]map[string]hex.TrafficBucket),
	}
}

func (a *Analytics) ObservePerson(ctx context.Context, person hex.Person, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	at = at.UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	previous, exists := a.people[person.ID]
	if !exists {
		previous.FirstSeen = at
	}
	if !at.Before(previous.LastSeen) {
		previous.Person = person
		previous.LastSeen = at
	}
	a.people[person.ID] = previous
	return nil
}

func (a *Analytics) RecordSiteEvents(ctx context.Context, events []hex.SiteEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, event := range events {
		event.At = event.At.UTC()
		if _, exists := a.events[event.ID]; exists {
			continue
		}
		if event.Actor != nil {
			person := *event.Actor
			event.Actor = &person
		}
		a.events[event.ID] = event
	}
	return nil
}

func (a *Analytics) RecordTraffic(ctx context.Context, events []hex.TrafficEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	events = slices.Clone(events)
	slices.SortFunc(events, func(a, b hex.TrafficEvent) int { return a.At.Compare(b.At) })
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -7)
	for id, at := range a.receipts {
		if at.Before(cutoff) {
			delete(a.receipts, id)
		}
	}
	for _, event := range events {
		event.At = event.At.UTC()
		if _, exists := a.receipts[event.ID]; exists || event.At.Before(cutoff) {
			continue
		}
		a.receipts[event.ID] = event.At
		visitor := visitorKey{event.Site, event.UserID}
		day := event.At.UTC().Truncate(24 * time.Hour)
		key := bucketKey{visitor, day}
		bucket := a.buckets[key]
		before := bucket.TrafficTotals
		bucket.Day, bucket.Site, bucket.UserID = day, event.Site, event.UserID
		bucket.Requests++
		bucket.Bytes += event.Bytes
		bucket.DurationMillis += event.DurationMillis
		if event.Status >= 400 {
			bucket.Errors++
		}
		if event.PageView {
			bucket.PageViews++
			if event.At.After(bucket.LastVisited) {
				bucket.LastVisited = event.At
			}
			if event.UserID != "" {
				previous := a.sessions[visitor]
				if previous.IsZero() || event.At.Sub(previous) >= 30*time.Minute {
					bucket.Visits++
				}
				if event.At.After(previous) {
					a.sessions[visitor] = event.At
				}
			}
		}
		a.buckets[key] = bucket
		if a.lifetime[event.Site] == nil {
			a.lifetime[event.Site] = make(map[string]hex.TrafficBucket)
		}
		total := a.lifetime[event.Site][event.UserID]
		total.Site, total.UserID = event.Site, event.UserID
		total.Requests += bucket.Requests - before.Requests
		total.PageViews += bucket.PageViews - before.PageViews
		total.Visits += bucket.Visits - before.Visits
		total.Errors += bucket.Errors - before.Errors
		total.Bytes += bucket.Bytes - before.Bytes
		total.DurationMillis += bucket.DurationMillis - before.DurationMillis
		if bucket.LastVisited.After(total.LastVisited) {
			total.LastVisited = bucket.LastVisited
		}
		a.lifetime[event.Site][event.UserID] = total
		if event.At.After(a.lastByVisitor[visitor]) {
			a.lastByVisitor[visitor] = event.At
		}
	}
	return nil
}

func (a *Analytics) QueryAnalytics(ctx context.Context, query hex.AnalyticsQuery) (hex.AnalyticsReport, error) {
	return a.queryAnalytics(ctx, query, false)
}

func (a *Analytics) QueryAnalyticsSummary(ctx context.Context, query hex.AnalyticsQuery) (hex.AnalyticsReport, error) {
	return a.queryAnalytics(ctx, query, true)
}

func (a *Analytics) QueryAnalyticsVisitors(ctx context.Context, query hex.AnalyticsVisitorsQuery) (hex.AnalyticsVisitorsPage, error) {
	report, err := a.QueryAnalytics(ctx, query.AnalyticsQuery)
	if err != nil {
		return hex.AnalyticsVisitorsPage{}, err
	}
	return hex.PaginateAnalyticsVisitors(report, query), nil
}

func (a *Analytics) queryAnalytics(ctx context.Context, query hex.AnalyticsQuery, summary bool) (hex.AnalyticsReport, error) {
	if err := ctx.Err(); err != nil {
		return hex.AnalyticsReport{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	people := []hex.AnalyticsPerson{}
	if query.Site == "" {
		for _, person := range a.people {
			if query.User == "" || person.ID == query.User {
				people = append(people, person)
			}
		}
	}
	slices.SortFunc(people, func(a, b hex.AnalyticsPerson) int {
		if name := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); name != 0 {
			return name
		}
		return strings.Compare(a.ID, b.ID)
	})
	events := []hex.SiteEvent{}
	for _, event := range a.events {
		if event.At.Before(query.From) || !event.At.Before(query.Until) || query.Site != "" && event.Site != query.Site {
			continue
		}
		if query.User != "" && (event.Actor == nil || event.Actor.ID != query.User) {
			continue
		}
		if event.Actor != nil {
			person := *event.Actor
			event.Actor = &person
		}
		events = append(events, event)
	}
	buckets := []hex.TrafficBucket{}
	for _, bucket := range a.buckets {
		if bucket.Day.Before(query.From) || !bucket.Day.Before(query.Until) || query.Site != "" && bucket.Site != query.Site || query.User != "" && bucket.UserID != query.User {
			continue
		}
		buckets = append(buckets, bucket)
	}
	last := time.Time{}
	for visitor, at := range a.lastByVisitor {
		if query.Site != "" && visitor.site != query.Site || query.User != "" && visitor.user != query.User {
			continue
		}
		if at.After(last) {
			last = at
		}
	}
	if summary {
		return hex.SummarizeAnalyticsSummary(query, people, events, buckets, last), nil
	}
	report := hex.SummarizeAnalytics(query, people, events, buckets, last)
	for index := range report.Users {
		row := &report.Users[index]
		if person, ok := a.people[row.Key]; ok && row.PageViews > 0 {
			label := person.Person
			row.Person = &label
		}
	}
	return report, nil
}

func (a *Analytics) SiteTraffic(ctx context.Context, sites []string) ([]hex.AnalyticsRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sites = slices.Clone(sites)
	slices.Sort(sites)
	sites = slices.Compact(sites)
	a.mu.Lock()
	defer a.mu.Unlock()
	buckets := []hex.TrafficBucket{}
	for _, site := range sites {
		for _, bucket := range a.lifetime[site] {
			buckets = append(buckets, bucket)
		}
	}
	return hex.SummarizeSiteTraffic(buckets), nil
}
