package hex

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"
)

// AI usage accounting records every model call with its exact token counts
// and cost, attributed to the site and the person or automation it ran for.
// Monthly budgets in US dollars cap spending for the platform, each site and
// each person; they are checked before every model call. Costs are kept in
// micro-dollars so sums stay exact.

// AIPrice is what a model costs, in US dollars per million tokens. Cached
// input is what a cache read costs and CacheWrite what writing to the cache
// costs; they default to a tenth and 1.25 times the input price.
type AIPrice struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cachedInput,omitempty"`
	CacheWrite  float64 `json:"cacheWrite,omitempty"`
	Output      float64 `json:"output"`
}

// costMicros prices usage. Prices are per million tokens, so tokens times
// price is the cost in micro-dollars.
func (p AIPrice) costMicros(usage AIUsage) int64 {
	cached := p.CachedInput
	if cached == 0 {
		cached = p.Input / 10
	}
	write := p.CacheWrite
	if write == 0 {
		write = p.Input * 1.25
	}
	cost := float64(usage.InputTokens)*p.Input + float64(usage.CachedInputTokens)*cached +
		float64(usage.CacheWriteTokens)*write + float64(usage.OutputTokens)*p.Output
	return int64(math.Round(cost))
}

// AIUsageRecord is one model call. Caller is the stable key of who it ran
// for ("user:<id>" or "automation:<site>/<name>").
type AIUsageRecord struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Site       string    `json:"site"`
	Caller     string    `json:"caller"`
	CallerName string    `json:"callerName,omitempty"`
	Model      string    `json:"model"`
	Usage      AIUsage   `json:"usage"`
	CostMicros int64     `json:"costMicros"`
	Priced     bool      `json:"priced"`
}

// AIUsageFilter selects records from Since (inclusive) to Until (exclusive);
// empty fields match everything.
type AIUsageFilter struct {
	Since  time.Time
	Until  time.Time
	Site   string
	Caller string
}

// AIUsageTotal sums the records of one group.
type AIUsageTotal struct {
	Key               string `json:"key"`
	Name              string `json:"name,omitempty"`
	Calls             int64  `json:"calls"`
	InputTokens       int64  `json:"inputTokens"`
	CachedInputTokens int64  `json:"cachedInputTokens"`
	CacheWriteTokens  int64  `json:"cacheWriteTokens"`
	OutputTokens      int64  `json:"outputTokens"`
	CostMicros        int64  `json:"costMicros"`
	Estimated         int64  `json:"estimated"`
}

// Grouping for AIUsageTotals.
const (
	GroupBySite   = "site"
	GroupByCaller = "caller"
	GroupByModel  = "model"
	GroupByDay    = "day"
)

