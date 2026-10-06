package hex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The portal's Automations tab shows owners what a site's automations do
// and when they run, and lets them try one as a dry run or run it now.
// Definitions stay in the app's project; the portal does not edit them.

type automationsView struct {
	Site        string
	Automations []automationCard
}

type automationCard struct {
	Name        string
	Description string
	Schedule    string
	Cron        string
	Timezone    string
	Disabled    bool
	NextRuns    []runTime
	Script      string
	LastRun     *runSummary
}

type runTime struct {
	When     string
	ISO      string
	Relative string
}

type runSummary struct {
	ID       string
	Status   string
	Tone     string
	Trigger  string
	DryRun   bool
	When     string
	ISO      string
	Duration string
	By       string
	Error    string
}

type automationRunView struct {
	Site       string
	Automation string
	Run        runSummary
	Running    bool
	Output     string
	Truncated  bool
	SourceHash string
	Logs       []scriptLogRow
	Operations []scriptOperationRow
}

type scriptLogRow struct {
	Message string
	Data    string
}

type scriptOperationRow struct {
	Kind      string
	Target    string
	Status    string
	Duration  string
	Output    string
	Error     string
	Truncated bool
}

type automationRunsView struct {
	Site       string
	Automation string
	Runs       []runSummary
}

func (s *Server) registerManageAutomationRoutes() {
	if !s.automationsEnabled() {
		return
	}
	s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/automations", s.manageAutomations)
	s.mux.HandleFunc("POST /api/hex/manage/sites/{site}/automations/{automation}/run", s.manageRunAutomation)
	s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/automations/{automation}/runs", s.manageAutomationRuns)
	s.mux.HandleFunc("GET /api/hex/manage/sites/{site}/automation-runs/{run}", s.manageAutomationRun)
}

