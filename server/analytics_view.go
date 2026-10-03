package hex

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Presentation for the analytics pages: plain-language metric cards with a
// comparison to the previous period, a daily trend chart, ranked lists and
// an activity feed. Everything is server-rendered; the chart is SVG drawn
// with presentation attributes because the portal's policy forbids inline
// styles.

// periodChoice is one of the quick date ranges above a report.
type periodChoice struct {
	Label   string
	URL     string
	Current bool
	// The link refreshes only the report with HTMX: the site's analytics
	// panel when there is a fragment URL, otherwise the admin content.
	RequestURL string
	Target     string
	PushURL    string
}

func newPeriodLink(label, pageURL, fragmentURL string, current bool) periodChoice {
	if fragmentURL != "" {
		return periodChoice{Label: label, URL: pageURL, Current: current, RequestURL: fragmentURL, Target: "#site-analytics", PushURL: pageURL}
	}
	return periodChoice{Label: label, URL: pageURL, Current: current, RequestURL: pageURL, Target: "#admin-content", PushURL: "true"}
}

var periodPresets = []struct {
	days  int
	label string
}{
	{7, "7 days"},
	{30, "30 days"},
	{90, "90 days"},
	{365, "12 months"},
}

// periodChoices links each preset ending today. pageURL and fragmentURL
// receive the encoded query; an empty fragmentURL means full-page links.
func periodChoices(query AnalyticsQuery, now time.Time, keep url.Values, pageURL, fragmentURL string) ([]periodChoice, bool) {
	today := now.UTC().Truncate(24 * time.Hour)
	endsToday := query.Until.Equal(today.AddDate(0, 0, 1))
	length := int(query.Until.Sub(query.From).Hours() / 24)
	matched := false
	choices := make([]periodChoice, 0, len(periodPresets))
	for _, preset := range periodPresets {
		values := url.Values{}
		for name, value := range keep {
			values[name] = value
		}
		values.Set("from", today.AddDate(0, 0, 1-preset.days).Format(time.DateOnly))
		values.Set("until", today.Format(time.DateOnly))
		link := pageURL + "?" + values.Encode()
		fragment := ""
		if fragmentURL != "" {
			fragment = fragmentURL + "?" + values.Encode()
		}
		choice := newPeriodLink(preset.label, link, fragment, endsToday && length == preset.days)
		matched = matched || choice.Current
		choices = append(choices, choice)
	}
	return choices, !matched
}

// periodLabel describes the selected range in words.
func periodLabel(query AnalyticsQuery, now time.Time) string {
	days := int(query.Until.Sub(query.From).Hours() / 24)
	today := now.UTC().Truncate(24 * time.Hour)
	if query.Until.Equal(today.AddDate(0, 0, 1)) {
		for _, preset := range periodPresets {
			if preset.days == days {
				return "Last " + preset.label
			}
		}
	}
	last := query.Until.AddDate(0, 0, -1)
	if query.From.Equal(last) {
		return query.From.Format("Jan 2, 2006")
	}
	return shortDate(query.From, last) + " – " + last.Format("Jan 2, 2006")
}

func shortDate(at, reference time.Time) string {
	if at.Year() == reference.Year() {
		return at.Format("Jan 2")
	}
	return at.Format("Jan 2, 2006")
}

// previousPeriod is the range of the same length just before the query.
func previousPeriod(query AnalyticsQuery) AnalyticsQuery {
	previous := query
	length := query.Until.Sub(query.From)
	previous.Until = query.From
	previous.From = query.From.Add(-length)
	return previous
}

// peopleCount reads naturally for any count: 1 person, 12 people.
func peopleCount(count int) string {
	if count == 1 {
		return "1 person"
	}
	return formatCount(int64(count)) + " people"
}

// metricCard is one headline number.
type metricCard struct {
	Label       string
	Value       string
	Hint        string
	Change      string
	ChangeTone  string
	ChangeLabel string
}

