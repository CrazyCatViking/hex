package postgres

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type portalSitesFixture struct{}

func (portalSitesFixture) ListSites(context.Context) ([]string, error) { return []string{"demo"}, nil }

func TestPostgresPortalPinsAndBatchRevocationAcrossReplicas(t *testing.T) {
	database := isolatedAnalyticsDatabase(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, database.pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	replica := &Database{pool: pool}
	defer replica.Close()
	policy := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{SignedIn: true, Owners: true}}
	if err := database.PutSiteAccess(ctx, "demo", policy); err != nil {
		t.Fatal(err)
	}
	policies, err := replica.GetSiteAccessBatch(ctx, []string{"demo", "missing", "demo"})
	if err != nil || len(policies) != 1 || policies["demo"].Version != 2 {
		t.Fatalf("batch decoding/missing policy: %v %v", policies, err)
	}
	config := hex.Config{Sites: portalSitesFixture{}, SiteBaseURL: "http://example.com", Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "viewer"}}, Access: database, Database: database}
	first := hex.New(config)
	config.Access, config.Database = replica, replica
	second := hex.New(config)
	call := func(server *hex.Server, method, path, body string, status int) string {
		t.Helper()
		request := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
		request.Header.Set("X-Hex-Request", "1")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s: %d: %s", path, response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	call(first, "PUT", "/api/hex/preferences/pins/demo", `{"pinned":true}`, 200)
	if !strings.Contains(call(second, "GET", "/library?scope=pinned", "", 200), `data-pin="demo"`) {
		t.Fatal("pins did not persist across pools")
	}
	policy.Admission = hex.Subjects{Owners: true}
	if err := replica.PutSiteAccess(ctx, "demo", policy); err != nil {
		t.Fatal(err)
	}
	for _, server := range []*hex.Server{first, second} {
		if strings.Contains(call(server, "GET", "/library?scope=pinned", "", 200), `data-pin="demo"`) {
			t.Fatal("warm portal retained revoked admission")
		}
	}
	call(first, "PUT", "/api/hex/preferences/pins/demo", `{"pinned":true}`, 403)
	call(first, "PUT", "/api/hex/preferences/pins/demo", `{"pinned":false}`, 200)
}

type blockingTrafficTracer struct {
	started chan struct{}
	release chan struct{}
}

func (tracer blockingTrafficTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT site, sum(requests)") {
		close(tracer.started)
		select {
		case <-tracer.release:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (blockingTrafficTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPostgresSlowTrafficDoesNotHoldCacheLock(t *testing.T) {
	database := isolatedAnalyticsDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := database.MigrateAnalytics(ctx); err != nil {
		t.Fatal(err)
	}
	tracer := blockingTrafficTracer{started: make(chan struct{}), release: make(chan struct{})}
	config := database.pool.Config()
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Database{pool: pool, trafficCache: map[string]cachedSiteTraffic{"warm": {row: hex.AnalyticsRow{Key: "warm", TrafficTotals: hex.TrafficTotals{Requests: 1}}, expires: time.Now().Add(time.Minute)}}}
	defer reader.Close()
	cold := make(chan error, 1)
	go func() { _, err := reader.SiteTraffic(ctx, []string{"cold"}); cold <- err }()
	defer func() {
		close(tracer.release)
		if err := <-cold; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-tracer.started:
	case <-ctx.Done():
		t.Fatal("cold query did not start")
	}
	warm := make(chan error, 1)
	go func() { _, err := reader.SiteTraffic(ctx, []string{"warm"}); warm <- err }()
	select {
	case err := <-warm:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a database query is holding the traffic cache mutex")
	}
}
