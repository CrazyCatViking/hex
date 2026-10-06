package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The portal's AI pages show what AI costs and let platform admins set the
// monthly budgets: each site's AI tab shows its spending by day, person and
// model against its limit, and the admin AI page shows the whole platform,
// the defaults and the overrides for people.

type aiMonth struct {
	Label    string
	Param    string
	Previous string
	Next     string
	Since    time.Time
	Until    time.Time
}

// selectedMonth reads ?month=YYYY-MM, defaulting to the current UTC month.
func selectedMonth(value string, now time.Time) (aiMonth, error) {
	start := monthStart(now)
	if value != "" {
		parsed, err := time.Parse("2006-01", value)
		if err != nil {
			return aiMonth{}, errors.New("month must look like 2026-10")
		}
		start = parsed.UTC()
	}
	month := aiMonth{
		Label: start.Format("January 2006"), Param: start.Format("2006-01"),
		Previous: start.AddDate(0, -1, 0).Format("2006-01"),
		Since:    start, Until: start.AddDate(0, 1, 0),
	}
	if month.Until.Before(now) {
		month.Next = month.Until.Format("2006-01")
	}
	return month, nil
}

type monthNav struct {
	Month aiMonth
	Query string
}

type spendTable struct {
	Heading string
	Rows    []spendRow
}

// spendMeter is spending against a limit.
type spendMeter struct {
	Spent   string
	Limit   string
	Percent int
	Tone    string
	Status  string
	Limited bool
}

func newSpendMeter(spent int64, limit *int64) spendMeter {
	meter := spendMeter{Spent: FormatDollars(spent), Tone: "ok", Limit: "No limit"}
	if limit == nil {
		return meter
	}
	meter.Limited = true
	meter.Limit = FormatDollars(*limit)
	share := 100.0
	if *limit > 0 {
		share = float64(spent) * 100 / float64(*limit)
	}
	meter.Percent = int(math.Min(100, math.Round(share)))
	switch {
	case share >= 100:
		meter.Tone, meter.Status = "danger", "Budget used up"
	case share >= 80:
		meter.Tone, meter.Status = "warning", fmt.Sprintf("%d%% used", int(share))
	default:
		meter.Status = fmt.Sprintf("%d%% used", int(share))
	}
	return meter
}

func newAIBudgetMeter(spend AIUsageSpend, limit *int64) spendMeter {
	meter := newSpendMeter(spend.SettledMicros+spend.ReservedMicros, limit)
	if spend.Reservations > 0 {
		meter.Spent = FormatDollars(spend.SettledMicros) + " + " + FormatDollars(spend.ReservedMicros) + " reserved"
		pending := fmt.Sprintf("%d pending calls", spend.Reservations)
		if spend.Reservations == 1 {
			pending = "1 pending call"
		}
		if meter.Status == "" {
			meter.Status = pending
			// Unlimited meters do not render a separate status line.
			meter.Spent += " (" + pending + ")"
		} else {
			meter.Status += " (" + pending + ")"
		}
	}
	return meter
}

func (s *Server) aiMonthSpend(ctx context.Context, month aiMonth, site string, now time.Time) (AIUsageSpend, error) {
	spend, err := s.config.AIUsage.AIBudgetSpend(ctx, AIUsageFilter{Since: month.Since, Until: month.Until, Site: site})
	if month.Param != now.UTC().Format("2006-01") {
		// Holds constrain current admission, not historical spending reports.
		spend.ReservedMicros, spend.Reservations = 0, 0
	}
	return spend, err
}

// spendRow is one line of a breakdown, with its share of the largest line.
type spendRow struct {
	Key       string
	Label     string
	Detail    string
	Cost      string
	Calls     string
	Tokens    string
	Percent   int
	Estimated bool
	Link      string
}

func spendRows(totals []AIUsageTotal, label func(AIUsageTotal) (string, string)) []spendRow {
	var largest int64
	for _, total := range totals {
		largest = max(largest, total.CostMicros)
	}
	rows := make([]spendRow, 0, len(totals))
	for _, total := range totals {
		name, detail := label(total)
		row := spendRow{
			Key: total.Key, Label: name, Detail: detail, Cost: FormatDollars(total.CostMicros),
			Calls:     formatCount(total.Calls),
			Tokens:    formatCount(total.InputTokens + total.CachedInputTokens + total.CacheWriteTokens + total.OutputTokens),
			Estimated: total.Estimated > 0,
		}
		if largest > 0 {
			row.Percent = int(math.Round(float64(total.CostMicros) * 100 / float64(largest)))
		}
		rows = append(rows, row)
	}
	return rows
}

