package postgres

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestPostgresAnalyticsDurabilityAndConcurrentReceipts(t *testing.T) {
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL to run against PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	site := "analytics-" + rand.Text()
	user := "user-" + rand.Text()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, table := range []string{"hex_analytics_events", "hex_analytics_traffic", "hex_analytics_sessions"} {
			if _, err := database.pool.Exec(cleanup, "DELETE FROM "+table+" WHERE site=$1", site); err != nil {
				t.Error(err)
			}
		}
		if _, err := database.pool.Exec(cleanup, "DELETE FROM hex_analytics_people WHERE id=$1", user); err != nil {
			t.Error(err)
		}
		if _, err := database.pool.Exec(cleanup, "DELETE FROM hex_analytics_receipts WHERE id LIKE $1", site+"%"); err != nil {
			t.Error(err)
		}
	}()
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	at := day.Add(23*time.Hour + 50*time.Minute)
	person := hex.Person{ID: user, Name: "Analytics User", Email: "analytics@example.test"}
	if err := database.ObservePerson(ctx, person, at); err != nil {
		t.Fatal(err)
	}
	if err := database.ObservePerson(ctx, person, at.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	publication := hex.SiteEvent{ID: site + "-created", At: at, Site: site, Kind: "created", Actor: &person}
	if err := database.RecordSiteEvents(ctx, []hex.SiteEvent{publication, publication}); err != nil {
		t.Fatal(err)
	}
	events := []hex.TrafficEvent{
		{ID: site + "-1", At: at, Site: site, UserID: user, PageView: true, Status: 200, Bytes: 20},
		{ID: site + "-2", At: at.Add(20 * time.Minute), Site: site, UserID: user, PageView: true, Status: 304},
		{ID: site + "-3", At: at.Add(60 * time.Minute), Site: site, UserID: user, PageView: true, Status: 200, Bytes: 30},
	}
	var workers sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		workers.Go(func() { errors <- database.RecordTraffic(ctx, events) })
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	query := hex.AnalyticsQuery{From: day, Until: day.AddDate(0, 0, 2), Site: site}
	report, err := database.QueryAnalytics(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if report.Traffic.Requests != 3 || report.Traffic.PageViews != 3 || report.Traffic.Visitors != 1 || report.Traffic.Visits != 2 || report.Traffic.Bytes != 50 || report.Created != 1 {
		t.Fatalf("duplicate traffic or bad sessionization: %+v", report)
	}
	reopened, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	report, err = reopened.QueryAnalytics(ctx, query)
	if err != nil || report.Traffic.Requests != 3 || report.Created != 1 {
		t.Fatalf("analytics did not survive reopening: %+v %v", report, err)
	}
	query.Site, query.User = "", user
	report, err = reopened.QueryAnalytics(ctx, query)
	if err != nil || report.KnownUsers != 1 || len(report.People) != 1 || !report.People[0].FirstSeen.Equal(at) || !report.People[0].LastSeen.Equal(at.Add(20*time.Minute)) {
		t.Fatalf("first/last seen or user filter incorrect: %+v %v", report, err)
	}
	// Separate batches with overlapping visitor sets must use consistent lock
	// order. Also verifies an existing session is not reset on a new connection.
	errors = make(chan error, 2)
	for index := range 2 {
		workers.Go(func() {
			more := []hex.TrafficEvent{
				{ID: fmt.Sprintf("%s-more-%d-a", site, index), At: at.Add(70 * time.Minute), Site: site, UserID: user, PageView: true, Status: 200},
				{ID: fmt.Sprintf("%s-more-%d-b", site, index), At: at.Add(70 * time.Minute), Site: site, UserID: user + "-other", PageView: true, Status: 200},
			}
			if index == 1 {
				more[0], more[1] = more[1], more[0]
			}
			errors <- reopened.RecordTraffic(ctx, more)
		})
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}
