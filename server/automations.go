package hex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
)

// Automation configures when a site's JavaScript module runs. All workflow
// logic lives in Script; definitions contain scheduling metadata only.
// Automations act as their site, never as the person deploying or triggering
// them. Integrations require site grants and approvals; connected accounts are
// unavailable. Within their site, automations act as owners.
type Automation struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Schedule    string            `json:"schedule,omitempty"`
	Timezone    string            `json:"timezone,omitempty"`
	Disabled    bool              `json:"disabled,omitempty"`
	Script      *AutomationScript `json:"script"`
}

// AutomationScript is a JavaScript ES module with a default run(hex) export.
// Project metadata references File; the CLI includes Source for deployment.
// The server executes the stored Source and never reads File from disk.
type AutomationScript struct {
	File   string `json:"file,omitempty"`
	Source string `json:"source,omitempty"`
}

// ScheduledAutomation includes the store-assigned revision and next run.
// A zero NextRun disables scheduling, including for manual-only automations.
type ScheduledAutomation struct {
	Site       string     `json:"site"`
	Automation Automation `json:"automation"`
	Revision   string     `json:"revision"`
	NextRun    time.Time  `json:"nextRun,omitzero"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	UpdatedBy  *Person    `json:"updatedBy,omitempty"`
}

// AutomationRun records one script's result and bounded operation history.
type AutomationRun struct {
	ID         string                `json:"id"`
	Site       string                `json:"site"`
	Automation string                `json:"automation"`
	Trigger    string                `json:"trigger"`
	DryRun     bool                  `json:"dryRun,omitempty"`
	Status     string                `json:"status"`
	StartedAt  time.Time             `json:"startedAt"`
	FinishedAt time.Time             `json:"finishedAt,omitzero"`
	Error      string                `json:"error,omitempty"`
	StartedBy  *Person               `json:"startedBy,omitempty"`
	Revision   string                `json:"revision,omitempty"`
	SourceHash string                `json:"sourceHash,omitempty"`
	Output     json.RawMessage       `json:"output,omitempty"`
	Truncated  bool                  `json:"truncated,omitempty"`
	Logs       []AutomationLog       `json:"logs,omitempty"`
	Operations []AutomationOperation `json:"operations,omitempty"`
}

type AutomationLog struct {
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type AutomationOperation struct {
	Kind       string          `json:"kind"`
	Target     string          `json:"target,omitempty"`
	Status     string          `json:"status"`
	DurationMS int64           `json:"durationMs"`
	Output     json.RawMessage `json:"output,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
	Error      string          `json:"error,omitempty"`
}

const (
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"

	OperationSucceeded = "succeeded"
	OperationFailed    = "failed"
	OperationDryRun    = "dry-run"

	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerTest     = "test"
)

// AutomationStore keeps definitions, scheduling state and history.
// Whole-site replacement must serialize with claims, including absent names,
// assign fresh non-reusable revisions, and preserve the authoritative stored
// NextRun for unchanged schedules (SameAutomationSchedule), including zero.
// Incoming NextRun values are proposals for new or changed schedules only.
// Claims compare both revision and NextRun before advancing the occurrence.
// Returned definitions must be detached snapshots. Stores retain at least the
// latest 50 runs per automation.
type AutomationStore interface {
	ReplaceSiteAutomations(ctx context.Context, site string, automations []ScheduledAutomation) error
	ListSiteAutomations(ctx context.Context, site string) ([]ScheduledAutomation, error)
	DueAutomations(ctx context.Context, now time.Time, limit int) ([]ScheduledAutomation, error)
	ClaimAutomation(ctx context.Context, site, name, revision string, expected, next time.Time) (bool, error)
	RecordAutomationRun(ctx context.Context, run AutomationRun) error
	ListAutomationRuns(ctx context.Context, site, name string, limit int) ([]AutomationRun, error)
	GetAutomationRun(ctx context.Context, site, id string) (AutomationRun, error)
}

