package hex

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTemplateRendering(t *testing.T) {
	data := map[string]any{
		"steps": map[string]any{
			"issues": map[string]any{"output": map[string]any{
				"total": 42.0,
				"items": []any{
					map[string]any{"title": "Crash", "level": "error", "count": 30.0},
					map[string]any{"title": "Slow", "level": "warning", "count": 12.0},
				},
			}},
		},
		"item": map[string]any{"name": "Kari", "birthday": "10-05"},
		"now":  clockValues(time.Date(2026, time.October, 5, 8, 30, 0, 0, time.UTC)),
	}
	cases := map[string]string{
		`"{{ steps.issues.output.total }}"`:                                                         `42`,
		`"Total: {{ steps.issues.output.total }} issues"`:                                           `"Total: 42 issues"`,
		`"{{ steps.issues.output.items | map(\"title\") | join(\" / \") }}"`:                        `"Crash / Slow"`,
		`"{{ steps.issues.output.items | where(\"level\", \"error\") | length }}"`:                  `1`,
		`"{{ steps.issues.output.items | sum(\"count\") }}"`:                                        `42`,
		`"{{ steps.missing.output | default(\"none\") }}"`:                                          `"none"`,
		`"{{ item.birthday | eq(now.monthDay) }}"`:                                                  `true`,
		`"Week {{ now.week }} from {{ now.previousWeekStart }} to {{ now.previousWeekEnd }}"`:       `"Week 41 from 2026-09-28 to 2026-10-04"`,
		`{"text": "Hi {{ item.name | upper }}", "n": ["{{ steps.issues.output.items[1].count }}"]}`: `{"n":[12],"text":"Hi KARI"}`,
		`"{{ now.date | date(\"DD.MM.YYYY\") }}"`:                                                   `"05.10.2026"`,
		`"{{ steps.issues.output.items | first | json }}"`:                                          `"{\"count\":30,\"level\":\"error\",\"title\":\"Crash\"}"`,
	}
	for template, want := range cases {
		rendered, err := renderTemplate(json.RawMessage(template), data)
		if err != nil {
			t.Errorf("%s: %v", template, err)
			continue
		}
		if string(rendered) != want {
			t.Errorf("%s: got %s, want %s", template, rendered, want)
		}
	}

	for _, invalid := range []string{`"{{ steps..x }}"`, `"{{ x | nope }}"`, `"{{ x | join(\"a) }}"`} {
		if _, err := renderTemplate(json.RawMessage(invalid), data); err == nil {
			t.Errorf("%s: expected an error", invalid)
		}
	}

	for condition, want := range map[string]bool{
		"steps.issues.output.total | gt(40)":                            true,
		"{{ steps.issues.output.items | where(\"level\", \"fatal\") }}": false,
		"steps.issues.output.total | gt(40) | not":                      false,
	} {
		got, err := evaluateCondition(condition, data)
		if err != nil || got != want {
			t.Errorf("%s: got %v, %v", condition, got, err)
		}
	}
}

func TestScheduleValidation(t *testing.T) {
	steps := []AutomationStep{{ID: "a", Query: &AutomationQuery{Collection: "people"}}}
	valid := []Automation{
		{Name: "weekly", Schedule: "0 8 * * MON", Timezone: "Europe/Oslo", Steps: steps},
		{Name: "daily", Schedule: "@daily", Steps: steps},
		{Name: "manual", Steps: steps},
	}
	if err := ValidateAutomations(valid); err != nil {
		t.Fatal(err)
	}
	invalid := []Automation{
		{Name: "frequent", Schedule: "* * * * *", Steps: steps},
		{Name: "bad", Schedule: "every monday", Steps: steps},
		{Name: "zone", Schedule: "@daily", Timezone: "Mars/Olympus", Steps: steps},
		{Name: "empty"},
		{Name: "forward", Steps: []AutomationStep{
			{ID: "a", Call: "x.y", Input: json.RawMessage(`{"v":"{{ steps.b.output }}"}`)},
			{ID: "b", Query: &AutomationQuery{Collection: "people"}},
		}},
		{Name: "two-kinds", Steps: []AutomationStep{{ID: "a", Call: "x.y", Action: "z"}}},
		{Name: "duplicate", Steps: []AutomationStep{
			{ID: "a", Query: &AutomationQuery{Collection: "people"}},
			{ID: "a", Query: &AutomationQuery{Collection: "people"}},
		}},
	}
	for _, automation := range invalid {
		if err := validateAutomation(automation); err == nil {
			t.Errorf("accepted invalid automation %s", automation.Name)
		}
	}

	oslo, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Skip("time zone data unavailable")
	}
	next := nextRun(valid[0], time.Date(2026, time.October, 5, 7, 0, 0, 0, time.UTC))
	if want := time.Date(2026, time.October, 12, 8, 0, 0, 0, oslo); !next.Equal(want) {
		t.Fatalf("next weekly run %s, want %s", next, want)
	}
}

func TestDescribeSchedule(t *testing.T) {
	cases := map[string]string{
		"":             "Only when run by hand",
		"@daily":       "Every day at 00:00",
		"0 9 * * *":    "Every day at 09:00",
		"30 7 * * 1-5": "Every weekday at 07:30",
		"0 8 * * MON":  "Every Monday at 08:00",
		"0 8 * * 1,3":  "Every Monday, Wednesday at 08:00",
		"0 6 1 * *":    "On day 1 of every month at 06:00",
		"*/15 * * * *": "*/15 * * * *",
	}
	for schedule, want := range cases {
		if got := describeSchedule(schedule); got != want {
			t.Errorf("%q: got %q, want %q", schedule, got, want)
		}
	}
}
