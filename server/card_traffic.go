package hex

import (
	"context"
	"log/slog"
	"slices"
	"strings"
)

func (s *Server) cardTrafficEnabled() bool {
	_, supported := s.config.Analytics.(SiteTrafficReader)
	return supported
}

// Load aggregate traffic once per listing, for its visible sites only. An
// analytics outage must not make the app directory unavailable or look empty.
func (s *Server) fillCardTraffic(ctx context.Context, groups ...[]siteCard) {
	reader, ok := s.config.Analytics.(SiteTrafficReader)
	if !ok {
		return
	}
	names := []string{}
	for _, cards := range groups {
		for _, card := range cards {
			names = append(names, card.Name)
		}
	}
	if len(names) == 0 {
		return
	}
	slices.Sort(names)
	names = slices.Compact(names)
	rows, err := reader.SiteTraffic(ctx, names)
	if err != nil {
		slog.Error("load card traffic", "error", err)
		return
	}
	traffic := make(map[string]TrafficTotals, len(rows))
	for _, row := range rows {
		traffic[row.Key] = row.TrafficTotals
	}
	for _, cards := range groups {
		for index := range cards {
			total := traffic[cards[index].Name]
			cards[index].Traffic = &total
		}
	}
}

func (s *Server) cardSortChoices(selected string) []choice {
	values := [][2]string{{"recent", "Recently updated"}, {"name", "Name A–Z"}}
	if s.cardTrafficEnabled() {
		values = append(values, [2]string{"popular", "Most popular (page views)"}, [2]string{"visitors", "Most visitors"}, [2]string{"visited", "Recently visited"})
	}
	return choices(selected, values...)
}

func cardTrafficChoices(selected string) []choice {
	return choices(selected, [2]string{"all", "All activity"}, [2]string{"visited", "Has page views"}, [2]string{"unvisited", "No page views yet"})
}

func matchesTraffic(card siteCard, filter string) bool {
	if card.Traffic == nil {
		return true
	}
	switch filter {
	case "visited":
		return card.Traffic.PageViews > 0
	case "unvisited":
		return card.Traffic.PageViews == 0
	default:
		return true
	}
}

func sortSiteCards(cards []siteCard, sort string) {
	slices.SortFunc(cards, func(left, right siteCard) int {
		if left.Traffic != nil && right.Traffic != nil {
			switch sort {
			case "popular":
				if left.Traffic.PageViews != right.Traffic.PageViews {
					return compareDescending(left.Traffic.PageViews, right.Traffic.PageViews)
				}
			case "visitors":
				if left.Traffic.Visitors != right.Traffic.Visitors {
					return compareDescending(int64(left.Traffic.Visitors), int64(right.Traffic.Visitors))
				}
			case "visited":
				if !left.Traffic.LastVisited.Equal(right.Traffic.LastVisited) {
					return right.Traffic.LastVisited.Compare(left.Traffic.LastVisited)
				}
			}
		}
		if sort != "name" && sort != "popular" && sort != "visitors" && sort != "visited" && !left.Timestamp.Equal(right.Timestamp) {
			return right.Timestamp.Compare(left.Timestamp)
		}
		if title := strings.Compare(strings.ToLower(left.Title), strings.ToLower(right.Title)); title != 0 {
			return title
		}
		return strings.Compare(left.Name, right.Name)
	})
}
