package hex_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

type postedMessages struct {
	mu    sync.Mutex
	texts []string
}

func (p *postedMessages) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.texts...)
}

func setupAutomations(t *testing.T, grants ...hex.IntegrationGrant) (*hex.Server, *memory.AutomationStore, *memory.Database, *postedMessages) {
	t.Helper()
	posted := &postedMessages{}
	registry := new(hex.IntegrationRegistry)
	if err := registry.Register(hex.Integration{
		Name: "defects", Title: "Defects",
		Endpoints: []hex.IntegrationEndpoint{{
			Name: "counts", Description: "Issue counts.", InputSchema: json.RawMessage(`{"type":"object","properties":{"period":{"type":"string"}},"required":["period"]}`),
			Handler: func(_ context.Context, call hex.IntegrationCall, input json.RawMessage) (any, error) {
				if call.Automation == "" || call.Identity != nil {
					t.Errorf("automation calls must not carry a person: %+v", call)
				}
				return map[string]any{"total": 7, "byLevel": map[string]int{"error": 5, "warning": 2}}, nil
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(hex.Integration{
		Name: "chat", Title: "Chat",
		Endpoints: []hex.IntegrationEndpoint{{
			Name: "post", Description: "Post a message.", Write: true,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
			Handler: func(_ context.Context, _ hex.IntegrationCall, input json.RawMessage) (any, error) {
				var message struct{ Text string }
				if err := json.Unmarshal(input, &message); err != nil {
					return nil, err
				}
				posted.mu.Lock()
				posted.texts = append(posted.texts, message.Text)
				posted.mu.Unlock()
				return map[string]string{"ts": "1"}, nil
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	sites, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sites.Close(); err != nil {
			t.Error(err)
		}
	})
	access := memory.NewAccessStore()
	if err := access.PutSiteAccess(context.Background(), "demo", hex.SiteAccess{Owners: []string{"user:owner"}}); err != nil {
		t.Fatal(err)
	}
	store := memory.NewAutomationStore()
	database := memory.NewDatabase()
	server := hex.New(hex.Config{
		Sites: sites, Publisher: sites, Database: database, SiteBaseURL: "http://example.com",
		Identity: easyauth.Resolver{}, Access: access,
		Integrations: registry, IntegrationStore: memory.NewIntegrationStore(),
		IntegrationGrants: grants,
		Automations:       store,
	})
	return server, store, database, posted
}

var demoGrant = hex.IntegrationGrant{Principal: "site:demo", Permissions: []string{"defects.*", "chat.post"}}

func TestAutomationsNeedSiteGrants(t *testing.T) {
	server, _, _, posted := setupAutomations(t,
		hex.IntegrationGrant{Principal: "site:other", Permissions: []string{"*"}},
		hex.IntegrationGrant{Principal: "user:owner", Permissions: []string{"*"}})
	response := requestAs(t, server, roleHeaders("owner"), "POST", "/api/hex/sites/demo/automations/test?dryRun=false",
		[]byte(`{"name":"post","steps":[{"id":"x","call":"chat.post","input":{"text":"hi"}}]}`), 202)
	var started hex.AutomationRun
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+started.ID)
	if run.Status != hex.RunFailed || !strings.Contains(run.Error, "chat.post permission") || len(posted.all()) != 0 {
		t.Fatalf("an automation used another principal's grant: %+v", run)
	}
}

const weeklyReport = `[{
	"name": "weekly-report",
	"schedule": "0 8 * * MON",
	"timezone": "Europe/Oslo",
	"steps": [
		{"id": "counts", "call": "defects.counts", "input": {"period": "7d"}},
		{"id": "post", "if": "steps.counts.output.total | gt(0)", "call": "chat.post",
		 "input": {"text": "Week {{ now.week }}: {{ steps.counts.output.total }} issues ({{ steps.counts.output.byLevel.error }} errors)"}},
		{"id": "archive", "save": {"collection": "reports", "id": "week-{{ now.week }}", "data": {"total": "{{ steps.counts.output.total }}"}}}
	]
}]`

func waitForRun(t *testing.T, server *hex.Server, path string) hex.AutomationRun {
	t.Helper()
	for range 200 {
		response := requestAs(t, server, roleHeaders("owner"), "GET", path, nil, 200)
		var run hex.AutomationRun
		if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		if run.Status != hex.RunRunning {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the run did not finish")
	return hex.AutomationRun{}
}

func TestAutomationRunsStepsWithSiteGrants(t *testing.T) {
	server, store, database, posted := setupAutomations(t, demoGrant)
	owner := roleHeaders("owner")
	base := "/api/hex/sites/demo/automations"

	requestAs(t, server, roleHeaders("stranger"), "PUT", base, []byte(weeklyReport), 403)
	requestAs(t, server, owner, "PUT", base, []byte(`[{"name":"x","schedule":"* * * * *","steps":[{"id":"a","call":"defects.counts"}]}]`), 400)
	listed := requestAs(t, server, owner, "PUT", base, []byte(weeklyReport), 200)
	if !strings.Contains(listed.Body.String(), `"nextRun":"`) {
		t.Fatal(listed.Body.String())
	}

	dry := requestAs(t, server, owner, "POST", base+"/weekly-report/run?dryRun=true", nil, 202)
	var started hex.AutomationRun
	if err := json.Unmarshal(dry.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+started.ID)
	if run.Status != hex.RunSucceeded || run.Steps[1].Status != hex.StepDryRun || run.Steps[2].Status != hex.StepDryRun {
		t.Fatalf("unexpected dry run: %+v", run)
	}
	if len(posted.all()) != 0 || !strings.Contains(string(run.Steps[1].Output), "issues (5 errors)") {
		t.Fatalf("dry run posted or rendered wrongly: %s", run.Steps[1].Output)
	}

	real := requestAs(t, server, owner, "POST", base+"/weekly-report/run", nil, 202)
	if err := json.Unmarshal(real.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	run = waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+started.ID)
	if run.Status != hex.RunSucceeded {
		t.Fatalf("run failed: %+v", run)
	}
	messages := posted.all()
	if len(messages) != 1 || !strings.HasSuffix(messages[0], ": 7 issues (5 errors)") {
		t.Fatalf("unexpected messages %v", messages)
	}
	documents, err := database.List(context.Background(), "demo", "reports", hex.ListOptions{Limit: 10})
	if err != nil || len(documents) != 1 || documents[0].CreatedBy != "automation:weekly-report" || string(documents[0].Data) != `{"total":7}` {
		t.Fatalf("unexpected saved report: %+v %v", documents, err)
	}

	runs, err := store.ListAutomationRuns(context.Background(), "demo", "weekly-report", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("expected two recorded runs, got %d: %v", len(runs), err)
	}
}

func TestAutomationBirthdaysLoopAndFailures(t *testing.T) {
	server, _, database, posted := setupAutomations(t, demoGrant)
	owner := roleHeaders("owner")
	today := time.Now().UTC().Format("01-02")
	for id, person := range map[string]string{
		"kari": `{"name":"Kari","birthday":"` + today + `"}`,
		"ola":  `{"name":"Ola","birthday":"02-30"}`,
		"per":  `{"name":"Per","birthday":"` + today + `"}`,
	} {
		if _, err := database.Put(context.Background(), "demo", "people", id, json.RawMessage(person), hex.WriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	birthdays := `{"name":"birthdays","steps":[
		{"id":"today","query":{"collection":"people","where":{"birthday":"{{ now.monthDay }}"}}},
		{"id":"greet","forEach":"steps.today.output","call":"chat.post","input":{"text":"Happy birthday {{ item.data.name }}!"}}
	]}`
	response := requestAs(t, server, owner, "POST", "/api/hex/sites/demo/automations/test?dryRun=false", []byte(birthdays), 202)
	var started hex.AutomationRun
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+started.ID)
	messages := posted.all()
	if run.Status != hex.RunSucceeded || len(messages) != 2 || !strings.Contains(strings.Join(messages, ","), "Happy birthday Kari!") {
		t.Fatalf("unexpected birthday run %+v: %v", run, messages)
	}

	response = requestAs(t, server, owner, "POST", "/api/hex/sites/demo/automations/test", []byte(`{"name":"bad","steps":[{"id":"x","call":"defects.missing"}]}`), 202)
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	run = waitForRun(t, server, "/api/hex/sites/demo/automation-runs/"+started.ID)
	if run.Status != hex.RunFailed || !strings.Contains(run.Error, "no integration endpoint") {
		t.Fatalf("expected a failed run: %+v", run)
	}
}

func TestSchedulerClaimsDueAutomations(t *testing.T) {
	server, store, _, posted := setupAutomations(t, demoGrant)
	var automations []hex.Automation
	if err := json.Unmarshal([]byte(weeklyReport), &automations); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := store.ReplaceSiteAutomations(context.Background(), "demo", []hex.ScheduledAutomation{
		{Site: "demo", Automation: automations[0], NextRun: past},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	server.RunBackground(ctx)

	if len(posted.all()) != 1 {
		t.Fatalf("the due automation ran %d times", len(posted.all()))
	}
	stored, err := store.ListSiteAutomations(context.Background(), "demo")
	if err != nil || !stored[0].NextRun.After(time.Now()) {
		t.Fatalf("the next run was not rescheduled: %+v %v", stored, err)
	}
	runs, err := store.ListAutomationRuns(context.Background(), "demo", "weekly-report", 5)
	if err != nil || len(runs) != 1 || runs[0].Trigger != hex.TriggerSchedule || runs[0].Status != hex.RunSucceeded {
		t.Fatalf("unexpected runs %+v %v", runs, err)
	}
}
