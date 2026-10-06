package hex_test

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func setupAIPortal(t *testing.T) (*hex.Server, *memory.AIUsageStore) {
	return setupAIPortalProvider(t, &scriptedProvider{})
}

func setupAIPortalProvider(t *testing.T, provider hex.AIProvider) (*hex.Server, *memory.AIUsageStore) {
	t.Helper()
	sites, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sites.Close() })
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
		t.Fatal(err)
	}
	usage := memory.NewAIUsageStore()
	now := time.Now().UTC()
	for _, record := range []hex.AIUsageRecord{
		{ID: "1", At: now, Site: "demo", Caller: "user:owner", CallerName: "Olga Owner", Model: "general", CostMicros: 1_250_000, Priced: true,
			Usage: hex.AIUsage{InputTokens: 1000, CachedInputTokens: 4000, OutputTokens: 200}},
		{ID: "2", At: now, Site: "demo", Caller: "automation:demo/weekly-report", CallerName: "automation:demo/weekly-report", Model: "general", CostMicros: 500_000, Priced: true,
			Usage: hex.AIUsage{InputTokens: 300, OutputTokens: 100, Estimated: true}},
		{ID: "3", At: now, Site: "other", Caller: "user:someone", CallerName: "Some One", Model: "premium", CostMicros: 3_000_000, Priced: true},
	} {
		if err := usage.RecordAIUsage(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	server := hex.New(hex.Config{
		Sites: sites, Publisher: sites, Identity: easyauth.Resolver{}, Access: access,
		AdminGroups: []string{"admin-group"}, SiteBaseURL: "http://example.com",
		AI:      &hex.AIConfig{Provider: provider, Limits: hex.AILimits{SiteMonthly: 5}},
		AIUsage: usage,
	})
	return server, usage
}

type unavailableModelCatalog struct{ scriptedProvider }

func (*unavailableModelCatalog) Models(context.Context) ([]hex.AIModel, error) {
	return nil, errors.New("model catalog unavailable")
}

func TestAIAdministrationWithoutSitesOrAccess(t *testing.T) {
	store := memory.NewAIUsageStore()
	server := hex.New(hex.Config{
		Identity: easyauth.Resolver{}, AdminGroups: []string{"admin-group"},
		SiteBaseURL: "http://example.com",
		AI:          &hex.AIConfig{Provider: &unavailableModelCatalog{}}, AIUsage: store,
	})
	admin := principalHeaders("admin", "admin-group")
	requestAs(t, server, nil, "GET", "/admin/ai", nil, 401)
	requestAs(t, server, roleHeaders("person"), "GET", "/admin/ai", nil, 403)
	requestAs(t, server, admin, "GET", "/admin/ai", nil, 200)
	formRequest(t, server, roleHeaders("person"), "PUT", "/api/hex/manage/ai/budgets", url.Values{"platform": {"10"}}, 403)
	formRequest(t, server, admin, "PUT", "/api/hex/manage/ai/budgets", url.Values{"platform": {"10"}}, 200)
	formRequest(t, server, admin, "POST", "/api/hex/manage/ai/overrides", url.Values{"subject": {"user:person"}, "limit": {"2"}}, 200)
	formRequest(t, server, admin, "DELETE", "/api/hex/manage/ai/overrides?subject=user:person", nil, 200)
	budgets, err := store.ListAIBudgets(context.Background())
	if err != nil || len(budgets) != 3 {
		t.Fatalf("API-only budget administration failed: %+v %v", budgets, err)
	}
}

func TestAISpendingAndDisablingWorkDuringModelCatalogOutage(t *testing.T) {
	server, store := setupAIPortalProvider(t, &unavailableModelCatalog{})
	page := html.UnescapeString(requestAs(t, server, roleHeaders("owner"), "GET", "/manage/demo?tab=ai", nil, 200).Body.String())
	if !strings.Contains(page, "$1.75") {
		t.Fatalf("catalog outage hid spending: %s", page)
	}
	admin := principalHeaders("admin", "admin-group")
	formRequest(t, server, admin, "PUT", "/api/hex/manage/sites/demo/ai/budget",
		url.Values{"enabled": {"off"}, "limit": {"2"}, "model.premium": {"on"}}, 200)
	budgets, err := store.ListAIBudgets(context.Background())
	if err != nil || len(budgets) != 1 || !budgets[0].Disabled || len(budgets[0].Models) != 0 {
		t.Fatalf("outage prevented disabling or enabled unvalidated models: %+v %v", budgets, err)
	}
	// Enabling a newly selected model still requires catalog validation.
	formRequest(t, server, admin, "PUT", "/api/hex/manage/sites/demo/ai/budget",
		url.Values{"enabled": {"on"}, "model.premium": {"on"}}, 500)
}

func TestAIReservationOperatorAPIWithoutSiteProviders(t *testing.T) {
	ctx := context.Background()
	store := memory.NewAIUsageStore()
	held := hex.AIUsageReservation{
		ID: "recover-after-restart", At: time.Now().UTC().AddDate(0, -1, 0).Truncate(time.Microsecond),
		Site: "demo", Caller: "user:person", CallerName: "Person", Model: "general", CostMicros: 50,
		Price: &hex.AIPrice{Input: 1, Output: 2}, ContextTokens: 20, MaxOutputTokens: 10,
		EstimatedUsage: hex.AIUsage{InputTokens: 5, Estimated: true},
	}
	if err := store.ReserveAIUsage(ctx, held, nil); err != nil {
		t.Fatal(err)
	}
	server := hex.New(hex.Config{
		Identity: easyauth.Resolver{}, AdminGroups: []string{"admin-group"}, SiteBaseURL: "http://example.com",
		AI: &hex.AIConfig{Provider: &unavailableModelCatalog{}}, AIUsage: store,
	})
	admin := principalHeaders("admin", "admin-group")
	path := "/api/hex/manage/ai/reservations?site=demo&caller=user:person"
	requestAs(t, server, nil, "GET", path, nil, 401)
	requestAs(t, server, roleHeaders("person"), "GET", path, nil, 403)
	response := requestAs(t, server, admin, "GET", path, nil, 200)
	var pending []hex.AIUsageReservation
	if err := json.Unmarshal(response.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Model != held.Model || pending[0].Price.Input != 1 || pending[0].EstimatedUsage.InputTokens != 5 {
		t.Fatalf("reservation is not recoverable from API: %+v", pending)
	}
	page := requestAs(t, server, admin, "GET", "/admin/ai", nil, 200).Body.String()
	if !strings.Contains(page, "reserved") || !strings.Contains(page, "1 pending call") || !strings.Contains(page, "/manage/demo?tab=ai") {
		t.Fatalf("pending-only spending is invisible: %s", page)
	}
	record := hex.AIUsageRecord{
		ID: pending[0].ID, At: pending[0].At, Site: pending[0].Site, Caller: pending[0].Caller,
		CallerName: pending[0].CallerName, Model: pending[0].Model, Usage: pending[0].EstimatedUsage,
		CostMicros: pending[0].CostMicros, Priced: true,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	settlementPath := "/api/hex/manage/ai/settlements/" + record.ID
	requestAs(t, server, roleHeaders("person"), "PUT", settlementPath, encoded, 403)
	requestAs(t, server, admin, "PUT", settlementPath, encoded, 204)
	requestAs(t, server, admin, "PUT", settlementPath, encoded, 204)
	requestAs(t, server, admin, "PUT", "/api/hex/manage/ai/settlements/wrong-id", encoded, 400)
	response = requestAs(t, server, admin, "GET", path, nil, 200)
	if response.Body.String() != "[]\n" {
		t.Fatalf("settled hold remains pending: %s", response.Body.String())
	}
	totals, err := store.AIUsageTotals(ctx, hex.AIUsageFilter{Site: "demo"}, hex.GroupBySite)
	if err != nil || len(totals) != 1 || totals[0].Calls != 1 || totals[0].CostMicros != 50 || totals[0].Estimated != 1 {
		t.Fatalf("operator retries did not settle exactly once: %+v %v", totals, err)
	}
}

func TestAICurrentBudgetMetersIncludeOldPendingHolds(t *testing.T) {
	server, store := setupAIPortal(t)
	at := time.Now().UTC().AddDate(0, -1, 0)
	if err := store.ReserveAIUsage(context.Background(), hex.AIUsageReservation{
		ID: "older-hold", At: at, Site: "demo", Caller: "user:owner", Model: "general", CostMicros: 1_000_000,
	}, nil); err != nil {
		t.Fatal(err)
	}
	page := html.UnescapeString(requestAs(t, server, roleHeaders("owner"), "GET", "/manage/demo?tab=ai", nil, 200).Body.String())
	if !strings.Contains(page, "$1.75 + $1.00 reserved") || !strings.Contains(page, "55% used") {
		t.Fatalf("site budget did not reflect outstanding spend: %s", page)
	}
	page = requestAs(t, server, roleHeaders("owner"), "GET", "/manage/demo?tab=ai&month="+at.Format("2006-01"), nil, 200).Body.String()
	if strings.Contains(page, "pending call") {
		t.Fatal("historical meter included a current admission hold")
	}
}

func TestSiteAITab(t *testing.T) {
	server, usage := setupAIPortal(t)
	owner := roleHeaders("owner")
	admin := principalHeaders("admin", "admin-group")

	page := requestAs(t, server, owner, "GET", "/manage/demo?tab=ai", nil, 200).Body.String()
	for _, expected := range []string{"AI spending", "$1.75", "of $5.00", "35% used", "The default for every site",
		"Olga Owner", "weekly-report", "Automation", "general", "Daily spending", "includes\n    estimates"} {
		if !strings.Contains(page, expected) {
			t.Fatalf("the AI tab lacks %q: %s", expected, page)
		}
	}
	if !strings.Contains(page, "Restricted models on this site: Premium (not") {
		t.Fatalf("the owner does not see the restricted models: %s", page)
	}
	if strings.Contains(page, `name="limit"`) {
		t.Fatal("a site owner can edit the AI budget")
	}
	if strings.Contains(page, "Some One") {
		t.Fatal("another site's spending is shown")
	}
	requestAs(t, server, roleHeaders("stranger"), "GET", "/manage/demo?tab=ai", nil, 403)

	form := url.Values{"limit": {"1.50"}, "enabled": {"off"}, "model.premium": {"on"}, "model.general": {"on"}}
	requestAs(t, server, owner, "PUT", "/api/hex/manage/sites/demo/ai/budget", []byte(form.Encode()), 403)
	saved := formRequest(t, server, admin, "PUT", "/api/hex/manage/sites/demo/ai/budget", form, 200)
	if !strings.Contains(saved, "Saved") || !strings.Contains(saved, "AI is turned off") || !strings.Contains(saved, "Budget used up") {
		t.Fatalf("unexpected saved budget: %s", saved)
	}
	budgets, err := usage.ListAIBudgets(context.Background())
	if err != nil || len(budgets) != 1 || !budgets[0].Disabled || *budgets[0].LimitMicros != 1_500_000 ||
		!slices.Equal(budgets[0].Models, []string{"premium"}) {
		t.Fatalf("the site budget was not stored: %+v %v", budgets, err)
	}

	invalid := formRequest(t, server, admin, "PUT", "/api/hex/manage/sites/demo/ai/budget", url.Values{"limit": {"lots"}, "enabled": {"on"}}, 200)
	if !strings.Contains(invalid, "enter an amount in dollars") {
		t.Fatalf("an invalid limit was accepted: %s", invalid)
	}

	if adminPage := requestAs(t, server, admin, "GET", "/admin/ai", nil, 200).Body.String(); !strings.Contains(adminPage, `<span class="tag">premium</span>`) {
		t.Fatalf("the admin page does not show the enabled model: %s", adminPage)
	}

	formRequest(t, server, admin, "PUT", "/api/hex/manage/sites/demo/ai/budget", url.Values{"limit": {""}, "enabled": {"on"}}, 200)
	if budgets, _ := usage.ListAIBudgets(context.Background()); len(budgets) != 0 {
		t.Fatalf("restoring the defaults left a budget: %+v", budgets)
	}
}

func TestAdminAIPage(t *testing.T) {
	server, usage := setupAIPortal(t)
	admin := principalHeaders("admin", "admin-group")

	requestAs(t, server, roleHeaders("owner"), "GET", "/admin/ai", nil, 403)
	page := requestAs(t, server, admin, "GET", "/admin/ai", nil, 200).Body.String()
	for _, expected := range []string{"AI spending", "$4.75", "No limit", "/manage/other?tab=ai", "premium", "until saved here: The site default."} {
		if !strings.Contains(page, expected) {
			t.Fatalf("the admin AI page lacks %q: %s", expected, page)
		}
	}
	if home := requestAs(t, server, admin, "GET", "/manage", nil, 200).Body.String(); !strings.Contains(home, `href="/admin/ai"`) {
		t.Fatal("admins have no link to the AI page")
	}

	defaults := formRequest(t, server, admin, "PUT", "/api/hex/manage/ai/budgets",
		url.Values{"platform": {"100"}, "siteDefault": {"20"}, "personDefault": {""}}, 200)
	if !strings.Contains(defaults, "Saved") || !strings.Contains(defaults, `value="100"`) || strings.Contains(defaults, "until saved here") {
		t.Fatalf("unexpected defaults: %s", defaults)
	}

	overrides := formRequest(t, server, admin, "POST", "/api/hex/manage/ai/overrides", url.Values{"subject": {"role:AI.Premium"}, "limit": {"50"}}, 200)
	if !strings.Contains(overrides, "role:AI.Premium") || !strings.Contains(overrides, "$50.00 a month") {
		t.Fatalf("the override was not added: %s", overrides)
	}
	rejected := formRequest(t, server, admin, "POST", "/api/hex/manage/ai/overrides", url.Values{"subject": {"everyone"}, "limit": {"50"}}, 200)
	if !strings.Contains(rejected, "role:&lt;app role&gt;") {
		t.Fatalf("an invalid principal was accepted: %s", rejected)
	}
	formRequest(t, server, admin, "DELETE", "/api/hex/manage/ai/overrides?subject=role:AI.Premium", nil, 200)

	budgets, err := usage.ListAIBudgets(context.Background())
	if err != nil || len(budgets) != 3 {
		t.Fatalf("expected the three saved defaults, got %+v %v", budgets, err)
	}
}
