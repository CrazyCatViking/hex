package hex

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Historical imports are independent of page requests. Successful sites are not
// retried after a partial failure, and shutdown cancels workers and retry waits.
func (s *Server) importAnalyticsHistory(ctx context.Context) {
	completed := make(map[string]bool)
	for ctx.Err() == nil {
		if s.importAnalyticsHistoryPass(ctx, completed) {
			s.analyticsImportPending.Store(false)
			return
		}
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Server) importAnalyticsHistoryPass(ctx context.Context, completed map[string]bool) bool {
	names, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		s.analyticsFailures.Add(1)
		slog.Error("list sites for analytics import", "error", err)
		return false
	}

	var mu sync.Mutex
	var workers sync.WaitGroup
	jobs := make(chan string)
	failed := false
	for range min(4, len(names)) {
		workers.Go(func() {
			for site := range jobs {
				readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := s.recoverPublicationHistory(readCtx, site)
				cancel()

				mu.Lock()
				if err == nil {
					completed[site] = true
				} else {
					failed = true
				}
				mu.Unlock()
				if err != nil {
					s.analyticsFailures.Add(1)
					slog.Error("import publication history", "site", site, "error", err)
				}
			}
		})
	}
	for _, site := range names {
		mu.Lock()
		done := completed[site]
		mu.Unlock()
		if done || !siteNamePattern.MatchString(site) {
			continue
		}
		select {
		case jobs <- site:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	workers.Wait()
	return !failed && ctx.Err() == nil
}