func callerLabel(total AIUsageTotal) (string, string) {
	if automation, isAutomation := strings.CutPrefix(total.Key, "automation:"); isAutomation {
		_, name, _ := strings.Cut(automation, "/")
		return name, "Automation"
	}
	if total.Name != "" {
		return total.Name, ""
	}
	return strings.TrimPrefix(total.Key, "user:"), ""
}

func plainLabel(total AIUsageTotal) (string, string) {
	return total.Key, ""
}

// newSpendChart reuses the analytics trend chart: one bar per day of the
// month so far, in dollars, without the visitors line.
func newSpendChart(month aiMonth, days []AIUsageTotal, now time.Time) trendChart {
	costs := make(map[string]int64, len(days))
	for _, day := range days {
		costs[day.Key] = day.CostMicros
	}
	end := month.Until
	if end.After(now) {
		end = now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	}
	var series []AnalyticsDay
	var peak, total int64
	for day := month.Since; day.Before(end); day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		series = append(series, AnalyticsDay{Date: date, TrafficTotals: TrafficTotals{PageViews: costs[date]}})
		peak = max(peak, costs[date])
		total += costs[date]
	}

	chart := newTrendChart(series)
	chart.VisitorLine = ""
	scale := niceCeiling(peak)
	for tick := range chart.YLabels {
		chart.YLabels[tick] = FormatDollars(scale * int64(chartTicks-tick) / chartTicks)
	}
	for index := range chart.Days {
		cost := FormatDollars(series[index].PageViews)
		chart.Days[index].Views = cost
		chart.Days[index].Tooltip = chart.Days[index].Label + "|" + cost
	}
	chart.Summary = fmt.Sprintf("Daily AI spending in %s, %s in total.", month.Label, FormatDollars(total))
	return chart
}

// parseDollars reads a limit typed in the portal; blank means none.
func parseDollars(value string) (*int64, error) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "$"))
	if value == "" {
		return nil, nil
	}
	dollars, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64)
	if err != nil || dollars < 0 || dollars > 1_000_000 || math.IsNaN(dollars) {
		return nil, errors.New("enter an amount in dollars, such as 25 or 12.50")
	}
	micros := int64(math.Round(dollars * 1e6))
	return &micros, nil
}

func limitInput(limit *int64) string {
	if limit == nil {
		return ""
	}
	return strconv.FormatFloat(float64(*limit)/1e6, 'f', -1, 64)
}

// Site AI tab.

type siteAIView struct {
	Site       string
	Month      aiMonth
	Meter      spendMeter
	LimitFrom  string
	OwnLimit   string
	Disabled   bool
	Admin      bool
	MonthNav   monthNav
	Chart      trendChart
	People     spendTable
	Models     spendTable
	Calls      string
	Estimated  bool
	Message    string
	ErrorText  string
	TabLink    string
	PlatformAI bool
	Restricted []siteAIModel
}

// siteAIModel is a restricted model and whether it is enabled for the site.
type siteAIModel struct {
	ID      string
	Name    string
	Enabled bool
}

// restrictedModels lists the provider's restricted models.
func (s *Server) restrictedModels(ctx context.Context) ([]AIModel, error) {
	models, err := s.config.AI.Provider.Models(ctx)
	if err != nil {
		return nil, fmt.Errorf("list AI models: %w", err)
	}
	var restricted []AIModel
	for _, model := range models {
		if model.Restricted {
			restricted = append(restricted, model)
		}
	}
	return restricted, nil
}