func newMetric(label string, current, previous int64, comparable bool, comparison string) metricCard {
	card := metricCard{Label: label, Value: formatCount(current)}
	if !comparable || previous == 0 {
		return card
	}
	percent := math.Round(float64(current-previous) * 100 / float64(previous))
	switch {
	case percent > 0:
		card.Change, card.ChangeTone = fmt.Sprintf("+%.0f%%", percent), "up"
	case percent < 0:
		card.Change, card.ChangeTone = fmt.Sprintf("−%.0f%%", -percent), "down"
	default:
		card.Change, card.ChangeTone = "No change", "flat"
	}
	card.ChangeLabel = "vs " + comparison
	return card
}

// formatCount groups thousands for readability: 12,345.
func formatCount(value int64) string {
	digits := strconv.FormatInt(value, 10)
	negative := strings.HasPrefix(digits, "-")
	digits = strings.TrimPrefix(digits, "-")
	var grouped strings.Builder
	for index, digit := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	if negative {
		return "-" + grouped.String()
	}
	return grouped.String()
}

// trendChart draws daily page views as bars and visitors as a line.
// Each day is 10 units wide in a 100-unit-high plot; the SVG stretches to
// the available width, and labels are HTML so they never distort.
type trendChart struct {
	Width       int
	Days        []chartDay
	Grid        []chartGridLine
	YLabels     []string
	XLabels     []string
	VisitorLine string
	Summary     string
	Empty       bool
	Peak        string
}

type chartDay struct {
	Date      string
	Label     string
	X         int
	BarX      string
	BarY      string
	BarHeight string
	BarWidth  string
	Weekend   bool
	Views     string
	Visitors  string
}

type chartGridLine struct {
	Y string
}

const chartTicks = 4

func newTrendChart(days []AnalyticsDay) trendChart {
	chart := trendChart{Width: max(1, len(days)) * 10, Empty: true}
	var peak int64
	var peakDay AnalyticsDay
	var total int64
	for _, day := range days {
		total += day.PageViews
		if day.PageViews > peak {
			peak = day.PageViews
			peakDay = day
		}
	}
	scale := niceCeiling(peak)
	for tick := 0; tick <= chartTicks; tick++ {
		value := scale * int64(chartTicks-tick) / chartTicks
		chart.YLabels = append(chart.YLabels, formatCount(value))
		chart.Grid = append(chart.Grid, chartGridLine{Y: fmt.Sprintf("%.2f", float64(tick)*100/chartTicks)})
	}

	barWidth := 7.0
	if len(days) > 120 {
		barWidth = 9
	}
	var line strings.Builder
	for index, day := range days {
		at, _ := time.Parse(time.DateOnly, day.Date)
		height := float64(day.PageViews) * 100 / float64(scale)
		entry := chartDay{
			Date:      day.Date,
			Label:     at.Format("Mon, Jan 2"),
			X:         index * 10,
			BarX:      fmt.Sprintf("%.2f", float64(index*10)+(10-barWidth)/2),
			BarWidth:  fmt.Sprintf("%.2f", barWidth),
			BarY:      fmt.Sprintf("%.2f", 100-height),
			BarHeight: fmt.Sprintf("%.2f", height),
			Weekend:   at.Weekday() == time.Saturday || at.Weekday() == time.Sunday,
			Views:     formatCount(day.PageViews),
			Visitors:  formatCount(int64(day.Visitors)),
		}
		chart.Days = append(chart.Days, entry)

		command := "L"
		if index == 0 {
			command = "M"
		}
		y := 100 - float64(day.Visitors)*100/float64(scale)
		fmt.Fprintf(&line, "%s%.2f %.2f ", command, float64(index*10)+5, y)
	}
	chart.VisitorLine = strings.TrimSpace(line.String())
	chart.XLabels = axisLabels(days)
	if total > 0 {
		chart.Empty = false
		at, _ := time.Parse(time.DateOnly, peakDay.Date)
		chart.Peak = fmt.Sprintf("%s · %s", at.Format("Mon, Jan 2"), plural(int(peak), "page view"))
	}
	chart.Summary = fmt.Sprintf("Daily page views and visitors, %s in total.", plural(int(total), "page view"))
	return chart
}

