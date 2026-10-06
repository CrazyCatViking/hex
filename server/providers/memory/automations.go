package memory

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

const maxRunsPerAutomation = 50

// AutomationStore keeps automations and their runs in process memory, for
// local development and tests.
type AutomationStore struct {
	mu          sync.Mutex
	automations map[string][]hex.ScheduledAutomation
	runs        map[string]hex.AutomationRun
}

func NewAutomationStore() *AutomationStore {
	return &AutomationStore{
		automations: make(map[string][]hex.ScheduledAutomation),
		runs:        make(map[string]hex.AutomationRun),
	}
}

func (s *AutomationStore) ReplaceSiteAutomations(ctx context.Context, site string, automations []hex.ScheduledAutomation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	proposed, err := cloneScheduledAutomations(automations)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(automations) == 0 {
		delete(s.automations, site)
		return nil
	}
	current := make(map[string]hex.ScheduledAutomation, len(s.automations[site]))
	for _, entry := range s.automations[site] {
		current[entry.Automation.Name] = entry
	}
	for i := range proposed {
		entry := &proposed[i]
		entry.Site = site
		entry.Revision = rand.Text()
		if previous, exists := current[entry.Automation.Name]; exists && hex.SameAutomationSchedule(previous.Automation, entry.Automation) {
			entry.NextRun = previous.NextRun
		}
	}
	s.automations[site] = proposed
	return nil
}

func (s *AutomationStore) ListSiteAutomations(ctx context.Context, site string) ([]hex.ScheduledAutomation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := cloneScheduledAutomations(s.automations[site])
	if err != nil {
		return nil, err
	}
	slices.SortFunc(result, func(a, b hex.ScheduledAutomation) int {
		return strings.Compare(a.Automation.Name, b.Automation.Name)
	})
	if result == nil {
		result = []hex.ScheduledAutomation{}
	}
	return result, nil
}

func (s *AutomationStore) DueAutomations(ctx context.Context, now time.Time, limit int) ([]hex.ScheduledAutomation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []hex.ScheduledAutomation
	for _, automations := range s.automations {
		for _, automation := range automations {
			if !automation.NextRun.IsZero() && !automation.NextRun.After(now) {
				due = append(due, automation)
			}
		}
	}
	slices.SortFunc(due, func(a, b hex.ScheduledAutomation) int { return a.NextRun.Compare(b.NextRun) })
	if len(due) > limit {
		due = due[:limit]
	}
	return cloneScheduledAutomations(due)
}

func (s *AutomationStore) ClaimAutomation(ctx context.Context, site, name, revision string, expected, next time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	automations := s.automations[site]
	for index := range automations {
		if automations[index].Automation.Name == name && automations[index].Revision == revision &&
			!expected.IsZero() && automations[index].NextRun.Equal(expected) {
			automations[index].NextRun = next
			return true, nil
		}
	}
	return false, nil
}

func cloneScheduledAutomations(automations []hex.ScheduledAutomation) ([]hex.ScheduledAutomation, error) {
	data, err := json.Marshal(automations)
	if err != nil {
		return nil, fmt.Errorf("encode automations: %w", err)
	}
	cloned := make([]hex.ScheduledAutomation, 0)
	if len(automations) > 0 {
		if err := json.Unmarshal(data, &cloned); err != nil {
			return nil, fmt.Errorf("decode automations: %w", err)
		}
	}
	return cloned, nil
}

func (s *AutomationStore) RecordAutomationRun(ctx context.Context, run hex.AutomationRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run

	var same []hex.AutomationRun
	for _, existing := range s.runs {
		if existing.Site == run.Site && existing.Automation == run.Automation {
			same = append(same, existing)
		}
	}
	if len(same) > maxRunsPerAutomation {
		slices.SortFunc(same, func(a, b hex.AutomationRun) int { return b.StartedAt.Compare(a.StartedAt) })
		for _, old := range same[maxRunsPerAutomation:] {
			delete(s.runs, old.ID)
		}
	}
	return nil
}

func (s *AutomationStore) ListAutomationRuns(ctx context.Context, site, name string, limit int) ([]hex.AutomationRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := make([]hex.AutomationRun, 0)
	for _, run := range s.runs {
		if run.Site == site && run.Automation == name {
			runs = append(runs, run)
		}
	}
	slices.SortFunc(runs, func(a, b hex.AutomationRun) int { return b.StartedAt.Compare(a.StartedAt) })
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (s *AutomationStore) GetAutomationRun(ctx context.Context, site, id string) (hex.AutomationRun, error) {
	if err := ctx.Err(); err != nil {
		return hex.AutomationRun{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.runs[id]
	if !exists || run.Site != site {
		return hex.AutomationRun{}, hex.ErrNotFound
	}
	return run, nil
}
