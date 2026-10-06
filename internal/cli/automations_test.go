package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	hex "github.com/crazycatviking/hex/server"
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

func TestJavaScriptAutomationSourcesAndDeployment(t *testing.T) {
	directory := t.TempDir()
	runJSON := func(args ...string) string {
		app, output := testApp(t, directory)
		app.Err = io.Discard
		if err := app.Execute(t.Context(), args, "test"); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}
	platform := startIntegrationPlatform(t, new(atomic.Int32))
	saveReadProfile(t, directory, platform.URL)
	writeProjectFile(t, filepath.Join(directory, "index.html"), "<h1>Scripts</h1>")
	writeProjectFile(t, filepath.Join(directory, "hex.json"), `{"name":"demo","automations":[{"name":"root","script":{"file":"root.js"}}]}`)
	writeProjectFile(t, filepath.Join(directory, "root.js"), `export default hex => { hex.log("root script"); return {site:hex.site}; };`)
	writeProjectFile(t, filepath.Join(directory, "automations", "folder.json"), `{"script":{"file":"folder.js"}}`)
	writeProjectFile(t, filepath.Join(directory, "automations", "folder.js"), `export default hex => ({site:hex.site, value:42});`)
	run(t, directory, "publish")
	for _, file := range []string{"root.js", "automations/folder.js"} {
		if _, err := os.Stat(filepath.Join(platform.Sites, "demo", file)); !os.IsNotExist(err) {
			t.Fatalf("script source was publicly published: %s %v", file, err)
		}
	}
	output := runJSON("automations", "test", "folder", "--json")
	var result hex.AutomationRun
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.Status != hex.RunSucceeded || !strings.Contains(string(result.Output), "42") {
		t.Fatalf("script test failed: %s %v", output, err)
	}
	// Deployed source is a snapshot; changing the local file does not change it.
	writeProjectFile(t, filepath.Join(directory, "automations", "folder.js"), `export default () => 99;`)
	output = runJSON("automations", "run", "folder", "--json")
	if err := json.Unmarshal([]byte(output), &result); err != nil || !strings.Contains(string(result.Output), "42") {
		t.Fatalf("deployed script read the local file: %s %v", output, err)
	}
	output = run(t, directory, "automations", "run", "root")
	if !strings.Contains(output, "root script") {
		t.Fatalf("CLI omitted script logs: %s", output)
	}
}

