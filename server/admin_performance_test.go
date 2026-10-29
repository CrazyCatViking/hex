package hex_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

type adminReportProbe struct {
	*memory.Analytics
	reports atomic.Int64
	imports atomic.Int64
	fail    bool
}

func BenchmarkAdminDirectoryAudit(b *testing.B) {
	for _, count := range []int{100, 500} {
		b.Run(fmt.Sprintf("sites/%d", count), func(b *testing.B) {
			directory := &auditSiteDirectory{}
			access := &auditAccessStore{AccessStore: memory.NewAccessStore()}
			for index := range count {
				name := fmt.Sprintf("app-%04d", index)
				directory.names = append(directory.names, name)
				if err := access.PutSiteAccess(b.Context(), name, hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}); err != nil {
					b.Fatal(err)
				}
			}
			analytics := &adminReportProbe{Analytics: memory.NewAnalytics(), fail: true}
			server := hex.New(hex.Config{
				Sites: directory, Access: access, Analytics: analytics, SiteBaseURL: "http://example.com",
				Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "operator", Groups: []string{"admins"}}}, AdminGroups: []string{"admins"},
			})
			request := httptest.NewRequest(http.MethodGet, "http://example.com/admin/sites", nil)
			server.ServeHTTP(httptest.NewRecorder(), request.Clone(b.Context()))
			directory.lists.Store(0)
			directory.metadataReads.Store(0)
			access.batches.Store(0)
			var bytes int
			b.ResetTimer()
			for range b.N {
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request.Clone(b.Context()))
				if response.Code != http.StatusOK {
					b.Fatalf("admin directory: %d %s", response.Code, response.Body.String())
				}
				bytes = response.Body.Len()
			}
			b.StopTimer()
			b.ReportMetric(float64(directory.lists.Load())/float64(b.N), "lists/op")
			b.ReportMetric(float64(directory.metadataReads.Load())/float64(b.N), "metadata/op")
			b.ReportMetric(float64(access.batches.Load())/float64(b.N), "policy-batches/op")
			b.ReportMetric(float64(analytics.reports.Load())/float64(b.N), "reports/op")
			b.ReportMetric(float64(analytics.imports.Load())/float64(b.N), "imports/op")
			b.ReportMetric(float64(bytes), "html-bytes/op")
		})
	}
}

func (a *adminReportProbe) QueryAdminAnalytics(ctx context.Context, q hex.AdminAnalyticsQuery) (hex.AdminAnalyticsResult, error) {
	a.reports.Add(1)
	if a.fail {
		return hex.AdminAnalyticsResult{}, errors.New("unavailable")
	}
	return a.Analytics.QueryAdminAnalytics(ctx, q)
}
func (a *adminReportProbe) RecordSiteEvents(context.Context, []hex.SiteEvent) error {
	a.imports.Add(1)
	return errors.New("must not import during a page request")
}

func TestAdminInventoryAndShellNeverWaitForAnalyticsOrHistory(t *testing.T) {
	config, directory, access := discoveryFixture(t, 156)
	probe := &adminReportProbe{Analytics: memory.NewAnalytics(), fail: true}
	config.Analytics = probe
	config.AdminGroups = []string{"admins"}
	server := hex.New(config)
	admin := principalHeaders("operator", "admins")
	for _, path := range []string{"/admin/sites", "/admin/sites?page=2", "/admin/sites?q=app-0155", "/admin", "/admin/users", "/admin/sites/app-0155"} {
		body := requestAs(t, server, admin, "GET", path, nil, 200).Body.String()
		if strings.Count(body, `class="site-row"`) > 12 {
			t.Fatal("admin directory is not bounded")
		}
		if strings.Contains(path, "q=") && !strings.Contains(body, "/manage/app-0155") {
			t.Fatal("admin search is limited to the first page")
		}
	}
	if probe.reports.Load() != 0 || probe.imports.Load() != 0 {
		t.Fatal("admin browsing depends on analytics/history")
	}
	if directory.lists.Load() != 1 || directory.metadataReads.Load() != 156 || access.batches.Load() != 3 {
		t.Fatalf("admin browsing repeated storage inventory: lists=%d metadata=%d batches=%d", directory.lists.Load(), directory.metadataReads.Load(), access.batches.Load())
	}
	failed := requestAs(t, server, admin, "GET", "/admin?fragment=1", nil, 200).Body.String()
	if !strings.Contains(failed, "Analytics are temporarily unavailable") || strings.Contains(failed, "No page views") {
		t.Fatal("report failure was hidden as an empty report")
	}
	if probe.imports.Load() != 0 {
		t.Fatal("failed report triggered a synchronous history import")
	}
}

func TestAdminSiteReportDoesNotEnumerateOtherSites(t *testing.T) {
	config, directory, access := discoveryFixture(t, 156)
	config.Analytics = memory.NewAnalytics()
	config.AdminGroups = []string{"admins"}
	server := hex.New(config)
	requestAs(t, server, principalHeaders("operator", "admins"), "GET", "/admin/sites/app-0155?fragment=1", nil, 200)
	if directory.lists.Load() != 0 || directory.metadataReads.Load() != 1 || access.reads.Load() != 1 || access.batches.Load() != 0 {
		t.Fatalf("single-site report loaded platform inventory: lists=%d metadata=%d policies=%d batches=%d", directory.lists.Load(), directory.metadataReads.Load(), access.reads.Load(), access.batches.Load())
	}
}

func TestAdminPeopleReportsAreBoundedAndSearchBeyondFirstPage(t *testing.T) {
	config, _, _ := discoveryFixture(t, 3)
	analytics := memory.NewAnalytics()
	config.Analytics = analytics
	config.AdminGroups = []string{"admins"}
	for index := range 100 {
		id := strings.Repeat("x", index+1)
		if err := analytics.ObservePerson(context.Background(), hex.Person{ID: id, Name: id}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	server := hex.New(config)
	admin := principalHeaders("operator", "admins")
	body := requestAs(t, server, admin, "GET", "/admin/users?fragment=1&sort=name&page=2", nil, 200).Body.String()
	if !strings.Contains(body, "Page 2 of 5") || strings.Count(body, `class="cell-identity"`) > 25 {
		t.Fatal("people report is not correctly paginated")
	}
	needle := strings.Repeat("x", 100)
	body = requestAs(t, server, admin, "GET", "/admin/users?fragment=1&q="+needle, nil, 200).Body.String()
	if !strings.Contains(body, needle) {
		t.Fatal("people search did not search beyond the first page")
	}
}
