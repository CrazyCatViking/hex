package hex_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

var runLinkPattern = regexp.MustCompile(`/api/hex/manage/sites/demo/automation-runs/([0-9a-f]+)`)

func TestPortalShowsAndTestsAutomations(t *testing.T) {
	server, _, _, posted := setupAutomations(t, demoGrant)
	owner := roleHeaders("owner")
	requestAs(t, server, owner, "PUT", "/api/hex/sites/demo/automations", []byte(weeklyReport), 200)

	page := requestAs(t, server, owner, "GET", "/manage/demo?tab=automations", nil, 200).Body.String()
	if !strings.Contains(page, `href="/manage/demo?tab=automations"`) || !strings.Contains(page, "/api/hex/manage/sites/demo/automations") {
		t.Fatal("the site page has no automations tab")
	}

	listing := requestAs(t, server, owner, "GET", "/api/hex/manage/sites/demo/automations", nil, 200).Body.String()
	for _, expected := range []string{"weekly-report", "Every Monday at 08:00", "Europe/Oslo", "Next:", "JavaScript:", "weekly-report.js"} {
		if !strings.Contains(listing, expected) {
			t.Fatalf("the listing lacks %q: %s", expected, listing)
		}
	}

	started := requestAs(t, server, owner, "POST", "/api/hex/manage/sites/demo/automations/weekly-report/run", nil, 200).Body.String()
	match := runLinkPattern.FindStringSubmatch(started)
	if match == nil {
		t.Fatalf("a started run does not poll for progress: %s", started)
	}
	finished := pollRunFragment(t, server, match[0])
	if !strings.Contains(finished, "succeeded") || !strings.Contains(finished, "dry-run") || len(posted.all()) != 0 {
		t.Fatalf("a portal test was not a dry run: %s", finished)
	}

	live := requestAs(t, server, owner, "POST", "/api/hex/manage/sites/demo/automations/weekly-report/run?live=true", nil, 200).Body.String()
	pollRunFragment(t, server, runLinkPattern.FindString(live))
	if len(posted.all()) != 1 {
		t.Fatal("run now did not run the automation for real")
	}

	history := requestAs(t, server, owner, "GET", "/api/hex/manage/sites/demo/automations/weekly-report/runs", nil, 200).Body.String()
	if strings.Count(history, "/automation-runs/") != 2 || !strings.Contains(history, "Test") || !strings.Contains(history, "By hand") {
		t.Fatalf("unexpected history: %s", history)
	}

	stranger := roleHeaders("stranger")
	requestAs(t, server, stranger, "GET", "/api/hex/manage/sites/demo/automations", nil, 403)
	requestAs(t, server, stranger, "POST", "/api/hex/manage/sites/demo/automations/weekly-report/run", nil, 403)
	requestAs(t, server, stranger, "GET", match[0], nil, 403)
}

// pollRunFragment follows a run's progress fragment until it stops polling.
func pollRunFragment(t *testing.T, server *hex.Server, path string) string {
	t.Helper()
	for range 200 {
		fragment := requestAs(t, server, roleHeaders("owner"), "GET", path, nil, 200).Body.String()
		if !strings.Contains(fragment, "hx-trigger") {
			return fragment
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the run did not finish")
	return ""
}