func TestJavaScriptAutomationRejectsUnsafePathsAndTypeScript(t *testing.T) {
	directory := t.TempDir()
	for _, filename := range []string{"../outside.js", "/outside.js", "script.ts", "missing.js"} {
		project := Project{Automations: json.RawMessage(`[{"name":"test","script":{"file":"` + filename + `"}}]`)}
		if _, err := projectAutomations(directory, project); err == nil {
			t.Fatalf("accepted script file %q", filename)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.js")
	writeProjectFile(t, outside, `export default () => 1;`)
	if err := os.Symlink(outside, filepath.Join(directory, "escape.js")); err != nil {
		t.Fatal(err)
	}
	project := Project{Automations: json.RawMessage(`[{"name":"test","script":{"file":"escape.js"}}]`)}
	if _, err := projectAutomations(directory, project); err == nil {
		t.Fatal("script symlink escaped the definition directory")
	}
	writeProjectFile(t, filepath.Join(directory, "script.js"), `export default (hex: object) => 1;`)
	project.Automations = json.RawMessage(`[{"name":"test","script":{"file":"script.js"}}]`)
	if _, err := projectAutomations(directory, project); err == nil {
		t.Fatal("TypeScript syntax was transpiled or accepted")
	}
}

func TestAutomationProjectMetadataRequiresFileAndRejectsWorkflowFields(t *testing.T) {
	directory := t.TempDir()
	writeProjectFile(t, filepath.Join(directory, "script.js"), `export default () => 42;`)
	for _, definition := range []string{
		`{"name":"invalid"}`,
		`{"name":"invalid","steps":[{"id":"main","call":"crm.deals"}]}`,
		`{"name":"invalid","script":{"file":"script.js"},"steps":[]}`,
		`{"name":"invalid","script":{"file":"script.js"},"input":{"minimum":5}}`,
		`{"name":"invalid","script":{"source":"export default () => 42;"}}`,
	} {
		if _, err := projectAutomations(directory, Project{Automations: json.RawMessage("[" + definition + "]")}); err == nil {
			t.Fatalf("accepted invalid project metadata %s", definition)
		}
	}
}

const automationProject = `{
	"name": "demo",
	"automations": [{
		"name": "weekly",
		"schedule": "0 8 * * MON",
		"timezone": "Europe/Oslo",
		"script": {"file":"automations/weekly.js"}
	}]
}`

func writeWeeklyAutomationScript(t *testing.T, directory string) {
	t.Helper()
	writeProjectFile(t, filepath.Join(directory, "automations", "weekly.js"), `export default async hex => {
	  const deals = await hex.call("crm.deals", {stage:"won"});
	  return await hex.call("chat.post", {text:deals.deals.join(", ")+" won"});
	};`)
}

func TestProjectAutomationSources(t *testing.T) {
	directory := t.TempDir()
	project := Project{Name: "demo"}
	automations, err := projectAutomations(directory, project)
	if err != nil || automations != nil {
		t.Fatalf("a project without automations must leave them unchanged: %v %v", automations, err)
	}

	writeProjectFile(t, filepath.Join(directory, "automations", "birthdays.json"),
		`{"schedule":"@daily","script":{"file":"birthdays.js"}}`)
	writeProjectFile(t, filepath.Join(directory, "automations", "birthdays.js"), `export default hex => hex.db.query("people");`)
	writeProjectFile(t, filepath.Join(directory, "weekly.js"), `export default () => 1;`)
	writeProjectFile(t, filepath.Join(directory, "automations", "notes.txt"), "ignored")
	project.Automations = json.RawMessage(`[{"name":"weekly","script":{"file":"weekly.js"}}]`)
	automations, err = projectAutomations(directory, project)
	if err != nil || len(*automations) != 2 || (*automations)[1].Name != "birthdays" {
		t.Fatalf("unexpected automations %+v: %v", automations, err)
	}

	project.Automations = json.RawMessage(`[{"name":"birthdays","script":{"file":"weekly.js"}}]`)
	if _, err := projectAutomations(directory, project); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("expected a duplicate error: %v", err)
	}
	project.Automations = json.RawMessage(`[{"name":"typo","schedul":"@daily","script":{"file":"weekly.js"}}]`)
	if _, err := projectAutomations(directory, project); err == nil {
		t.Fatal("accepted an unknown field")
	}
	project.Automations = json.RawMessage(`[{"name":"fast","schedule":"* * * * *","script":{"file":"weekly.js"}}]`)
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
	writeWeeklyAutomationScript(t, directory)
	writeProjectFile(t, filepath.Join(directory, "index.html"), "<h1>Demo</h1>")
	writeProjectFile(t, filepath.Join(directory, "automations", "daily.json"),
		`{"schedule":"@daily","script":{"file":"daily.js"}}`)
	writeProjectFile(t, filepath.Join(directory, "automations", "daily.js"), `export default hex => hex.call("crm.deals");`)

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
	if !strings.Contains(tested, "weekly (test, dry run): succeeded") || !strings.Contains(tested, "~ call chat.post") || !strings.Contains(tested, "Acme won") || calls.Load() != 1 {
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
		`{"script":{"file":"broken.js"}}`)
	writeProjectFile(t, filepath.Join(directory, "automations", "broken.js"), `export default hex => hex.call("crm.unknown");`)
	err, output = runFailing(t, directory, "automations", "test", "broken", "--live")
	if !strings.Contains(err.Error(), "the run failed") || !strings.Contains(output, "✗ call crm.unknown") {
		t.Fatalf("expected a failed run: %v %s", err, output)
	}
}

func TestPublishingWithoutAutomationsKeepsDeployedOnes(t *testing.T) {
	directory := t.TempDir()
	platform := startIntegrationPlatform(t, new(atomic.Int32))
	saveReadProfile(t, directory, platform.URL)
	writeProjectFile(t, filepath.Join(directory, "hex.json"), automationProject)
	writeWeeklyAutomationScript(t, directory)
	writeProjectFile(t, filepath.Join(directory, "index.html"), "<h1>Demo</h1>")
	run(t, directory, "automations", "deploy")

	writeProjectFile(t, filepath.Join(directory, "hex.json"), `{"name":"demo"}`)
	if err := os.RemoveAll(filepath.Join(directory, "automations")); err != nil {
		t.Fatal(err)
	}
	run(t, directory, "publish")
	if listing := run(t, directory, "automations", "list"); !strings.Contains(listing, "weekly") {
		t.Fatal(listing)
	}
}
