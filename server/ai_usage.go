package hex

import (
	"context"
	"errors"
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
	// Overrides distinguish an explicitly free cache category from the
	// historical zero-value defaults without changing the float fields.
	CachedInputOverride *float64 `json:"cachedInputOverride,omitempty"`
	CacheWriteOverride  *float64 `json:"cacheWriteOverride,omitempty"`
}

func (p AIPrice) cachePrices() (float64, float64) {
	cached, write := p.CachedInput, p.CacheWrite
	if cached == 0 {
		cached = p.Input / 10
	}
	if write == 0 {
		write = p.Input * 1.25
	}
	if p.CachedInputOverride != nil {
		cached = *p.CachedInputOverride
	}
	if p.CacheWriteOverride != nil {
		write = *p.CacheWriteOverride
	}
	return cached, write
}

func (p AIPrice) valid() bool {
	cached, write := p.cachePrices()
	for _, rate := range []float64{p.Input, cached, write, p.Output} {
		if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			return false
		}
	}
	return true
}

// costMicros prices usage. Prices are per million tokens, so tokens times
// price is the cost in micro-dollars.
func (p AIPrice) costMicros(usage AIUsage) int64 {
	cached, write := p.cachePrices()
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
//   - site: one site (Subject); a nil Limit uses the default, Disabled
//     turns AI off for the site and Models lists the restricted models
//     enabled for it
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
	Models      []string  `json:"models,omitempty"`
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
// latest name by (At, ID), with the lexically greatest ID breaking time ties.
type AIUsageStore interface {
	// ReserveAIUsage atomically checks settled monthly spend plus ALL
	// outstanding reservations against each check, then inserts the hold.
	// Admission is allowed only below every limit. The final admitted call
	// may cross a limit by at most its conservative CostMicros ceiling.
	// Reservations never expire, including across month boundaries.
	ReserveAIUsage(ctx context.Context, reservation AIUsageReservation, checks []AIBudgetCheck) error
	// SettleAIUsage atomically inserts the record and removes its reservation.
	// Retrying with the original record ID is idempotent. A failed transaction
	// retains the hold; an ambiguous commit leaves either the hold or the
	// durable record, never neither. Attribution must match the reservation.
	SettleAIUsage(ctx context.Context, record AIUsageRecord) error
	// ListAIReservations returns pending holds ordered by (At, ID). Date
	// filters apply to listing, but never age holds out of admission.
	ListAIReservations(ctx context.Context, filter AIUsageFilter) ([]AIUsageReservation, error)
	// AIBudgetSpend atomically reads settled spend in the filter's period plus
	// all outstanding holds for its Site/Caller, including older months.
	AIBudgetSpend(ctx context.Context, filter AIUsageFilter) (AIUsageSpend, error)
	// RecordAIUsage is an idempotent import of an already settled call by ID.
	RecordAIUsage(ctx context.Context, record AIUsageRecord) error
	SumAICost(ctx context.Context, filter AIUsageFilter) (int64, error)
	AIUsageTotals(ctx context.Context, filter AIUsageFilter, groupBy string) ([]AIUsageTotal, error)
	ListAIBudgets(ctx context.Context) ([]AIBudget, error)
	PutAIBudget(ctx context.Context, budget AIBudget) error
	DeleteAIBudget(ctx context.Context, scope, subject string) error
}

// AIUsageReservation holds a conservative call ceiling under the future usage
// record's ID, timestamp and attribution. For budget-enforced calls CostMicros
// must cover all possible billed token categories for the model call.
type AIUsageReservation struct {
	ID              string    `json:"id"`
	At              time.Time `json:"at"`
	Site            string    `json:"site"`
	Caller          string    `json:"caller"`
	CostMicros      int64     `json:"costMicros"`
	CallerName      string    `json:"callerName,omitempty"`
	Model           string    `json:"model,omitempty"`
	Price           *AIPrice  `json:"price,omitempty"`
	ContextTokens   int       `json:"contextTokens,omitempty"`
	MaxOutputTokens int       `json:"maxOutputTokens,omitempty"`
	// This is the pre-call input estimate, not recovered upstream counts.
	EstimatedUsage AIUsage `json:"estimatedUsage"`
}

// Clone keeps pricing snapshots independent of callers' mutable model catalogs.
func (r AIUsageReservation) Clone() AIUsageReservation {
	if r.Price != nil {
		price := *r.Price
		if price.CachedInputOverride != nil {
			value := *price.CachedInputOverride
			price.CachedInputOverride = &value
		}
		if price.CacheWriteOverride != nil {
			value := *price.CacheWriteOverride
			price.CacheWriteOverride = &value
		}
		r.Price = &price
	}
	return r
}

type AIUsageSpend struct {
	SettledMicros  int64 `json:"settledMicros"`
	ReservedMicros int64 `json:"reservedMicros"`
	Reservations   int64 `json:"reservations"`
}

// AIBudgetCheck supplies an effective monthly limit and its settled-usage
// filter. Outstanding holds match Site/Caller but deliberately ignore dates.
type AIBudgetCheck struct {
	Filter      AIUsageFilter
	LimitMicros int64
}

// AIBudgetExceededError identifies the first denied check, in input order.
type AIBudgetExceededError struct{ Index int }

func (e *AIBudgetExceededError) Error() string { return "AI budget used up" }

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

// siteRestrictedModels lists the restricted models enabled for a site. They
// are stored with its budget, so without Config.AIUsage none are enabled.
func (s *Server) siteRestrictedModels(ctx context.Context, site string) ([]string, error) {
	if !s.aiAccountingEnabled() {
		return nil, nil
	}
	budgets, err := s.loadAIBudgets(ctx)
	if err != nil {
		return nil, err
	}
	return budgets.sites[site].Models, nil
}

type budgetCheck struct {
	limit  *int64
	filter AIUsageFilter
	whose  string
}

func (s *Server) aiBudgetChecks(ctx context.Context, caller integrationCaller, at time.Time) ([]budgetCheck, error) {
	if !s.aiAccountingEnabled() {
		return nil, nil
	}
	budgets, err := s.loadAIBudgets(ctx)
	if err != nil {
		return nil, err
	}
	if budgets.sites[caller.site].Disabled {
		return nil, &AIError{Status: http.StatusForbidden, Message: "AI is turned off for this site"}
	}

	since := monthStart(at)
	siteLimit, _ := budgets.siteLimit(caller.site)
	checks := []budgetCheck{
		{budgets.platform, AIUsageFilter{Since: since}, "the platform's"},
		{siteLimit, AIUsageFilter{Since: since, Site: caller.site}, "this site's"},
	}
	if !caller.isAutomation() && caller.identity != nil {
		checks = append(checks, budgetCheck{budgets.personLimit(caller.identity), AIUsageFilter{Since: since, Caller: caller.usageKey()}, "your"})
	}
	return checks, nil
}

// checkAIBudget provides an early rejection for settled spending. The
// authoritative admission, including in-flight spending, is ReserveAIUsage.
func (s *Server) checkAIBudget(ctx context.Context, caller integrationCaller) error {
	checks, err := s.aiBudgetChecks(ctx, caller, time.Now())
	if err != nil {
		return err
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
			return budgetExceeded(check)
		}
	}
	return nil
}

