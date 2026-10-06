package hex

import (
	"context"
	"sync"
)

// RunBackground performs the platform's scheduled work until ctx ends:
// running due automations, removing connected accounts that went unused and
// integration audit records past their retention.
// Hosts start it once per instance; it returns after automation runs in
// progress have finished.
func (s *Server) RunBackground(ctx context.Context) {
	var tasks sync.WaitGroup
	if s.automationsEnabled() {
		tasks.Go(func() { s.runAutomationScheduler(ctx) })
	}
	if s.connectionsEnabled() {
		tasks.Go(func() { s.cleanUpConnections(ctx) })
	}
	if s.config.IntegrationAudit != nil {
		tasks.Go(func() { s.cleanUpIntegrationAudit(ctx) })
	}
	tasks.Wait()
}
