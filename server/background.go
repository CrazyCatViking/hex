package hex

import (
	"context"
	"sync"
)

// RunBackground performs the platform's scheduled work until ctx ends:
// running due automations and removing connected accounts that went unused.
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
	tasks.Wait()
}