const (
	maxAutomations            = 32
	maxQueryDocuments         = 1000
	maxStoredAutomationOutput = 16 << 10
	maxAutomationOutput       = 1 << 20
	automationRunTimeout      = 10 * time.Minute
	minimumScheduleInterval   = 5 * time.Minute
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

func (s *Server) automationsEnabled() bool {
	return s.config.Automations != nil
}

func ValidateAutomations(automations []Automation) error {
	return ValidateAutomationsContext(context.Background(), automations)
}

// ValidateAutomationsContext cancels validation with its caller.
func ValidateAutomationsContext(ctx context.Context, automations []Automation) error {
	if len(automations) > maxAutomations {
		return fmt.Errorf("a site can have at most %d automations", maxAutomations)
	}
	names := make(map[string]bool)
	for _, automation := range automations {
		if err := validateAutomationContext(ctx, automation); err != nil {
			return err
		}
		if names[automation.Name] {
			return fmt.Errorf("automation %s is declared more than once", automation.Name)
		}
		names[automation.Name] = true
	}
	return nil
}

func validateAutomation(automation Automation) error {
	return validateAutomationContext(context.Background(), automation)
}

func validateAutomationContext(ctx context.Context, automation Automation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !namePattern.MatchString(automation.Name) {
		return fmt.Errorf("invalid automation name %q; use letters, digits, - and _", automation.Name)
	}
	label := "automation " + automation.Name
	if utf8.RuneCountInString(automation.Description) > maxActionDescriptionSize {
		return fmt.Errorf("%s: the description is longer than %d characters", label, maxActionDescriptionSize)
	}
	if _, err := automationLocation(automation); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if automation.Schedule != "" {
		if err := validateSchedule(automation); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	if automation.Script == nil {
		return fmt.Errorf("%s requires a JavaScript script", label)
	}
	if err := validateAutomationScript(ctx, *automation.Script); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func automationLocation(automation Automation) (*time.Location, error) {
	if automation.Timezone == "" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(automation.Timezone)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", automation.Timezone)
	}
	return location, nil
}

func validateSchedule(automation Automation) error {
	schedule, err := cronParser.Parse(automation.Schedule)
	if err != nil {
		return fmt.Errorf("invalid schedule %q: %w", automation.Schedule, err)
	}
	location, err := automationLocation(automation)
	if err != nil {
		return err
	}
	start := time.Date(2026, time.January, 5, 0, 0, 0, 0, location)
	previous := schedule.Next(start)
	for range 300 {
		next := schedule.Next(previous)
		if next.IsZero() {
			break
		}
		if next.Sub(previous) < minimumScheduleInterval {
			return fmt.Errorf("schedule %q runs more often than every %s", automation.Schedule, minimumScheduleInterval)
		}
		previous = next
	}
	return nil
}

func nextRun(automation Automation, now time.Time) time.Time {
	if automation.Disabled || automation.Schedule == "" {
		return time.Time{}
	}
	schedule, err := cronParser.Parse(automation.Schedule)
	if err != nil {
		return time.Time{}
	}
	location, err := automationLocation(automation)
	if err != nil {
		return time.Time{}
	}
	return schedule.Next(now.In(location)).UTC()
}

func (s *Server) replaceAutomations(ctx context.Context, site string, automations []Automation, identity *Identity) error {
	if len(automations) > 0 && !s.automationsEnabled() {
		return errors.New("this platform has no automation store, so it cannot run automations")
	}
	if !s.automationsEnabled() {
		return nil
	}
	now := time.Now().UTC()
	scheduled := make([]ScheduledAutomation, 0, len(automations))
	for _, automation := range automations {
		scheduled = append(scheduled, ScheduledAutomation{
			Site: site, Automation: automation, NextRun: nextRun(automation, now),
			UpdatedAt: now, UpdatedBy: personOf(identity),
		})
	}
	return s.config.Automations.ReplaceSiteAutomations(ctx, site, scheduled)
}

// Script and description changes preserve schedule position, but each store
// replacement still assigns a new revision to invalidate stale snapshots.
func SameAutomationSchedule(a, b Automation) bool {
	return a.Schedule == b.Schedule && a.Timezone == b.Timezone && a.Disabled == b.Disabled
}

type automationRunner struct {
	server           *Server
	site             string
	automation       Automation
	run              *AutomationRun
	caller           integrationCaller
	scriptCalls      int
	scannedDocuments int
	scannedBytes     int
	hostTimeUsed     time.Duration
	hostBudgetError  error
}

func (s *Server) runAutomation(ctx context.Context, site string, automation Automation, run *AutomationRun) {
	ctx, cancel := context.WithTimeout(ctx, automationRunTimeout)
	defer cancel()
	runner := &automationRunner{
		server: s, site: site, automation: automation, run: run,
		caller: integrationCaller{site: site, automation: automation.Name, role: roleOwner, dryRun: run.DryRun},
	}
	if automation.Script != nil {
		digest := sha256.Sum256([]byte(automation.Script.Source))
		run.SourceHash = fmt.Sprintf("%x", digest)
	}
	output, err := runner.executeScript(ctx)
	if err != nil {
		run.Status = RunFailed
		run.Error = err.Error()
	} else {
		run.Status = RunSucceeded
		run.Output, run.Truncated = truncateOutput(output)
	}
	run.FinishedAt = time.Now().UTC()
	slog.Info("automation run", "site", site, "automation", automation.Name, "trigger", run.Trigger,
		"status", run.Status, "dryRun", run.DryRun, "duration", run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond))
	s.recordRun(*run)
}

func (s *Server) recordRun(run AutomationRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.config.Automations.RecordAutomationRun(ctx, run); err != nil {
		slog.Error("record automation run", "site", run.Site, "automation", run.Automation, "error", err)
	}
}

func truncateOutput(encoded []byte) (json.RawMessage, bool) {
	if len(encoded) <= maxStoredAutomationOutput {
		return encoded, false
	}
	preview, err := json.Marshal(string(encoded[:maxStoredAutomationOutput]))
	if err != nil {
		return nil, true
	}
	return preview, true
}