func (s *Server) loadSiteAI(ctx context.Context, site string, identity *Identity, values url.Values) (siteAIView, error) {
	now := time.Now()
	month, err := selectedMonth(values.Get("month"), now)
	if err != nil {
		return siteAIView{}, err
	}
	budgets, err := s.loadAIBudgets(ctx)
	if err != nil {
		return siteAIView{}, err
	}
	filter := AIUsageFilter{Since: month.Since, Until: month.Until, Site: site}
	spend, err := s.aiMonthSpend(ctx, month, site, now)
	if err != nil {
		return siteAIView{}, err
	}
	limit, own := budgets.siteLimit(site)
	view := siteAIView{
		Site: site, Month: month, Meter: newAIBudgetMeter(spend, limit),
		Disabled: budgets.sites[site].Disabled, Admin: s.isAdmin(identity),
		TabLink: "/manage/" + site + "?tab=ai",
	}
	restricted, err := s.restrictedModels(ctx)
	if err != nil {
		slogAIError("list restricted AI models for spending page", err)
		view.ErrorText = "Model availability could not be loaded; spending and budgets are still available."
		for _, id := range budgets.sites[site].Models {
			view.Restricted = append(view.Restricted, siteAIModel{ID: id, Name: id, Enabled: true})
		}
	}
	for _, model := range restricted {
		view.Restricted = append(view.Restricted, siteAIModel{
			ID: model.ID, Name: model.Name, Enabled: slices.Contains(budgets.sites[site].Models, model.ID),
		})
	}
	switch {
	case own:
		view.LimitFrom, view.OwnLimit = "This site's own limit", limitInput(limit)
	case limit != nil:
		view.LimitFrom = "The default for every site"
	default:
		view.LimitFrom = "No limit is set for sites"
	}

	people, err := s.config.AIUsage.AIUsageTotals(ctx, filter, GroupByCaller)
	if err != nil {
		return siteAIView{}, err
	}
	models, err := s.config.AIUsage.AIUsageTotals(ctx, filter, GroupByModel)
	if err != nil {
		return siteAIView{}, err
	}
	days, err := s.config.AIUsage.AIUsageTotals(ctx, filter, GroupByDay)
	if err != nil {
		return siteAIView{}, err
	}
	view.People = spendTable{Heading: "Who", Rows: spendRows(people, callerLabel)}
	view.Models = spendTable{Heading: "Model", Rows: spendRows(models, plainLabel)}
	view.MonthNav = monthNav{Month: month, Query: "tab=ai&"}
	view.Chart = newSpendChart(month, days, now)
	var calls int64
	for _, model := range models {
		calls += model.Calls
		view.Estimated = view.Estimated || model.Estimated > 0
	}
	view.Calls = formatCount(calls)
	return view, nil
}

