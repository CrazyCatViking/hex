package hex

import (
	"testing"
	"time"
)

func TestScheduleValidation(t *testing.T) {
	script := &AutomationScript{Source: `export default () => 42;`}
	valid := []Automation{
		{Name: "weekly", Schedule: "0 8 * * MON", Timezone: "Europe/Oslo", Script: script},
		{Name: "daily", Schedule: "@daily", Script: script},
		{Name: "manual", Script: script},
	}
	if err := ValidateAutomations(valid); err != nil {
		t.Fatal(err)
	}
	invalid := []Automation{
		{Name: "frequent", Schedule: "* * * * *", Script: script},
		{Name: "bad", Schedule: "every monday", Script: script},
		{Name: "zone", Timezone: "Mars/Olympus", Script: script},
		{Name: "missing"},
		{Name: "empty", Script: &AutomationScript{}},
		{Name: "typescript", Script: &AutomationScript{File: "test.ts", Source: script.Source}},
		{Name: "syntax", Script: &AutomationScript{Source: `export default ( => 1;`}},
	}
	for _, automation := range invalid {
		if err := validateAutomation(automation); err == nil {
			t.Errorf("accepted invalid automation %s", automation.Name)
		}
	}
	if err := ValidateAutomations([]Automation{valid[0], valid[0]}); err == nil {
		t.Fatal("accepted duplicate names")
	}
	oslo, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatal(err)
	}
	next := nextRun(valid[0], time.Date(2026, time.October, 5, 7, 0, 0, 0, time.UTC))
	if want := time.Date(2026, time.October, 12, 8, 0, 0, 0, oslo); !next.Equal(want) {
		t.Fatalf("next weekly run %s, want %s", next, want)
	}
}

func TestAutomationClockUsesLocalDatesAndISOWeeks(t *testing.T) {
	oslo, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatal(err)
	}
	clock := clockValues(time.Date(2027, time.January, 1, 0, 30, 0, 0, oslo))
	if clock["date"] != "2027-01-01" || clock["iso"] != "2027-01-01T00:30:00+01:00" || clock["year"] != 2027 || clock["weekYear"] != 2026 || clock["week"] != 53 || clock["previousWeekStart"] != "2026-12-21" || clock["previousWeekEnd"] != "2026-12-27" {
		t.Fatalf("wrong local clock snapshot: %+v", clock)
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
