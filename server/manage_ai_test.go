package hex_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func setupAIPortal(t *testing.T) (*hex.Server, *memory.AIUsageStore) {
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
		AI:      &hex.AIConfig{Provider: &scriptedProvider{}, Limits: hex.AILimits{SiteMonthly: 5}},
		AIUsage: usage,
	})
	return server, usage
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
	if strings.Contains(page, `name="limit"`) {
		t.Fatal("a site owner can edit the AI budget")
	}
	if strings.Contains(page, "Some One") {
		t.Fatal("another site's spending is shown")
	}
	requestAs(t, server, roleHeaders("stranger"), "GET", "/manage/demo?tab=ai", nil, 403)

	form := url.Values{"limit": {"1.50"}, "enabled": {"off"}}
	requestAs(t, server, owner, "PUT", "/api/hex/manage/sites/demo/ai/budget", []byte(form.Encode()), 403)
	saved := formRequest(t, server, admin, "PUT", "/api/hex/manage/sites/demo/ai/budget", form, 200)
	if !strings.Contains(saved, "Saved") || !strings.Contains(saved, "AI is turned off") || !strings.Contains(saved, "Budget used up") {
		t.Fatalf("unexpected saved budget: %s", saved)
	}
	budgets, err := usage.ListAIBudgets(context.Background())
	if err != nil || len(budgets) != 1 || !budgets[0].Disabled || *budgets[0].LimitMicros != 1_500_000 {
		t.Fatalf("the site budget was not stored: %+v %v", budgets, err)
	}

	invalid := formRequest(t, server, admin, "PUT", "/api/hex/manage/sites/demo/ai/budget", url.Values{"limit": {"lots"}, "enabled": {"on"}}, 200)
	if !strings.Contains(invalid, "enter an amount in dollars") {
		t.Fatalf("an invalid limit was accepted: %s", invalid)
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