// manageSiteAIBudget saves a site's limit, whether AI is on for it and the
// restricted models enabled for it.
func (s *Server) manageSiteAIBudget(w http.ResponseWriter, r *http.Request) {
	identity, site, _, _, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	if !s.isAdmin(identity) {
		writeError(w, http.StatusForbidden, "only platform admins change AI budgets")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	var enabled []string
	for field, values := range r.PostForm {
		if id, isModel := strings.CutPrefix(field, "model."); isModel && slices.Contains(values, "on") {
			enabled = append(enabled, id)
		}
	}
	if len(enabled) > 0 {
		restricted, err := s.restrictedModels(r.Context())
		if err != nil {
			if r.PostFormValue("enabled") == "on" {
				writeServerError(w, err)
				return
			}
			// Turning AI off must work during a provider outage. Keep the
			// previously validated model selection rather than enabling new IDs.
			budgets, loadErr := s.loadAIBudgets(r.Context())
			if loadErr != nil {
				writeServerError(w, loadErr)
				return
			}
			enabled = budgets.sites[site].Models
		} else {
			var validated []string
			for _, model := range restricted {
				if slices.Contains(enabled, model.ID) {
					validated = append(validated, model.ID)
				}
			}
			enabled = validated
		}
	}
	limit, parseErr := parseDollars(r.PostFormValue("limit"))
	if parseErr == nil {
		budget := AIBudget{
			Scope: BudgetSite, Subject: site, LimitMicros: limit,
			Disabled: r.PostFormValue("enabled") != "on", Models: enabled,
			UpdatedBy: personOf(identity), UpdatedAt: time.Now().UTC(),
		}
		var err error
		if budget.LimitMicros == nil && !budget.Disabled && len(budget.Models) == 0 {
			err = s.config.AIUsage.DeleteAIBudget(r.Context(), BudgetSite, site)
			if errors.Is(err, ErrNotFound) {
				err = nil
			}
		} else {
			err = s.config.AIUsage.PutAIBudget(r.Context(), budget)
		}
		if err != nil {
			writeServerError(w, err)
			return
		}
		slog.Info("AI budget changed", "site", site, "limit", limitInput(limit), "disabled", budget.Disabled, "models", budget.Models, "by", identityName(identity))
	}

	view, err := s.loadSiteAI(r.Context(), site, identity, r.URL.Query())
	if err != nil {
		writeServerError(w, err)
		return
	}
	if parseErr != nil {
		view.ErrorText = parseErr.Error()
	} else {
		view.Message = "Saved"
	}
	s.renderFragment(w, "site-ai-budget", view)
}

// Admin AI page.

type adminAIView struct {
	Chrome
	Month          aiMonth
	Meter          spendMeter
	Platform       string
	SiteDefault    string
	PersonDefault  string
	FromConfig     string
	Sites          []adminAISite
	Overrides      []aiOverride
	Models         spendTable
	People         spendTable
	MonthNav       monthNav
	Chart          trendChart
	Analytics      bool
	Audit          bool
	Message        string
	ErrorText      string
	OverrideError  string
	OverrideNotice string
}

type adminAISite struct {
	Name     string
	Meter    spendMeter
	Own      bool
	Disabled bool
	Models   []string
	Link     string
}

type aiOverride struct {
	Subject   string
	Label     string
	Limit     string
	RemoveURL string
}

func (s *Server) registerAIPortalRoutes() {
	if !s.aiAccountingEnabled() || s.config.Identity == nil {
		return
	}
	s.mux.HandleFunc("GET /admin/ai", s.adminAIPage)
	s.mux.HandleFunc("PUT /api/hex/manage/ai/budgets", s.manageAIDefaults)
	s.mux.HandleFunc("POST /api/hex/manage/ai/overrides", s.manageAddAIOverride)
	s.mux.HandleFunc("DELETE /api/hex/manage/ai/overrides", s.manageRemoveAIOverride)
	s.mux.HandleFunc("GET /api/hex/manage/ai/reservations", s.manageAIReservations)
	s.mux.HandleFunc("PUT /api/hex/manage/ai/settlements/{id}", s.manageAISettlement)
	if s.manageEnabled() {
		s.mux.HandleFunc("PUT /api/hex/manage/sites/{site}/ai/budget", s.manageSiteAIBudget)
	}
}

func (s *Server) manageAIReservations(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminCaller(w, r); !ok {
		return
	}
	filter := AIUsageFilter{Site: r.URL.Query().Get("site"), Caller: r.URL.Query().Get("caller")}
	held, err := s.config.AIUsage.ListAIReservations(r.Context(), filter)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, held)
}