func budgetExceeded(check budgetCheck) error {
	return &AIError{Status: http.StatusTooManyRequests,
		Message: fmt.Sprintf("%s AI budget of %s for %s is used up; a platform admin can raise it",
			capitalize(check.whose), FormatDollars(*check.limit), check.filter.Since.Format("January"))}
}

func capitalize(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

type aiCallAccounting struct {
	reservation AIUsageReservation
	record      AIUsageRecord
	price       *AIPrice
	enforcing   bool
}

// reserveAICall snapshots pricing before spending and obtains a durable hold.
func (s *Server) reserveAICall(ctx context.Context, caller integrationCaller, request AIRequest) (*aiCallAccounting, error) {
	if !s.aiAccountingEnabled() {
		return nil, nil
	}
	// PostgreSQL timestamps have microsecond precision; use the same temporal
	// ordering and settlement identity in both stores.
	at := time.Now().UTC().Truncate(time.Microsecond)
	checks, err := s.aiBudgetChecks(ctx, caller, at)
	if err != nil {
		return nil, err
	}
	var admission []AIBudgetCheck
	var limited []budgetCheck
	for _, check := range checks {
		if check.limit != nil {
			admission = append(admission, AIBudgetCheck{Filter: check.filter, LimitMicros: *check.limit})
			limited = append(limited, check)
		}
	}
	models, err := s.config.AI.Provider.Models(ctx)
	if err != nil {
		return nil, err
	}
	var model AIModel
	for _, candidate := range models {
		if candidate.ID == request.Model {
			model = candidate
			break
		}
	}
	price := model.Price
	if price != nil && !price.valid() {
		return nil, &AIError{Status: http.StatusServiceUnavailable, Message: "this model has invalid AI pricing"}
	}
	if price != nil {
		cached, write := price.cachePrices()
		snapshot := *price
		snapshot.CachedInputOverride = &cached
		snapshot.CacheWriteOverride = &write
		price = &snapshot
	}
	enforcing := len(admission) > 0
	if enforcing && (price == nil || model.ContextTokens <= 0) {
		return nil, &AIError{Status: http.StatusServiceUnavailable,
			Message: "this model needs valid pricing and contextTokens to enforce AI budgets"}
	}
	var ceiling int64
	if price != nil && model.ContextTokens > 0 {
		cached, write := price.cachePrices()
		// Any input token may be uncached, read or written. Charge every
		// context token at the highest rate, plus the full output allowance.
		cost := float64(model.ContextTokens)*max(price.Input, cached, write) + float64(request.MaxTokens)*price.Output
		if math.IsInf(cost, 0) || cost >= float64(math.MaxInt64) {
			return nil, fmt.Errorf("model %s reservation cost is too large", request.Model)
		}
		ceiling = int64(math.Ceil(cost))
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	call := &aiCallAccounting{
		reservation: AIUsageReservation{ID: id, At: at, Site: caller.site, Caller: caller.usageKey(), CostMicros: ceiling,
			CallerName: caller.label(), Model: request.Model, Price: price, ContextTokens: model.ContextTokens,
			MaxOutputTokens: request.MaxTokens, EstimatedUsage: estimateUsage(request, 0)},
		record: AIUsageRecord{ID: id, At: at, Site: caller.site, Caller: caller.usageKey(), CallerName: caller.label(), Model: request.Model},
		price:  price, enforcing: enforcing,
	}
	if err := s.config.AIUsage.ReserveAIUsage(ctx, call.reservation, admission); err != nil {
		var exceeded *AIBudgetExceededError
		if errors.As(err, &exceeded) && exceeded.Index >= 0 && exceeded.Index < len(limited) {
			return nil, budgetExceeded(limited[exceeded.Index])
		}
		return nil, fmt.Errorf("reserve AI usage %s: %w", id, err)
	}
	return call, nil
}

func (s *Server) settleAICall(call *aiCallAccounting, usage AIUsage) error {
	if call == nil {
		return nil
	}
	if usage.InputTokens < 0 || usage.CachedInputTokens < 0 || usage.CacheWriteTokens < 0 || usage.OutputTokens < 0 {
		return fmt.Errorf("provider reported negative AI usage; reservation %s retained", call.record.ID)
	}
	record := call.record
	record.Usage = usage
	if call.price != nil {
		record.CostMicros = call.price.costMicros(usage)
		record.Priced = true
		// Missing upstream usage cannot release potentially spent funds.
		if usage.Estimated && call.enforcing {
			record.CostMicros = call.reservation.CostMicros
		}
	} else {
		slog.Warn("AI model has no price; spending is unpriced", "model", record.Model)
	}
	// Independent of the caller's cancellation, and the exact same record
	// on every attempt: a lost commit acknowledgement cannot double-charge.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = s.config.AIUsage.SettleAIUsage(ctx, record)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	slog.Error("settle AI usage; reservation retained", "id", record.ID, "site", record.Site, "error", err)
	return fmt.Errorf("settle AI usage %s: %w", record.ID, err)
}
