package memory

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	hex "github.com/crazycatviking/hex/server"
)

// AIUsageStore keeps AI usage records and budgets in process memory, for
// local development and tests.
type AIUsageStore struct {
	mu           sync.Mutex
	records      []hex.AIUsageRecord
	budgets      map[string]hex.AIBudget
	reservations map[string]hex.AIUsageReservation
}

func NewAIUsageStore() *AIUsageStore {
	return &AIUsageStore{budgets: make(map[string]hex.AIBudget), reservations: make(map[string]hex.AIUsageReservation)}
}

func (s *AIUsageStore) hasRecord(id string) bool {
	for _, record := range s.records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func (s *AIUsageStore) ReserveAIUsage(ctx context.Context, reservation hex.AIUsageReservation, checks []hex.AIBudgetCheck) error {
	reservation.EstimatedUsage.Estimated = true
	if err := ctx.Err(); err != nil {
		return err
	}
	if reservation.ID == "" || reservation.CostMicros < 0 {
		return fmt.Errorf("invalid AI reservation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasRecord(reservation.ID) {
		return fmt.Errorf("AI usage ID already settled")
	}
	if previous, found := s.reservations[reservation.ID]; found {
		if !reflect.DeepEqual(previous, reservation) {
			return fmt.Errorf("AI reservation ID already exists")
		}
		return nil
	}
	for index, check := range checks {
		var spent int64
		for _, record := range s.records {
			if usageMatches(record, check.Filter) {
				spent += record.CostMicros
			}
		}
		for _, held := range s.reservations {
			// Outstanding spend survives month rollover until reconciled.
			if (check.Filter.Site == "" || held.Site == check.Filter.Site) &&
				(check.Filter.Caller == "" || held.Caller == check.Filter.Caller) {
				if spent >= check.LimitMicros || held.CostMicros >= check.LimitMicros-spent {
					return &hex.AIBudgetExceededError{Index: index}
				}
				spent += held.CostMicros
			}
		}
		if spent >= check.LimitMicros {
			return &hex.AIBudgetExceededError{Index: index}
		}
	}
	s.reservations[reservation.ID] = reservation.Clone()
	return nil
}

func (s *AIUsageStore) SettleAIUsage(ctx context.Context, record hex.AIUsageRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasRecord(record.ID) {
		return nil
	}
	held, found := s.reservations[record.ID]
	if !found {
		return hex.ErrNotFound
	}
	if record.Site != held.Site || record.Caller != held.Caller || !record.At.Equal(held.At) || record.CostMicros < 0 ||
		(held.Model != "" && record.Model != held.Model) {
		return fmt.Errorf("AI settlement does not match reservation: %w", hex.ErrNotFound)
	}
	s.records = append(s.records, record)
	delete(s.reservations, record.ID)
	return nil
}

func (s *AIUsageStore) ListAIReservations(ctx context.Context, filter hex.AIUsageFilter) ([]hex.AIUsageReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]hex.AIUsageReservation, 0)
	for _, held := range s.reservations {
		if usageMatches(hex.AIUsageRecord{At: held.At, Site: held.Site, Caller: held.Caller}, filter) {
			result = append(result, held.Clone())
		}
	}
	slices.SortFunc(result, func(a, b hex.AIUsageReservation) int {
		return cmp.Or(a.At.Compare(b.At), strings.Compare(a.ID, b.ID))
	})
	return result, nil
}