// manageAISettlement accepts a complete verified record so retries work even
// after its hold has been replaced, without depending on a live model catalog.
func (s *Server) manageAISettlement(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	var record AIUsageRecord
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		writeError(w, http.StatusBadRequest, "expected an AI usage record: "+err.Error())
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "expected one AI usage record")
		return
	}
	usage := record.Usage
	if record.ID != r.PathValue("id") || record.ID == "" || record.At.IsZero() || record.CostMicros < 0 ||
		usage.InputTokens < 0 || usage.CachedInputTokens < 0 || usage.CacheWriteTokens < 0 || usage.OutputTokens < 0 ||
		(!record.Priced && record.CostMicros != 0) {
		writeError(w, http.StatusBadRequest, "invalid AI settlement identity, usage or cost")
		return
	}
	if err := s.config.AIUsage.SettleAIUsage(r.Context(), record); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "no matching AI reservation")
		} else {
			writeServerError(w, err)
		}
		return
	}
	slog.Info("AI reservation reconciled", "id", record.ID, "site", record.Site, "costMicros", record.CostMicros, "by", identityName(identity))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) loadAdminAI(ctx context.Context, identity *Identity, values url.Values) (adminAIView, error) {
	now := time.Now()
	month, err := selectedMonth(values.Get("month"), now)
	if err != nil {
		return adminAIView{}, err
	}
	budgets, err := s.loadAIBudgets(ctx)
	if err != nil {
		return adminAIView{}, err
	}
	filter := AIUsageFilter{Since: month.Since, Until: month.Until}
	spend, err := s.aiMonthSpend(ctx, month, "", now)
	if err != nil {
		return adminAIView{}, err
	}
	view := adminAIView{
		Chrome: s.chromeFor(identity, "admin"), Month: month, Meter: newAIBudgetMeter(spend, budgets.platform),
		Platform: limitInput(budgets.platform), SiteDefault: limitInput(budgets.siteDefault),
		PersonDefault: limitInput(budgets.personDefault), Analytics: s.config.Analytics != nil,
		Audit: s.integrationAuditEnabled(),
	}
	fromConfig, err := s.configuredDefaults(ctx)
	if err != nil {
		return adminAIView{}, err
	}
	view.FromConfig = sentenceList(fromConfig)

	sites, err := s.config.AIUsage.AIUsageTotals(ctx, filter, GroupBySite)
	if err != nil {
		return adminAIView{}, err
	}
	listed := make(map[string]bool)
	for _, total := range sites {
		spend, err := s.aiMonthSpend(ctx, month, total.Key, now)
		if err != nil {
			return adminAIView{}, err
		}
		site := s.adminAISite(total.Key, spend, budgets)
		view.Sites = append(view.Sites, site)
		listed[total.Key] = true
	}
	for name := range budgets.sites {
		if !listed[name] {
			spend, err := s.aiMonthSpend(ctx, month, name, now)
			if err != nil {
				return adminAIView{}, err
			}
			view.Sites = append(view.Sites, s.adminAISite(name, spend, budgets))
			listed[name] = true
		}
	}
	if month.Param == now.UTC().Format("2006-01") {
		held, err := s.config.AIUsage.ListAIReservations(ctx, AIUsageFilter{})
		if err != nil {
			return adminAIView{}, err
		}
		for _, reservation := range held {
			if listed[reservation.Site] {
				continue
			}
			spend, err := s.aiMonthSpend(ctx, month, reservation.Site, now)
			if err != nil {
				return adminAIView{}, err
			}
			view.Sites = append(view.Sites, s.adminAISite(reservation.Site, spend, budgets))
			listed[reservation.Site] = true
		}
	}
	// Sites with spending come first, most expensive first, as returned;
	// sites that only have a budget follow by name.
	slices.SortStableFunc(view.Sites[len(sites):], func(a, b adminAISite) int { return strings.Compare(a.Name, b.Name) })
	for _, budget := range budgets.people {
		view.Overrides = append(view.Overrides, aiOverride{
			Subject: budget.Subject, Label: s.principalName(ctx, budget.Subject), Limit: FormatDollars(valueOr(budget.LimitMicros)),
			RemoveURL: "/api/hex/manage/ai/overrides?" + url.Values{"subject": {budget.Subject}, "month": {month.Param}}.Encode(),
		})
	}

	models, err := s.config.AIUsage.AIUsageTotals(ctx, filter, GroupByModel)
	if err != nil {
		return adminAIView{}, err
	}
	people, err := s.config.AIUsage.AIUsageTotals(ctx, filter, GroupByCaller)
	if err != nil {
		return adminAIView{}, err
	}
	days, err := s.config.AIUsage.AIUsageTotals(ctx, filter, GroupByDay)
	if err != nil {
		return adminAIView{}, err
	}
	view.Models = spendTable{Heading: "Model", Rows: spendRows(models, plainLabel)}
	if len(people) > 10 {
		people = people[:10]
	}
	view.People = spendTable{Heading: "Who", Rows: spendRows(people, callerLabel)}
	view.MonthNav = monthNav{Month: month}
	view.Chart = newSpendChart(month, days, now)
	return view, nil
}

func valueOr(limit *int64) int64 {
	if limit == nil {
		return 0
	}
	return *limit
}

func (s *Server) adminAISite(name string, spend AIUsageSpend, budgets aiBudgets) adminAISite {
	limit, own := budgets.siteLimit(name)
	return adminAISite{
		Name: name, Meter: newAIBudgetMeter(spend, limit), Own: own,
		Disabled: budgets.sites[name].Disabled, Models: budgets.sites[name].Models,
		Link: "/manage/" + name + "?tab=ai",
	}
}

