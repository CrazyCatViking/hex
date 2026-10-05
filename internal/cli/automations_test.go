package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func writeProjectFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const automationProject = `{
	"name": "demo",
	"automations": [{
		"name": "weekly",
		"schedule": "0 8 * * MON",
		"timezone": "Europe/Oslo",
		"steps": [
			{"id": "deals", "call": "crm.deals", "input": {"stage": "won"}},
			{"id": "post", "call": "chat.post", "input": {"text": "{{ steps.deals.output.deals | join(\", \") }} won"}}
		]
	}]
}`

func TestProjectAutomationSources(t *testing.T) {
	directory := t.TempDir()
	project := Project{Name: "demo"}
	automations, err := projectAutomations(directory, project)
	if err != nil || automations != nil {
		t.Fatalf("a project without automations must leave them unchanged: %v %v", automations, err)
	}

	writeProjectFile(t, filepath.Join(directory, "automations", "birthdays.json"),
		`{"schedule":"@daily","steps":[{"id":"people","query":{"collection":"people"}}]}`)
	writeProjectFile(t, filepath.Join(directory, "automations", "notes.txt"), "ignored")
	project.Automations = json.RawMessage(`[{"name":"weekly","steps":[{"id":"a","query":{"collection":"x"}}]}]`)
	automations, err = projectAutomations(directory, project)
	if err != nil || len(*automations) != 2 || (*automations)[1].Name != "birthdays" {
		t.Fatalf("unexpected automations %+v: %v", automations, err)
	}

	project.Automations = json.RawMessage(`[{"name":"birthdays","steps":[{"id":"a","query":{"collection":"x"}}]}]`)
	if _, err := projectAutomations(directory, project); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("expected a duplicate error: %v", err)
	}
	project.Automations = json.RawMessage(`[{"name":"typo","schedul":"@daily","steps":[]}]`)
	if _, err := projectAutomations(directory, project); err == nil {
		t.Fatal("accepted an unknown field")
	}
	project.Automations = json.RawMessage(`[{"name":"fast","schedule":"* * * * *","steps":[{"id":"a","query":{"collection":"x"}}]}]`)
	if _, err := projectAutomations(directory, project); err == nil {
		t.Fatal("accepted a schedule that runs every minute")
	}

	empty := t.TempDir()
	automations, err = projectAutomations(empty, Project{Automations: json.RawMessage(`[]`)})
	if err != nil || automations == nil || len(*automations) != 0 {
		t.Fatalf("an explicit empty list must remove automations: %v %v", automations, err)
	}
}

func TestAutomationsCLI(t *testing.T) {
	directory := t.TempDir()
	var calls atomic.Int32
	platform := startIntegrationPlatform(t, &calls)
	saveReadProfile(t, directory, platform.URL)
	writeProjectFile(t, filepath.Join(directory, "hex.json"), automationProject)
	writeProjectFile(t, filepath.Join(directory, "index.html"), "<h1>Demo</h1>")
	writeProjectFile(t, filepath.Join(directory, "automations", "daily.json"),
		`{"schedule":"@daily","steps":[{"id":"deals","call":"crm.deals"}]}`)

	if listing := run(t, directory, "automations", "list"); !strings.Contains(listing, "No automations are deployed") {
		t.Fatal(listing)
	}

	// Publishing from the project root deploys automations but does not
	// publish the automations folder.
	run(t, directory, "publish")
	if _, err := os.Stat(filepath.Join(platform.Sites, "demo", "automations")); err == nil {
		t.Fatal("the automations folder was published")
	}
	listing := run(t, directory, "automations", "list")
	if !strings.Contains(listing, "weekly") || !strings.Contains(listing, "0 8 * * MON (Europe/Oslo)") || !strings.Contains(listing, "daily") {
		t.Fatal(listing)
	}

	// Sites need approval for crm; an admin's request approves it.
	run(t, directory, "integrations", "request", "--site", "demo", "crm")
	tested := run(t, directory, "automations", "test", "weekly")
	if !strings.Contains(tested, "weekly (test, dry run): succeeded") || !strings.Contains(tested, "~ post") || !strings.Contains(tested, "Acme won") || calls.Load() != 1 {
		t.Fatalf("unexpected dry run (%d calls): %s", calls.Load(), tested)
	}
	live := run(t, directory, "automations", "run", "weekly")
	if !strings.Contains(live, "weekly (manual): succeeded") || calls.Load() != 3 {
		t.Fatalf("unexpected run (%d calls): %s", calls.Load(), live)
	}
	runs := run(t, directory, "automations", "runs", "weekly", "--limit", "5")
	var history []map[string]any
	if err := json.Unmarshal([]byte(runs), &history); err != nil || len(history) != 2 {
		t.Fatalf("expected two runs: %v %s", err, runs)
	}

	// Removing a definition needs confirmation.
	if err := os.Remove(filepath.Join(directory, "automations", "daily.json")); err != nil {
		t.Fatal(err)
	}
	err, output := runFailing(t, directory, "automations", "deploy")
	if !strings.Contains(err.Error(), "--yes") || !strings.Contains(output, "daily") {
		t.Fatalf("removal was not confirmed: %v %s", err, output)
	}
	if deployed := run(t, directory, "automations", "deploy", "--yes"); strings.Contains(deployed, "daily") {
		t.Fatal(deployed)
	}

	err, _ = runFailing(t, directory, "automations", "test", "missing")
	if !strings.Contains(err.Error(), "no automation missing") {
		t.Fatal(err)
	}
	writeProjectFile(t, filepath.Join(directory, "automations", "broken.json"),
		`{"steps":[{"id":"x","call":"crm.unknown"}]}`)
	err, output = runFailing(t, directory, "automations", "test", "broken", "--live")
	if !strings.Contains(err.Error(), "the run failed") || !strings.Contains(output, "✗ x") {
		t.Fatalf("expected a failed run: %v %s", err, output)
	}
}

func TestPublishingWithoutAutomationsKeepsDeployedOnes(t *testing.T) {
	directory := t.TempDir()
	platform := startIntegrationPlatform(t, new(atomic.Int32))
	saveReadProfile(t, directory, platform.URL)
	writeProjectFile(t, filepath.Join(directory, "hex.json"), automationProject)
	writeProjectFile(t, filepath.Join(directory, "index.html"), "<h1>Demo</h1>")
	run(t, directory, "automations", "deploy")

	writeProjectFile(t, filepath.Join(directory, "hex.json"), `{"name":"demo"}`)
	run(t, directory, "publish")
	if listing := run(t, directory, "automations", "list"); !strings.Contains(listing, "weekly") {
		t.Fatal(listing)
	}
}
