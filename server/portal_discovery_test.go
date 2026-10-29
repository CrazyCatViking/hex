package hex_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func discoveryFixture(t *testing.T, count int) (hex.Config, *auditSiteDirectory, *auditAccessStore) {
	t.Helper()
	directory := &auditSiteDirectory{}
	access := &auditAccessStore{AccessStore: memory.NewAccessStore()}
	for index := range count {
		name := fmt.Sprintf("app-%04d", index)
		directory.names = append(directory.names, name)
		if err := access.PutSiteAccess(context.Background(), name, hex.SiteAccess{
			Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{SignedIn: true, Owners: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return hex.Config{Sites: directory, Access: access, Database: memory.NewDatabase(), Identity: easyauth.Resolver{}, SiteBaseURL: "http://example.com"}, directory, access
}

type countedTraffic struct {
	*memory.Analytics
	calls     atomic.Int64
	requested atomic.Int64
}

func (a *countedTraffic) SiteTraffic(_ context.Context, sites []string) ([]hex.AnalyticsRow, error) {
	a.calls.Add(1)
	a.requested.Store(int64(len(sites)))
	return nil, fmt.Errorf("analytics must not be needed for browsing")
}

func TestPortalBoundedInventoryAndFreshBatchAuthorization(t *testing.T) {
	config, directory, access := discoveryFixture(t, 156)
	analytics := &countedTraffic{Analytics: memory.NewAnalytics()}
	config.Analytics = analytics
	server := hex.New(config)
	owner := principalHeaders("owner")
	home := requestAs(t, server, owner, "GET", "http://example.com/", nil, 200).Body.String()
	if directory.lists.Load() != 1 || directory.metadataReads.Load() != 156 || access.batches.Load() != 1 || access.reads.Load() != 0 {
		t.Fatalf("cold home repeated inventory or policy reads: lists=%d metadata=%d batches=%d reads=%d", directory.lists.Load(), directory.metadataReads.Load(), access.batches.Load(), access.reads.Load())
	}
	if strings.Count(home, `<article class="site-card">`) > 4 || strings.Count(home, `<article class="site-row">`) > 4 {
		t.Fatal("home previews are not bounded")
	}
	for _, route := range []string{"/library", "/library?page=2", "/manage", "/library?search=app-0155", "/library?search=no-match"} {
		body := requestAs(t, server, owner, "GET", "http://example.com"+route, nil, 200).Body.String()
		if strings.Count(body, `<article class="site-card">`) > 12 || strings.Count(body, `class="site-row"`) > 12 {
			t.Fatal("listing is not paginated")
		}
		if strings.Contains(route, "0155") && !strings.Contains(body, `href="http://app-0155.example.com/"`) {
			t.Fatal("search only searched the first page")
		}
		if route == "/library?page=2" && !strings.Contains(body, "Showing 13–24 of 156") {
			t.Fatal("wrong second page")
		}
	}
	if directory.lists.Load() != 1 || directory.metadataReads.Load() != 156 || access.batches.Load() != 6 || access.reads.Load() != 0 {
		t.Fatal("warm browsing rereads inventory or fails to refresh authorization")
	}
	if analytics.calls.Load() != 0 {
		t.Fatal("ordinary browsing called analytics")
	}
	manage := requestAs(t, server, owner, "GET", "http://example.com/manage?q=app-0155", nil, 200).Body.String()
	if !strings.Contains(manage, `href="/manage/app-0155"`) || strings.Contains(manage, "data-filter-target") {
		t.Fatal("management search still filters only rendered rows")
	}
	for _, path := range []string{"/library?page=-10", "/library?page=99999999999999999999999"} {
		requestAs(t, server, owner, "GET", "http://example.com"+path, nil, 200)
	}
	activity := requestAs(t, server, owner, "GET", "http://example.com/library?sort=popular&traffic=visited&search=app-0155", nil, 200).Body.String()
	if analytics.calls.Load() != 1 || analytics.requested.Load() != 1 {
		t.Fatal("optional activity requested unrelated inventory")
	}
	if !strings.Contains(activity, "Activity is temporarily unavailable") || !strings.Contains(activity, "app-0155") || strings.Contains(activity, "0 page views") {
		t.Fatal("activity failure was hidden or invented zero counts")
	}
	requestAs(t, server, owner, "GET", "http://example.com/library?sort=popular&search=no-match", nil, 200)
	if analytics.calls.Load() != 1 {
		t.Fatal("empty searches must not call analytics")
	}
}

func TestPortalCustomAccessStoreFallbackUsesFreshPolicies(t *testing.T) {
	config, _, access := discoveryFixture(t, 1)
	// Deliberately expose only the original AccessStore contract.
	config.Access = struct{ hex.AccessStore }{access}
	server := hex.New(config)
	viewer := principalHeaders("viewer")
	requestAs(t, server, viewer, "GET", "http://example.com/library", nil, 200)
	if err := access.PutSiteAccess(context.Background(), "app-0000", hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}); err != nil {
		t.Fatal(err)
	}
	body := requestAs(t, server, viewer, "GET", "http://example.com/library", nil, 200).Body.String()
	if strings.Contains(body, "app-0000") || access.reads.Load() != 2 || access.batches.Load() != 0 {
		t.Fatal("custom-provider fallback cached authorization")
	}
}

func TestPortalPinsSurviveReplicasButNeverAccessRevocation(t *testing.T) {
	config, _, access := discoveryFixture(t, 25)
	first, second := hex.New(config), hex.New(config)
	viewer := principalHeaders("viewer")
	for index := range 6 {
		requestAs(t, first, viewer, "PUT", fmt.Sprintf("http://example.com/api/hex/preferences/pins/app-%04d", index), []byte(`{"pinned":true}`), 200)
	}
	home := requestAs(t, second, viewer, "GET", "http://example.com/", nil, 200).Body.String()
	pins := home[strings.Index(home, `id="pinned-heading"`):strings.Index(home, `class="home-sections"`)]
	if strings.Count(pins, `<article class="site-card">`) != 4 {
		t.Fatal("home must show four pins")
	}
	library := requestAs(t, second, viewer, "GET", "http://example.com/library?scope=pinned", nil, 200).Body.String()
	if strings.Count(library, `<article class="site-card">`) != 6 {
		t.Fatal("all pins were not stored across replicas")
	}
	other := requestAs(t, second, principalHeaders("someone-else"), "GET", "http://example.com/library?scope=pinned", nil, 200).Body.String()
	if strings.Contains(other, `<article class="site-card">`) {
		t.Fatal("another user's pins leaked")
	}
	if strings.Contains(library, ">Manage</a>") || strings.Contains(library, "?tab=analytics") {
		t.Fatal("pins granted management shortcuts")
	}
	requestAs(t, first, viewer, "POST", "http://example.com/api/hex/preferences/recent/app-0000", nil, 200)
	if err := access.PutSiteAccess(context.Background(), "app-0000", hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}); err != nil {
		t.Fatal(err)
	}
	// Both replicas have warm descriptive caches. Neither may reuse old policy,
	// reveal a revoked pin in a count, or retain it through personal history.
	for _, server := range []*hex.Server{first, second} {
		for _, scope := range []string{"pinned", "recent", "all", "shared", "everyone"} {
			body := requestAs(t, server, viewer, "GET", "http://example.com/library?scope="+scope, nil, 200).Body.String()
			if strings.Contains(body, "app-0000") {
				t.Fatalf("revoked site leaked through %s", scope)
			}
		}
	}
	requestAs(t, first, viewer, "PUT", "http://example.com/api/hex/preferences/pins/app-0000", []byte(`{"pinned":true}`), 403)
	requestAs(t, first, viewer, "POST", "http://example.com/api/hex/preferences/recent/app-0000", nil, 403)
	requestAs(t, first, viewer, "PUT", "http://example.com/api/hex/preferences/pins/app-0000", []byte(`{"pinned":false}`), 200)
	requestAs(t, first, nil, "PUT", "http://example.com/api/hex/preferences/pins/app-0001", []byte(`{"pinned":true}`), 401)
	requestAs(t, first, viewer, "PUT", "http://app-0001.example.com/api/hex/preferences/pins/app-0001", []byte(`{"pinned":true}`), 403)
	for _, data := range []string{`{}`, `null`, `{"pinned":null}`, `{"pinned":"true"}`, `{"pinned":true,"user":"owner"}`, `{"pinned":true} {}`} {
		requestAs(t, first, viewer, "PUT", "http://example.com/api/hex/preferences/pins/app-0001", []byte(data), 400)
	}
}

func TestPortalPinConcurrentWritesAndLimit(t *testing.T) {
	config, _, _ := discoveryFixture(t, 110)
	server := hex.New(config)
	viewer := principalHeaders("viewer")
	var writes sync.WaitGroup
	for index := range 20 {
		writes.Go(func() {
			requestAs(t, server, viewer, "PUT", fmt.Sprintf("http://example.com/api/hex/preferences/pins/app-%04d", index), []byte(`{"pinned":true}`), 200)
		})
	}
	writes.Wait()
	for index := 20; index < 100; index++ {
		requestAs(t, server, viewer, "PUT", fmt.Sprintf("http://example.com/api/hex/preferences/pins/app-%04d", index), []byte(`{"pinned":true}`), 200)
	}
	requestAs(t, server, viewer, "PUT", "http://example.com/api/hex/preferences/pins/app-0100", []byte(`{"pinned":true}`), 400)
	body := requestAs(t, server, viewer, "GET", "http://example.com/library?scope=pinned", nil, 200).Body.String()
	if !strings.Contains(body, "Showing 1–12 of 100") {
		t.Fatal("concurrent pin writes were lost")
	}
	requestAs(t, server, viewer, "PUT", "http://example.com/api/hex/preferences/pins/app-0000", []byte(`{"pinned":true}`), 200)
}

type tabPublisher struct {
	hex.SitePublisher
	reads map[string]int
}

func (p *tabPublisher) ReadSiteFile(ctx context.Context, site, path string) (io.ReadCloser, error) {
	p.reads[path]++
	return p.SitePublisher.ReadSiteFile(ctx, site, path)
}

func TestPortalManagementLoadsOnlySelectedTab(t *testing.T) {
	config, _, _ := discoveryFixture(t, 1)
	publisher := &tabPublisher{reads: map[string]int{}}
	// Missing records are sufficient: no off-tab publication/action reads should
	// happen on Data or Settings, even when a publisher is configured.
	publisher.SitePublisher = missingPublisher{}
	config.Publisher = publisher
	server := hex.New(config)
	for _, tab := range []string{"data", "settings"} {
		requestAs(t, server, principalHeaders("owner"), "GET", "http://example.com/manage/app-0000?tab="+tab, nil, 200)
		if len(publisher.reads) != 0 {
			t.Fatalf("%s loaded unrelated records: %v", tab, publisher.reads)
		}
	}
}

type missingPublisher struct{ hex.SitePublisher }

func (missingPublisher) ReadSiteFile(context.Context, string, string) (io.ReadCloser, error) {
	return nil, hex.ErrNotFound
}

func TestPortalScopePreservesHiddenAndArtifactSemantics(t *testing.T) {
	hidden := false
	access := memory.NewAccessStore()
	for _, name := range []string{"private-file", "hidden-app"} {
		if err := access.PutSiteAccess(context.Background(), name, hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true, Principals: []string{"user:viewer"}}}); err != nil {
			t.Fatal(err)
		}
	}
	server := hex.New(hex.Config{Sites: portalDirectory{metadata: map[string]*hex.SiteMetadata{"public-app": {Title: "Public app"}, "private-file": {Title: "Private file", Kind: hex.KindArtifact}, "hidden-app": {Title: "Hidden app", Discoverable: &hidden}, "undiscoverable": {Title: "Unknown draft", Discoverable: &hidden}}}, Access: access, Identity: easyauth.Resolver{}, SiteBaseURL: "http://example.com"})
	shared := requestAs(t, server, principalHeaders("viewer"), "GET", "http://example.com/library?scope=shared", nil, 200).Body.String()
	if !strings.Contains(shared, "Private file") || !strings.Contains(shared, "Hidden app") || strings.Contains(shared, "Unknown draft") {
		t.Fatal("explicit personal sharing was lost or hidden inventory leaked")
	}
	company := requestAs(t, server, principalHeaders("viewer"), "GET", "http://example.com/library?scope=everyone", nil, 200).Body.String()
	if !strings.Contains(company, "Public app") || strings.Contains(company, "Private file") || strings.Contains(company, "Hidden app") {
		t.Fatal("company scope includes non-company sites")
	}
	stranger := requestAs(t, server, principalHeaders("stranger"), "GET", "http://example.com/library", nil, 200).Body.String()
	if strings.Contains(stranger, "Private file") || strings.Contains(stranger, "Hidden app") || strings.Contains(stranger, "Unknown draft") {
		t.Fatal("inaccessible sites leaked from warm inventory")
	}
}