// configuredDefaults names the limits that still come from the server's
// configuration because no admin has saved them.
func (s *Server) configuredDefaults(ctx context.Context) ([]string, error) {
	stored, err := s.config.AIUsage.ListAIBudgets(ctx)
	if err != nil {
		return nil, err
	}
	saved := make(map[string]bool)
	for _, budget := range stored {
		saved[budget.Scope] = true
	}
	var names []string
	limits := s.config.AI.Limits
	for _, entry := range []struct {
		scope, name string
		value       float64
	}{
		{BudgetPlatform, "the platform limit", limits.PlatformMonthly},
		{BudgetSiteDefault, "the site default", limits.SiteMonthly},
		{BudgetPersonDefault, "the person default", limits.PersonMonthly},
	} {
		if !saved[entry.scope] && entry.value > 0 {
			names = append(names, entry.name)
		}
	}
	return names, nil
}

// sentenceList joins names as "a, b and c", starting with a capital.
func sentenceList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return capitalize(names[0])
	default:
		return capitalize(strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1])
	}
}

// principalName labels an override's principal with a remembered person or
// configured group, falling back to the principal itself.
func (s *Server) principalName(ctx context.Context, principal string) string {
	labels := s.describePrincipals(ctx, []string{principal})
	if len(labels) == 1 && labels[0].Name != "" {
		kind, _, _ := strings.Cut(principal, ":")
		return labels[0].Name + " (" + kind + ")"
	}
	return principal
}

func (s *Server) adminAIPage(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	view, err := s.loadAdminAI(r.Context(), identity, r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writePortalPage(w, "admin-ai", view)
}

func (s *Server) manageAIDefaults(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	var limits []*int64
	var parseErr error
	for _, field := range []string{"platform", "siteDefault", "personDefault"} {
		limit, err := parseDollars(r.PostFormValue(field))
		if err != nil {
			parseErr = err
			break
		}
		limits = append(limits, limit)
	}
	if parseErr == nil {
		for index, scope := range []string{BudgetPlatform, BudgetSiteDefault, BudgetPersonDefault} {
			budget := AIBudget{Scope: scope, LimitMicros: limits[index], UpdatedBy: personOf(identity), UpdatedAt: time.Now().UTC()}
			if err := s.config.AIUsage.PutAIBudget(r.Context(), budget); err != nil {
				writeServerError(w, err)
				return
			}
		}
		slog.Info("AI budget defaults changed", "platform", limitInput(limits[0]), "site", limitInput(limits[1]),
			"person", limitInput(limits[2]), "by", identityName(identity))
	}
	view, err := s.loadAdminAI(r.Context(), identity, r.URL.Query())
	if err != nil {
		writeServerError(w, err)
		return
	}
	if parseErr != nil {
		view.ErrorText = parseErr.Error()
	} else {
		view.Message = "Saved"
	}
	s.renderFragment(w, "admin-ai-defaults", view)
}

func (s *Server) manageAddAIOverride(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	subject := strings.TrimSpace(r.PostFormValue("subject"))
	limit, parseErr := parseDollars(r.PostFormValue("limit"))
	switch {
	case !principalPattern.MatchString(subject):
		parseErr = errors.New("name who it is for as role:<app role>, group:<object id> or user:<object id>")
	case parseErr == nil && limit == nil:
		parseErr = errors.New("enter a monthly limit in dollars")
	}
	if parseErr == nil {
		budget := AIBudget{Scope: BudgetPerson, Subject: subject, LimitMicros: limit, UpdatedBy: personOf(identity), UpdatedAt: time.Now().UTC()}
		if err := s.config.AIUsage.PutAIBudget(r.Context(), budget); err != nil {
			writeServerError(w, err)
			return
		}
		slog.Info("AI budget override changed", "subject", subject, "limit", limitInput(limit), "by", identityName(identity))
	}
	s.renderOverrides(w, r, identity, parseErr)
}

func (s *Server) manageRemoveAIOverride(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.adminCaller(w, r)
	if !ok {
		return
	}
	subject := r.URL.Query().Get("subject")
	err := s.config.AIUsage.DeleteAIBudget(r.Context(), BudgetPerson, subject)
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeServerError(w, err)
		return
	}
	slog.Info("AI budget override removed", "subject", subject, "by", identityName(identity))
	s.renderOverrides(w, r, identity, nil)
}

func (s *Server) renderOverrides(w http.ResponseWriter, r *http.Request, identity *Identity, problem error) {
	view, err := s.loadAdminAI(r.Context(), identity, r.URL.Query())
	if err != nil {
		writeServerError(w, err)
		return
	}
	if problem != nil {
		view.OverrideError = problem.Error()
	}
	s.renderFragment(w, "admin-ai-overrides", view)
}
