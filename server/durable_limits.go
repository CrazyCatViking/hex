package hex

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var errSiteRateLimit = errors.New("site operation rate limit reached; retry after the current minute")

// One durable rolling record per source/category bounds storage and coordinates
// admission across replicas. Authorization must run before consuming quota.
func (s *Server) admitSiteOperation(ctx context.Context, site, category string) error {
	_, err := s.mutateSystem(ctx, "operation-limits", internalKey(site, category), func(current *Document) (json.RawMessage, error) {
		var window struct {
			Minute int64 `json:"minute"`
			Count  int   `json:"count"`
		}
		if current != nil {
			if err := json.Unmarshal(current.Data, &window); err != nil {
				return nil, err
			}
		}
		minute := time.Now().Unix() / 60
		if window.Minute != minute {
			window.Minute = minute
			window.Count = 0
		}
		if window.Count >= s.config.SiteOperationsPerMinute {
			return nil, errSiteRateLimit
		}
		window.Count++
		return json.Marshal(window)
	})
	return err
}