func (s *Server) manageAutomations(w http.ResponseWriter, r *http.Request) {
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	stored, err := s.config.Automations.ListSiteAutomations(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	now := time.Now()
	view := automationsView{Site: site}
	for _, entry := range stored {
		card := automationCardFor(entry, now)
		runs, err := s.config.Automations.ListAutomationRuns(r.Context(), site, entry.Automation.Name, 1)
		if err != nil {
			writeServerError(w, err)
			return
		}
		if len(runs) > 0 {
			summary := summarizeRun(runs[0], now)
			card.LastRun = &summary
		}
		view.Automations = append(view.Automations, card)
	}
	s.renderFragment(w, "automations", view)
}

func automationCardFor(entry ScheduledAutomation, now time.Time) automationCard {
	automation := entry.Automation
	location, err := automationLocation(automation)
	if err != nil {
		location = time.UTC
	}
	card := automationCard{
		Name: automation.Name, Description: automation.Description,
		Cron: automation.Schedule, Timezone: location.String(), Disabled: automation.Disabled,
		Schedule: describeSchedule(automation.Schedule),
	}
	if !entry.NextRun.IsZero() {
		card.NextRuns = upcomingRuns(automation, entry.NextRun, location, now)
	}
	if automation.Script != nil {
		card.Script = scriptFilename(*automation.Script, automation.Name)
	}
	return card
}

// upcomingRuns lists the next three occurrences, starting with the stored
// next run, in the automation's time zone.
func upcomingRuns(automation Automation, next time.Time, location *time.Location, now time.Time) []runTime {
	runs := []runTime{}
	for len(runs) < 3 && !next.IsZero() {
		local := next.In(location)
		runs = append(runs, runTime{
			When:     local.Format("Mon Jan 2, 15:04"),
			ISO:      local.Format(time.RFC3339),
			Relative: untilTime(next, now),
		})
		next = nextRun(automation, next)
	}
	return runs
}

func untilTime(t, now time.Time) string {
	remaining := t.Sub(now)
	switch {
	case remaining <= time.Minute:
		return "due now"
	case remaining < time.Hour:
		return "in " + plural(int(remaining.Minutes()), "minute")
	case remaining < 48*time.Hour:
		return "in " + plural(int(remaining.Hours()), "hour")
	default:
		return "in " + plural(int(remaining.Hours()/24), "day")
	}
}

var weekdayNames = map[string]string{
	"0": "Sunday", "1": "Monday", "2": "Tuesday", "3": "Wednesday", "4": "Thursday", "5": "Friday", "6": "Saturday", "7": "Sunday",
	"SUN": "Sunday", "MON": "Monday", "TUE": "Tuesday", "WED": "Wednesday", "THU": "Thursday", "FRI": "Friday", "SAT": "Saturday",
}

// describeSchedule puts common schedules in words; anything else is shown
// as its cron expression.
func describeSchedule(schedule string) string {
	switch schedule {
	case "":
		return "Only when run by hand"
	case "@hourly":
		return "Every hour"
	case "@daily", "@midnight":
		return "Every day at 00:00"
	case "@weekly":
		return "Every Sunday at 00:00"
	case "@monthly":
		return "On the 1st of every month at 00:00"
	case "@yearly", "@annually":
		return "Every January 1st at 00:00"
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return schedule
	}
	minute, hour, day, month, weekday := fields[0], fields[1], fields[2], fields[3], fields[4]
	minuteNumber, minuteErr := strconv.Atoi(minute)
	hourNumber, hourErr := strconv.Atoi(hour)
	if minuteErr != nil || hourErr != nil || month != "*" {
		return schedule
	}
	at := fmt.Sprintf("at %02d:%02d", hourNumber, minuteNumber)
	switch {
	case day == "*" && weekday == "*":
		return "Every day " + at
	case day == "*" && (weekday == "1-5" || strings.EqualFold(weekday, "MON-FRI")):
		return "Every weekday " + at
	case day == "*":
		names := []string{}
		for _, part := range strings.Split(weekday, ",") {
			name, known := weekdayNames[strings.ToUpper(part)]
			if !known {
				return schedule
			}
			names = append(names, name)
		}
		return "Every " + strings.Join(names, ", ") + " " + at
	case weekday == "*":
		if _, err := strconv.Atoi(day); err != nil {
			return schedule
		}
		return "On day " + day + " of every month " + at
	default:
		return schedule
	}
}

func summarizeRun(run AutomationRun, now time.Time) runSummary {
	summary := runSummary{
		ID: run.ID, Status: run.Status, Trigger: run.Trigger, DryRun: run.DryRun,
		When: relativeTime(run.StartedAt, now), ISO: run.StartedAt.Format(time.RFC3339),
		By: personName(run.StartedBy), Error: run.Error, Tone: runTone(run.Status),
	}
	if !run.FinishedAt.IsZero() {
		summary.Duration = formatDuration(run.FinishedAt.Sub(run.StartedAt))
	}
	return summary
}

func runTone(status string) string {
	switch status {
	case RunSucceeded:
		return "ok"
	case RunFailed:
		return "danger"
	case RunRunning, OperationDryRun:
		return "warning"
	default:
		return "muted"
	}
}

func formatDuration(duration time.Duration) string {
	if duration < time.Second {
		return strconv.FormatInt(duration.Milliseconds(), 10) + " ms"
	}
	return duration.Round(100 * time.Millisecond).String()
}

// manageRunAutomation starts the deployed automation, as a dry run unless
// the form asks for a live run, and answers with its progress.
func (s *Server) manageRunAutomation(w http.ResponseWriter, r *http.Request) {
	identity, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	stored, err := s.config.Automations.ListSiteAutomations(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	name := r.PathValue("automation")
	index := slices.IndexFunc(stored, func(entry ScheduledAutomation) bool { return entry.Automation.Name == name })
	if index < 0 {
		writeError(w, http.StatusNotFound, "automation not found")
		return
	}
	trigger := TriggerManual
	dryRun := r.URL.Query().Get("live") != "true"
	if dryRun {
		trigger = TriggerTest
	}
	run, err := s.startRun(site, stored[index].Automation, stored[index].Revision, trigger, dryRun, identity)
	if err != nil {
		writeAutomationStartError(w, err)
		return
	}
	s.renderFragment(w, "automation-run", runView(site, run))
}

func (s *Server) manageAutomationRun(w http.ResponseWriter, r *http.Request) {
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	run, err := s.config.Automations.GetAutomationRun(r.Context(), site, r.PathValue("run"))
	if err != nil {
		writeServerError(w, err)
		return
	}
	s.renderFragment(w, "automation-run", runView(site, run))
}

func runView(site string, run AutomationRun) automationRunView {
	view := automationRunView{
		Site: site, Automation: run.Automation, Run: summarizeRun(run, time.Now()),
		Running: run.Status == RunRunning,
		Output:  prettyOutput(run.Output), Truncated: run.Truncated, SourceHash: run.SourceHash,
	}
	for _, log := range run.Logs {
		view.Logs = append(view.Logs, scriptLogRow{Message: log.Message, Data: prettyOutput(log.Data)})
	}
	for _, operation := range run.Operations {
		view.Operations = append(view.Operations, scriptOperationRow{
			Kind: operation.Kind, Target: operation.Target, Status: operation.Status,
			Duration: formatDuration(time.Duration(operation.DurationMS) * time.Millisecond),
			Output:   prettyOutput(operation.Output), Error: operation.Error, Truncated: operation.Truncated,
		})
	}
	return view
}

func prettyOutput(output json.RawMessage) string {
	if len(output) == 0 || string(output) == "null" {
		return ""
	}
	var value any
	if err := json.Unmarshal(output, &value); err != nil {
		return string(output)
	}
	formatted, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return string(output)
	}
	return string(formatted)
}

func (s *Server) manageAutomationRuns(w http.ResponseWriter, r *http.Request) {
	_, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	name := r.PathValue("automation")
	runs, err := s.config.Automations.ListAutomationRuns(r.Context(), site, name, defaultRunHistoryLimit)
	if err != nil {
		writeServerError(w, err)
		return
	}
	now := time.Now()
	view := automationRunsView{Site: site, Automation: name}
	for _, run := range runs {
		view.Runs = append(view.Runs, summarizeRun(run, now))
	}
	s.renderFragment(w, "automation-runs", view)
}
