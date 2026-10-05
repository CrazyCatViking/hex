package memory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	hex "github.com/crazycatviking/hex/server"
)

// AIUsageStore keeps AI usage records and budgets in process memory, for
// local development and tests.
type AIUsageStore struct {
	mu      sync.Mutex
	records []hex.AIUsageRecord
	budgets map[string]hex.AIBudget
}

func NewAIUsageStore() *AIUsageStore {
	return &AIUsageStore{budgets: make(map[string]hex.AIBudget)}
}

func (s *AIUsageStore) RecordAIUsage(ctx context.Context, record hex.AIUsageRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
			total.Name = record.CallerName
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