func (s *AIUsageStore) AIBudgetSpend(ctx context.Context, filter hex.AIUsageFilter) (hex.AIUsageSpend, error) {
	if err := ctx.Err(); err != nil {
		return hex.AIUsageSpend{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var spend hex.AIUsageSpend
	for _, record := range s.records {
		if usageMatches(record, filter) {
			spend.SettledMicros += record.CostMicros
		}
	}
	for _, held := range s.reservations {
		if (filter.Site == "" || held.Site == filter.Site) && (filter.Caller == "" || held.Caller == filter.Caller) {
			spend.ReservedMicros += held.CostMicros
			spend.Reservations++
		}
	}
	return spend, nil
}

func (s *AIUsageStore) RecordAIUsage(ctx context.Context, record hex.AIUsageRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.ID == "" || record.CostMicros < 0 {
		return fmt.Errorf("invalid AI usage record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasRecord(record.ID) {
		return nil
	}
	if _, held := s.reservations[record.ID]; held {
		return fmt.Errorf("use SettleAIUsage for reserved calls")
	}
	s.records = append(s.records, record)
	return nil
}

func usageMatches(record hex.AIUsageRecord, filter hex.AIUsageFilter) bool {
	switch {
	case !filter.Since.IsZero() && record.At.Before(filter.Since):
		return false
	case !filter.Until.IsZero() && !record.At.Before(filter.Until):
		return false
	case filter.Site != "" && record.Site != filter.Site:
		return false
	case filter.Caller != "" && record.Caller != filter.Caller:
		return false
	}
	return true
}

func (s *AIUsageStore) SumAICost(ctx context.Context, filter hex.AIUsageFilter) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, record := range s.records {
		if usageMatches(record, filter) {
			total += record.CostMicros
		}
	}
	return total, nil
}

func usageKey(record hex.AIUsageRecord, groupBy string) (string, error) {
	switch groupBy {
	case hex.GroupBySite:
		return record.Site, nil
	case hex.GroupByCaller:
		return record.Caller, nil
	case hex.GroupByModel:
		return record.Model, nil
	case hex.GroupByDay:
		return record.At.UTC().Format("2006-01-02"), nil
	default:
		return "", fmt.Errorf("unknown AI usage grouping %q", groupBy)
	}
}

func (s *AIUsageStore) AIUsageTotals(ctx context.Context, filter hex.AIUsageFilter, groupBy string) ([]hex.AIUsageTotal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := make(map[string]*hex.AIUsageTotal)
	latest := make(map[string]hex.AIUsageRecord)
	for _, record := range s.records {
		if !usageMatches(record, filter) {
			continue
		}
		key, err := usageKey(record, groupBy)
		if err != nil {
			return nil, err
		}
		total, found := groups[key]
		if !found {
			total = &hex.AIUsageTotal{Key: key}
			groups[key] = total
		}
		if groupBy == hex.GroupByCaller {
			previous, found := latest[key]
			if !found || record.At.After(previous.At) || (record.At.Equal(previous.At) && record.ID > previous.ID) {
				latest[key] = record
				total.Name = record.CallerName
			}
		}
		total.Calls++
		total.InputTokens += int64(record.Usage.InputTokens)
		total.CachedInputTokens += int64(record.Usage.CachedInputTokens)
		total.CacheWriteTokens += int64(record.Usage.CacheWriteTokens)
		total.OutputTokens += int64(record.Usage.OutputTokens)
		total.CostMicros += record.CostMicros
		if record.Usage.Estimated {
			total.Estimated++
		}
	}

	totals := make([]hex.AIUsageTotal, 0, len(groups))
	for _, total := range groups {
		totals = append(totals, *total)
	}
	slices.SortFunc(totals, func(a, b hex.AIUsageTotal) int {
		if groupBy == hex.GroupByDay {
			return strings.Compare(a.Key, b.Key)
		}
		return cmp.Or(cmp.Compare(b.CostMicros, a.CostMicros), strings.Compare(a.Key, b.Key))
	})
	return totals, nil
}

func budgetKey(scope, subject string) string {
	return scope + "\x00" + subject
}

func (s *AIUsageStore) ListAIBudgets(ctx context.Context) ([]hex.AIBudget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	budgets := make([]hex.AIBudget, 0, len(s.budgets))
	for _, budget := range s.budgets {
		budget.Models = slices.Clone(budget.Models)
		budgets = append(budgets, budget)
	}
	slices.SortFunc(budgets, func(a, b hex.AIBudget) int {
		return strings.Compare(budgetKey(a.Scope, a.Subject), budgetKey(b.Scope, b.Subject))
	})
	return budgets, nil
}

func (s *AIUsageStore) PutAIBudget(ctx context.Context, budget hex.AIBudget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	budget.Models = slices.Clone(budget.Models)
	s.budgets[budgetKey(budget.Scope, budget.Subject)] = budget
	return nil
}

func (s *AIUsageStore) DeleteAIBudget(ctx context.Context, scope, subject string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := budgetKey(scope, subject)
	if _, found := s.budgets[key]; !found {
		return hex.ErrNotFound
	}
	delete(s.budgets, key)
	return nil
}
