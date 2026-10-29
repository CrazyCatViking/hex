package hex

import (
	"context"
	"slices"
	"strings"
)

// AdminAnalyticsReader returns only the rows used by an admin section. Storage
// implementations should aggregate, search, sort and page before returning rows.
type AdminAnalyticsReader interface {
	QueryAdminAnalytics(context.Context, AdminAnalyticsQuery) (AdminAnalyticsResult, error)
}

type AdminAnalyticsQuery struct {
	AnalyticsQuery
	Section string
	Search  string
	Sort    string
	Page    int
	Limit   int
}

type AdminAnalyticsResult struct {
	AnalyticsReport
	Total int
	Page  int
}

func analyticsPersonLabel(person Person) string {
	if person.Name != "" {
		return person.Name
	}
	if person.Email != "" {
		return person.Email
	}
	return person.ID
}

// BoundAdminAnalytics is the compatibility path for custom stores and the
// in-memory provider. PostgreSQL implements the bounded queries in storage.
func BoundAdminAnalytics(report AnalyticsReport, query AdminAnalyticsQuery) AdminAnalyticsResult {
	result := AdminAnalyticsResult{AnalyticsReport: report}
	limit := max(1, min(query.Limit, 25))
	result.Events = result.Events[:min(len(result.Events), 50)]
	if query.Section == "users" {
		traffic := make(map[string]TrafficTotals)
		for _, row := range report.Users {
			traffic[row.Key] = row.TrafficTotals
		}
		people := slices.DeleteFunc(slices.Clone(report.People), func(person AnalyticsPerson) bool {
			return !containsFold(person.ID+" "+person.Name+" "+person.Email, query.Search)
		})
		slices.SortFunc(people, func(a, b AnalyticsPerson) int {
			if query.Sort == "recent" && !a.LastSeen.Equal(b.LastSeen) {
				if a.LastSeen.After(b.LastSeen) {
					return -1
				}
				return 1
			}
			if query.Sort == "name" {
				if order := strings.Compare(strings.ToLower(analyticsPersonLabel(a.Person)), strings.ToLower(analyticsPersonLabel(b.Person))); order != 0 {
					return order
				}
			}
			if query.Sort != "name" && query.Sort != "recent" && traffic[a.ID].PageViews != traffic[b.ID].PageViews {
				if traffic[a.ID].PageViews > traffic[b.ID].PageViews {
					return -1
				}
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
		result.Total = len(people)
		result.Page = min(max(1, query.Page), max(1, (result.Total+limit-1)/limit))
		start := (result.Page - 1) * limit
		result.People = people[start:min(len(people), start+limit)]
		result.Users = nil
		for _, person := range result.People {
			result.Users = append(result.Users, AnalyticsRow{Key: person.ID, TrafficTotals: traffic[person.ID]})
		}
		result.Sites, result.Events = nil, nil
		return result
	}
	if query.Section == "overview" {
		result.Sites = result.Sites[:min(5, len(result.Sites))]
		result.Users = result.Users[:min(5, len(result.Users))]
		ids := make(map[string]bool)
		for _, row := range result.Users {
			ids[row.Key] = true
		}
		result.People = slices.DeleteFunc(slices.Clone(report.People), func(p AnalyticsPerson) bool { return !ids[p.ID] })
		return result
	}
	result.Users = nil
	if query.Section == "user" {
		result.Total = len(report.Sites)
		result.Page = min(max(1, query.Page), max(1, (result.Total+limit-1)/limit))
		start := (result.Page - 1) * limit
		result.Sites = report.Sites[start:min(len(report.Sites), start+limit)]
	} else {
		result.Sites = nil
		result.People = nil
	}
	return result
}
