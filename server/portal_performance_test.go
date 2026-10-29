package hex_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

// These benchmarks expose listing work independently of storage latency. They
// deliberately use current v2 policies, so migration work is not included.
type auditSiteDirectory struct {
	names           []string
	lists           atomic.Int64
	metadataReads   atomic.Int64
	metadataLatency time.Duration
}

func (d *auditSiteDirectory) ListSites(context.Context) ([]string, error) {
	d.lists.Add(1)
	return append([]string(nil), d.names...), nil
}

func (d *auditSiteDirectory) ReadSiteMetadata(ctx context.Context, name string) (*hex.SiteMetadata, error) {
	d.metadataReads.Add(1)
	if d.metadataLatency > 0 {
		timer := time.NewTimer(d.metadataLatency)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &hex.SiteMetadata{Title: name, PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func BenchmarkPortalColdInventoryWithStorageLatency(b *testing.B) {
	for _, count := range []int{100, 500} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			directory := &auditSiteDirectory{metadataLatency: 5 * time.Millisecond}
			for index := range count {
				directory.names = append(directory.names, fmt.Sprintf("app-%04d", index))
			}
			request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			b.ResetTimer()
			for range b.N {
				server := hex.New(hex.Config{Sites: directory, Access: memory.NewAccessStore(), SiteBaseURL: "http://example.com"})
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request.Clone(context.Background()))
				if response.Code != 200 {
					b.Fatal(response.Body.String())
				}
			}
			b.ReportMetric(float64(directory.lists.Load())/float64(b.N), "lists/op")
			b.ReportMetric(float64(directory.metadataReads.Load())/float64(b.N), "metadata/op")
		})
	}
}

type auditAccessStore struct {
	hex.AccessStore
	reads   atomic.Int64
	batches atomic.Int64
}

func (s *auditAccessStore) GetSiteAccess(ctx context.Context, site string) (hex.SiteAccess, error) {
	s.reads.Add(1)
	return s.AccessStore.GetSiteAccess(ctx, site)
}

func (s *auditAccessStore) GetSiteAccessBatch(ctx context.Context, sites []string) (map[string]hex.SiteAccess, error) {
	s.batches.Add(1)
	return s.AccessStore.(hex.SiteAccessBatchReader).GetSiteAccessBatch(ctx, sites)
}

func BenchmarkPortalInventoryAudit(b *testing.B) {
	for _, count := range []int{10, 100, 500} {
		for _, route := range []struct {
			name string
			path string
		}{
			{"home", "/"},
			{"search", "/api/hex/catalog?search=app-0000"},
			{"no-results", "/api/hex/catalog?search=no-match"},
			{"manage", "/manage"},
		} {
			b.Run(fmt.Sprintf("%s/%d", route.name, count), func(b *testing.B) {
				directory := &auditSiteDirectory{}
				access := &auditAccessStore{AccessStore: memory.NewAccessStore()}
				for index := range count {
					name := fmt.Sprintf("app-%04d", index)
					directory.names = append(directory.names, name)
					policy := hex.SiteAccess{
						Version: 2, Owners: []string{"user:audit-viewer"},
						Admission: hex.Subjects{SignedIn: true, Owners: true},
					}
					if err := access.PutSiteAccess(context.Background(), name, policy); err != nil {
						b.Fatal(err)
					}
				}
				server := hex.New(hex.Config{
					Sites: directory, Access: access, SiteBaseURL: "http://example.com",
					Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "audit-viewer", Name: "Audit Viewer"}},
				})
				request := httptest.NewRequest(http.MethodGet, "http://example.com"+route.path, nil)
				server.ServeHTTP(httptest.NewRecorder(), request.Clone(context.Background()))
				directory.lists.Store(0)
				directory.metadataReads.Store(0)
				access.reads.Store(0)
				access.batches.Store(0)
				var bytes int
				b.ResetTimer()
				for range b.N {
					response := httptest.NewRecorder()
					server.ServeHTTP(response, request.Clone(context.Background()))
					if response.Code != http.StatusOK {
						b.Fatalf("unexpected response: %d: %s", response.Code, response.Body.String())
					}
					bytes = response.Body.Len()
				}
				b.StopTimer()
				b.ReportMetric(float64(directory.lists.Load())/float64(b.N), "lists/op")
				b.ReportMetric(float64(directory.metadataReads.Load())/float64(b.N), "metadata/op")
				b.ReportMetric(float64(access.reads.Load())/float64(b.N), "policies/op")
				b.ReportMetric(float64(access.batches.Load())/float64(b.N), "policy-batches/op")
				b.ReportMetric(float64(bytes), "html-bytes/op")
			})
		}
	}
}
