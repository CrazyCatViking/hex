package hex

import "time"

// clockValues is a snapshot of the run's start time in its configured timezone.
func clockValues(now time.Time) map[string]any {
	weekday := (int(now.Weekday()) + 6) % 7
	weekStart := now.AddDate(0, 0, -weekday)
	year, week := now.ISOWeek()
	return map[string]any{
		"iso":               now.Format(time.RFC3339),
		"date":              now.Format(time.DateOnly),
		"time":              now.Format("15:04"),
		"year":              now.Year(),
		"month":             int(now.Month()),
		"day":               now.Day(),
		"monthDay":          now.Format("01-02"),
		"weekday":           now.Weekday().String(),
		"week":              week,
		"weekYear":          year,
		"yesterday":         now.AddDate(0, 0, -1).Format(time.DateOnly),
		"weekStart":         weekStart.Format(time.DateOnly),
		"previousWeekStart": weekStart.AddDate(0, 0, -7).Format(time.DateOnly),
		"previousWeekEnd":   weekStart.AddDate(0, 0, -1).Format(time.DateOnly),
		"unix":              now.Unix(),
	}
}