// AIBudget is one stored budget. Scopes:
//   - platform: every model call on the platform
//   - site-default: each site without its own limit
//   - site: one site (Subject); a nil Limit uses the default, and Disabled
//     turns AI off for the site
//   - person-default: each person without an override
//   - person: people matching the principal in Subject (user:, group: or
//     role:); the highest matching override applies
//
// A nil Limit means no limit, except on site budgets as described.
type AIBudget struct {
	Scope       string    `json:"scope"`
	Subject     string    `json:"subject,omitempty"`
	LimitMicros *int64    `json:"limitMicros"`
	Disabled    bool      `json:"disabled,omitempty"`
	UpdatedBy   *Person   `json:"updatedBy,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const (
	BudgetPlatform      = "platform"
	BudgetSiteDefault   = "site-default"
	BudgetSite          = "site"
	BudgetPersonDefault = "person-default"
	BudgetPerson        = "person"
)

// AIUsageStore keeps usage records and budgets. Totals are grouped by site,
// caller, model or UTC day (keyed YYYY-MM-DD); a caller group's Name is the
// latest name recorded for it.
type AIUsageStore interface {
	RecordAIUsage(ctx context.Context, record AIUsageRecord) error
	SumAICost(ctx context.Context, filter AIUsageFilter) (int64, error)
	AIUsageTotals(ctx context.Context, filter AIUsageFilter, groupBy string) ([]AIUsageTotal, error)
	ListAIBudgets(ctx context.Context) ([]AIBudget, error)
	PutAIBudget(ctx context.Context, budget AIBudget) error
	DeleteAIBudget(ctx context.Context, scope, subject string) error
}

// AILimits are the monthly limits in US dollars used where no budget is
// stored; zero means no limit.
type AILimits struct {
	PlatformMonthly float64
	SiteMonthly     float64
	PersonMonthly   float64
}

func dollarsToMicros(dollars float64) *int64 {
	if dollars <= 0 {
		return nil
	}
	micros := int64(math.Round(dollars * 1e6))
	return &micros
}

// FormatDollars renders micro-dollars as US dollars for messages and pages.
func FormatDollars(micros int64) string {
	dollars := float64(micros) / 1e6
	if dollars != 0 && math.Abs(dollars) < 0.01 {
		return fmt.Sprintf("$%.4f", dollars)
	}
	return fmt.Sprintf("$%.2f", dollars)
}

func monthStart(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func (s *Server) aiAccountingEnabled() bool {
	return s.aiEnabled() && s.config.AIUsage != nil
}

// aiBudgets is the stored budgets with the configured defaults applied.
type aiBudgets struct {
	platform      *int64
	siteDefault   *int64
	personDefault *int64
	sites         map[string]AIBudget
	people        []AIBudget
}

func (s *Server) loadAIBudgets(ctx context.Context) (aiBudgets, error) {
	limits := s.config.AI.Limits
	budgets := aiBudgets{
		platform:      dollarsToMicros(limits.PlatformMonthly),
		siteDefault:   dollarsToMicros(limits.SiteMonthly),
		personDefault: dollarsToMicros(limits.PersonMonthly),
		sites:         make(map[string]AIBudget),
	}
	stored, err := s.config.AIUsage.ListAIBudgets(ctx)
	if err != nil {
		return budgets, fmt.Errorf("load AI budgets: %w", err)
	}
	for _, budget := range stored {
		switch budget.Scope {
		case BudgetPlatform:
			budgets.platform = budget.LimitMicros
		case BudgetSiteDefault:
			budgets.siteDefault = budget.LimitMicros
		case BudgetPersonDefault:
			budgets.personDefault = budget.LimitMicros
		case BudgetSite:
			budgets.sites[budget.Subject] = budget
		case BudgetPerson:
			budgets.people = append(budgets.people, budget)
		}
	}
	return budgets, nil
}

// siteLimit is the site's own limit or the default, and whether it is the
// site's own.
func (b aiBudgets) siteLimit(site string) (*int64, bool) {
	if budget, found := b.sites[site]; found && budget.LimitMicros != nil {
		return budget.LimitMicros, true
	}
	return b.siteDefault, false
}

// personLimit is the highest override matching the person, or the default.
func (b aiBudgets) personLimit(identity *Identity) *int64 {
	var best *int64
	for _, budget := range b.people {
		if budget.LimitMicros == nil || !identity.matches(budget.Subject) {
			continue
		}
		if best == nil || *budget.LimitMicros > *best {
			best = budget.LimitMicros
		}
	}
	if best != nil {
		return best
	}
	return b.personDefault
}

type budgetCheck struct {
	limit  *int64
	filter AIUsageFilter
	whose  string
}

// checkAIBudget refuses a model call when AI is off for the site or the
// month's spending has reached the platform's, the site's or the person's
// limit. A call that starts below a limit may end slightly above it.
func (s *Server) checkAIBudget(ctx context.Context, caller integrationCaller) error {
	if !s.aiAccountingEnabled() {
		return nil
	}
	budgets, err := s.loadAIBudgets(ctx)
	if err != nil {
		return err
	}
	if budgets.sites[caller.site].Disabled {
		return &AIError{Status: http.StatusForbidden, Message: "AI is turned off for this site"}
	}

	since := monthStart(time.Now())
	siteLimit, _ := budgets.siteLimit(caller.site)
	checks := []budgetCheck{
		{budgets.platform, AIUsageFilter{Since: since}, "the platform's"},
		{siteLimit, AIUsageFilter{Since: since, Site: caller.site}, "this site's"},
	}
	if !caller.isAutomation() && caller.identity != nil {
		checks = append(checks, budgetCheck{budgets.personLimit(caller.identity), AIUsageFilter{Since: since, Caller: caller.usageKey()}, "your"})
	}

	for _, check := range checks {
		if check.limit == nil {
			continue
		}
		spent, err := s.config.AIUsage.SumAICost(ctx, check.filter)
		if err != nil {
			return fmt.Errorf("sum AI spending: %w", err)
		}
		if spent >= *check.limit {
			return &AIError{
				Status: http.StatusTooManyRequests,
				Message: fmt.Sprintf("%s AI budget of %s for %s is used up; a platform admin can raise it",
					capitalize(check.whose), FormatDollars(*check.limit), since.Format("January")),
			}
		}
	}
	return nil
}

func capitalize(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// modelPrice looks up the configured price of a model.
func (s *Server) modelPrice(ctx context.Context, id string) (*AIPrice, error) {
	models, err := s.config.AI.Provider.Models(ctx)
	if err != nil {
		return nil, err
	}
	for _, model := range models {
		if model.ID == id {
			return model.Price, nil
		}
	}
	return nil, nil
}

// recordAIUsage stores one model call. Failing to record is logged rather
// than failing a call that already happened.
func (s *Server) recordAIUsage(caller integrationCaller, model string, usage AIUsage) {
	if usage.InputTokens+usage.CachedInputTokens+usage.CacheWriteTokens+usage.OutputTokens == 0 {
		return
	}
	logAIUsage(caller, model, usage)
	if !s.aiAccountingEnabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	record := AIUsageRecord{
		At: time.Now().UTC(), Site: caller.site, Caller: caller.usageKey(),
		CallerName: caller.label(), Model: model, Usage: usage,
	}
	id, err := newID()
	if err != nil {
		slog.Error("record AI usage", "error", err)
		return
	}
	record.ID = id
	price, err := s.modelPrice(ctx, model)
	if err != nil {
		slog.Error("look up AI model price", "model", model, "error", err)
	}
	if price != nil {
		record.CostMicros = price.costMicros(usage)
		record.Priced = true
	} else {
		slog.Warn("AI model has no price, so its spending is not counted against budgets", "model", model)
	}
	if err := s.config.AIUsage.RecordAIUsage(ctx, record); err != nil {
		slog.Error("record AI usage", "site", caller.site, "model", model, "error", err)
	}
}