// niceCeiling rounds the axis maximum up to 1, 2 or 5 times a power of ten,
// with at least four units so the gridlines are whole numbers.
func niceCeiling(value int64) int64 {
	if value <= chartTicks {
		return chartTicks
	}
	magnitude := int64(math.Pow(10, math.Floor(math.Log10(float64(value)))))
	for _, step := range []int64{1, 2, 5, 10} {
		if step*magnitude >= value {
			return step * magnitude
		}
	}
	return 10 * magnitude
}

// axisLabels picks up to five evenly spaced dates, always the first and last.
func axisLabels(days []AnalyticsDay) []string {
	if len(days) == 0 {
		return nil
	}
	count := min(5, len(days))
	labels := make([]string, 0, count)
	last, _ := time.Parse(time.DateOnly, days[len(days)-1].Date)
	for step := 0; step < count; step++ {
		index := 0
		if count > 1 {
			index = step * (len(days) - 1) / (count - 1)
		}
		at, _ := time.Parse(time.DateOnly, days[index].Date)
		labels = append(labels, shortDate(at, last))
	}
	return labels
}

// rankedRow is one line of a "top" list with a proportional bar.
type rankedRow struct {
	Title    string
	Detail   string
	URL      string
	Value    string
	Percent  string
	Initials string
	Tone     string
	Card     *siteCard
}

func barPercent(value, peak int64) string {
	if peak <= 0 {
		return "0"
	}
	return fmt.Sprintf("%.1f", math.Max(2, float64(value)*100/float64(peak)))
}

// activityItem is one publishing event in words.
type activityItem struct {
	Who      string
	Verb     string
	Site     string
	SiteURL  string
	When     string
	WhenISO  string
	Initials string
	Tone     string
}

func activityItems(events []SiteEvent, from, until string, now time.Time) []activityItem {
	items := make([]activityItem, 0, len(events))
	for _, event := range events {
		who := "Someone"
		key := event.Site
		if event.Actor != nil {
			key = event.Actor.ID
			who = event.Actor.ID
			if event.Actor.Name != "" {
				who = event.Actor.Name
			}
		}
		verb := event.Kind
		switch event.Kind {
		case "created":
			verb = "created"
		case "published":
			verb = "updated"
		case "unpublished":
			verb = "took down"
		}
		title := event.Title
		if title == "" {
			title = event.Site
		}
		link := url.Values{"from": {from}, "until": {until}}
		items = append(items, activityItem{
			Who: who, Verb: verb, Site: title,
			SiteURL:  "/admin/sites/" + event.Site + "?" + link.Encode(),
			When:     relativeTime(event.At, now),
			WhenISO:  event.At.UTC().Format(time.RFC3339),
			Initials: initials(who),
			Tone:     tone(key),
		})
	}
	return items
}

// collectionStatus summarizes whether traffic is being recorded.
type collectionStatus struct {
	Tone    string
	Label   string
	Details string
}

func newCollectionStatus(collector *TrafficCollectorStatus, lastCollected time.Time, now time.Time) collectionStatus {
	switch {
	case collector != nil && !collector.Running:
		return collectionStatus{Tone: "danger", Label: "Traffic collection has stopped"}
	case lastCollected.IsZero():
		return collectionStatus{Tone: "warning", Label: "Waiting for the first visit"}
	case now.Sub(lastCollected) > 48*time.Hour:
		return collectionStatus{Tone: "warning", Label: "No visits recorded since " + relativeTime(lastCollected, now)}
	default:
		return collectionStatus{Tone: "ok", Label: "Collecting traffic · last visit recorded " + relativeTime(lastCollected, now)}
	}
}
