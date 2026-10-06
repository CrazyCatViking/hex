package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	hex "github.com/crazycatviking/hex/server"
	"github.com/spf13/cobra"
)

// automationsDirectory holds one automation per JSON file, next to hex.json.
const automationsDirectory = "automations"

const (
	runPollInterval = time.Second
	runWaitLimit    = 15 * time.Minute
)

// projectAutomations reads the automations in hex.json and automations/.
// It returns nil when the project defines neither, so publishing leaves
// automations managed through the API unchanged.
func projectAutomations(directory string, project Project) (*[]hex.Automation, error) {
	automations := []hex.Automation{}
	defined := len(project.Automations) > 0
	if defined {
		if err := decodeStrict(project.Automations, &automations); err != nil {
			return nil, fmt.Errorf("hex.json automations: %w", err)
		}
		for index := range automations {
			if err := resolveAutomationScript(directory, &automations[index]); err != nil {
				return nil, err
			}
		}
	}

	folder := filepath.Join(directory, automationsDirectory)
	entries, err := os.ReadDir(folder)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", automationsDirectory, err)
	}
	if err == nil {
		defined = true
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		automation, err := readAutomationFile(filepath.Join(folder, entry.Name()))
		if err != nil {
			return nil, err
		}
		automation.Script.File = filepath.ToSlash(filepath.Join(automationsDirectory, automation.Script.File))
		automations = append(automations, automation)
	}
	if !defined {
		return nil, nil
	}

	names := make(map[string]bool, len(automations))
	for _, automation := range automations {
		if names[automation.Name] {
			return nil, fmt.Errorf("automation %s is defined more than once", automation.Name)
		}
		names[automation.Name] = true
	}
	if err := hex.ValidateAutomations(automations); err != nil {
		return nil, err
	}
	return &automations, nil
}

func readAutomationFile(path string) (hex.Automation, error) {
	var automation hex.Automation
	data, err := readLimitedFile(path, 1<<20)
	if err != nil {
		return automation, fmt.Errorf("read automation: %w", err)
	}
	if err := decodeStrict(data, &automation); err != nil {
		return automation, fmt.Errorf("%s: %w", filepath.Join(automationsDirectory, filepath.Base(path)), err)
	}
	if automation.Name == "" {
		automation.Name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	if err := resolveAutomationScript(filepath.Dir(path), &automation); err != nil {
		return automation, err
	}
	return automation, nil
}

func resolveAutomationScript(directory string, automation *hex.Automation) error {
	script := automation.Script
	if script == nil {
		return fmt.Errorf("automation %s requires a JavaScript script", automation.Name)
	}
	if script.Source != "" {
		return fmt.Errorf("automation %s: project metadata must reference a .js file, not contain script source", automation.Name)
	}
	if !filepath.IsLocal(script.File) || filepath.Ext(script.File) != ".js" {
		return fmt.Errorf("automation %s: script file must be a local .js path", automation.Name)
	}
	if err := readAutomationScript(directory, script); err != nil {
		return fmt.Errorf("automation %s script %s: %w", automation.Name, script.File, err)
	}
	return nil
}

func readAutomationScript(directory string, script *hex.AutomationScript) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(script.File)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (128<<10)+1))
	if err != nil {
		return err
	}
	if len(data) > 128<<10 {
		return errors.New("script source exceeds 128 KiB")
	}
	script.Source = string(data)
	return nil
}

type automationOptions struct {
	platform string
	site     string
}

// target resolves the platform and site: --site, or the name in hex.json.
func (o *automationOptions) target(a *App, needsProject bool) (Project, string, error) {
	project, err := a.commandConfig(o.platform, needsProject || o.site == "")
	if err != nil {
		return project, "", err
	}
	site := o.site
	if site == "" {
		site = project.Name
	}
	if err := validateSiteName(site); err != nil {
		return project, "", err
	}
	return project, site, nil
}

func (a *App) automationsCommand() *cobra.Command {
	options := &automationOptions{}
	command := &cobra.Command{
		Use:   "automations",
		Short: "Deploy, test and run a site's scheduled automations",
		Long: "Automations are declared in hex.json under \"automations\" or one per file in " +
			"automations/*.json, and are deployed by hex publish or hex automations deploy.",
	}
	command.PersistentFlags().StringVar(&options.platform, "platform", "", "Saved platform profile")
	command.PersistentFlags().StringVar(&options.site, "site", "", "Site name (defaults to the name in hex.json)")

	var jsonOutput bool
	list := &cobra.Command{
		Use: "list", Short: "List the site's deployed automations", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, site, err := options.target(a, false)
			if err != nil {
				return err
			}
			automations, err := a.deployedAutomations(cmd.Context(), project, site)
			if err != nil {
				return err
			}
			if jsonOutput {
				return a.printJSON(automations)
			}
			return printAutomations(a, automations)
		},
	}
	list.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")

	command.AddCommand(list, a.automationsDeployCommand(options), a.automationsTestCommand(options),
		a.automationsRunCommand(options), a.automationsRunsCommand(options))
	return command
}

func (a *App) deployedAutomations(ctx context.Context, project Project, site string) ([]hex.AutomationStatus, error) {
	data, err := a.apiRequest(ctx, project, "/api/hex/sites/"+site+"/automations")
	if err != nil {
		return nil, automationsUnavailable(err)
	}
	var automations []hex.AutomationStatus
	if err := json.Unmarshal(data, &automations); err != nil {
		return nil, fmt.Errorf("unexpected automation list: %w", err)
	}
	return automations, nil
}

func automationsUnavailable(err error) error {
	var status *apiStatusError
	if errors.As(err, &status) && status.Status == http.StatusNotFound && status.Message == "" {
		return errors.New("this platform cannot run automations")
	}
	return err
}

func printAutomations(a *App, automations []hex.AutomationStatus) error {
	if len(automations) == 0 {
		_, err := fmt.Fprintln(a.Out, "No automations are deployed. Declare them in hex.json or automations/*.json, then run hex automations deploy.")
		return err
	}
	table := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSCHEDULE\tNEXT RUN\tLAST RUN")
	for _, automation := range automations {
		schedule := automation.Automation.Schedule
		if schedule == "" {
			schedule = "manual"
		} else if automation.Automation.Timezone != "" {
			schedule += " (" + automation.Automation.Timezone + ")"
		}
		if automation.Automation.Disabled {
			schedule += ", disabled"
		}
		next := "-"
		if !automation.NextRun.IsZero() {
			next = automation.NextRun.Local().Format("2006-01-02 15:04")
		}
		last := "-"
		if automation.LastRun != nil {
			last = automation.LastRun.Status + " " + automation.LastRun.StartedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", automation.Automation.Name, schedule, next, last)
	}
	return table.Flush()
}

func (a *App) automationsDeployCommand(options *automationOptions) *cobra.Command {
	var assumeYes bool
	command := &cobra.Command{
		Use:   "deploy",
		Short: "Replace the site's automations with the project's, without republishing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, site, err := options.target(a, true)
			if err != nil {
				return err
			}
			local, err := projectAutomations(a.Dir, project)
			if err != nil {
				return err
			}
			if local == nil {
				return errors.New("the project defines no automations; add them to hex.json or automations/*.json")
			}
			deployed, err := a.deployedAutomations(cmd.Context(), project, site)
			if err != nil {
				return err
			}
			if err := a.confirmRemovals(cmd.Context(), *local, deployed, assumeYes); err != nil {
				return err
			}
			data, err := a.apiCall(cmd.Context(), project, http.MethodPut, "/api/hex/sites/"+site+"/automations", *local)
			if err != nil {
				return automationsUnavailable(err)
			}
			var result []hex.AutomationStatus
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("unexpected automation list: %w", err)
			}
			fmt.Fprintf(a.Err, "Deployed %d automations to %s\n", len(*local), site)
			return printAutomations(a, result)
		},
	}
	command.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Remove deployed automations the project no longer defines without asking")
	return command
}

func (a *App) confirmRemovals(ctx context.Context, local []hex.Automation, deployed []hex.AutomationStatus, assumeYes bool) error {
	var removed []string
	for _, existing := range deployed {
		kept := slices.ContainsFunc(local, func(automation hex.Automation) bool {
			return automation.Name == existing.Automation.Name
		})
		if !kept {
			removed = append(removed, existing.Automation.Name)
		}
	}
	if len(removed) == 0 || assumeYes {
		return nil
	}
	fmt.Fprintf(a.Err, "Deploying removes automations the project no longer defines: %s.\n", strings.Join(removed, ", "))
	if !a.Interactive {
		return errors.New("rerun with --yes to remove them")
	}
	answer, err := a.ask(ctx, "Continue? [y/N] ")
	if err != nil {
		return err
	}
	if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
		return errors.New("deployment cancelled")
	}
	return nil
}

func (a *App) automationsTestCommand(options *automationOptions) *cobra.Command {
	var live, jsonOutput bool
	command := &cobra.Command{
		Use:   "test <name>",
		Short: "Run the project's local definition of an automation",
		Long: "Send the automation as defined in this project and run it now. By default it " +
			"is a dry run: integration endpoints that change data, actions and saves are " +
			"skipped and show the input they would have used. --live performs them.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, site, err := options.target(a, true)
			if err != nil {
				return err
			}
			local, err := projectAutomations(a.Dir, project)
			if err != nil {
				return err
			}
			var automation *hex.Automation
			if local != nil {
				for index := range *local {
					if (*local)[index].Name == args[0] {
						automation = &(*local)[index]
					}
				}
			}
			if automation == nil {
				return fmt.Errorf("the project defines no automation %s", args[0])
			}
			query := url.Values{"dryRun": {strconv.FormatBool(!live)}}
			path := "/api/hex/sites/" + site + "/automations/test?" + query.Encode()
			return a.startAndFollowRun(cmd.Context(), project, site, path, automation, jsonOutput)
		},
	}
	command.Flags().BoolVar(&live, "live", false, "Perform changes instead of a dry run")
	command.Flags().BoolVar(&jsonOutput, "json", false, "Print the run as JSON")
	return command
}

func (a *App) automationsRunCommand(options *automationOptions) *cobra.Command {
	var dryRun, jsonOutput bool
	command := &cobra.Command{
		Use: "run <name>", Short: "Run a deployed automation now", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateIdentifier("automation", args[0]); err != nil {
				return err
			}
			project, site, err := options.target(a, false)
			if err != nil {
				return err
			}
			path := "/api/hex/sites/" + site + "/automations/" + args[0] + "/run"
			if dryRun {
				path += "?dryRun=true"
			}
			return a.startAndFollowRun(cmd.Context(), project, site, path, nil, jsonOutput)
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "Skip changes and show what they would be")
	command.Flags().BoolVar(&jsonOutput, "json", false, "Print the run as JSON")
	return command
}

func (a *App) automationsRunsCommand(options *automationOptions) *cobra.Command {
	var limit int
	command := &cobra.Command{
		Use: "runs <name>", Short: "Show an automation's recent runs", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateIdentifier("automation", args[0]); err != nil {
				return err
			}
			if limit < 1 || limit > 50 {
				return errors.New("--limit must be between 1 and 50")
			}
			project, site, err := options.target(a, false)
			if err != nil {
				return err
			}
			path := "/api/hex/sites/" + site + "/automations/" + args[0] + "/runs?limit=" + strconv.Itoa(limit)
			data, err := a.apiRequest(cmd.Context(), project, path)
			if err != nil {
				return automationsUnavailable(err)
			}
			return a.printJSON(data)
		},
	}
	command.Flags().IntVar(&limit, "limit", 10, "Number of runs, 1–50")
	return command
}

// startAndFollowRun starts a run, polls it until it finishes and prints it.
// A failed run makes the command fail.
func (a *App) startAndFollowRun(ctx context.Context, project Project, site, path string, body any, jsonOutput bool) error {
	data, err := a.apiCall(ctx, project, http.MethodPost, path, body)
	if err != nil {
		return automationsUnavailable(err)
	}
	var run hex.AutomationRun
	if err := json.Unmarshal(data, &run); err != nil {
		return fmt.Errorf("unexpected run: %w", err)
	}
	fmt.Fprintf(a.Err, "Started run %s of %s\n", terminalText(run.ID), terminalText(run.Automation))

	deadline := time.Now().Add(runWaitLimit)
	for run.Status == hex.RunRunning {
		if time.Now().After(deadline) {
			return fmt.Errorf("run %s is still running; check it later with hex automations runs %s", run.ID, run.Automation)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(runPollInterval):
		}
		data, err := a.apiRequest(ctx, project, "/api/hex/sites/"+site+"/automation-runs/"+run.ID)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &run); err != nil {
			return fmt.Errorf("unexpected run: %w", err)
		}
	}

	if jsonOutput {
		if err := a.printJSON(run); err != nil {
			return err
		}
	} else if err := printRun(a, run); err != nil {
		return err
	}
	if run.Status == hex.RunFailed {
		return fmt.Errorf("the run failed: %s", terminalText(run.Error))
	}
	return nil
}

var operationMarks = map[string]string{
	hex.OperationSucceeded: "✓",
	hex.OperationDryRun:    "~",
	hex.OperationFailed:    "✗",
}

func printRun(a *App, run hex.AutomationRun) error {
	kind := run.Trigger
	if run.DryRun {
		kind += ", dry run"
	}
	duration := run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond)
	fmt.Fprintf(a.Out, "%s (%s): %s in %s\n", terminalText(run.Automation), terminalText(kind), terminalText(run.Status), duration)
	table := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	if len(run.Output) > 0 {
		fmt.Fprintf(table, "  output\t%s\n", outputPreview(run.Output))
	}
	for _, log := range run.Logs {
		fmt.Fprintf(table, "  log\t%s\t\t%s\n", terminalText(log.Message), outputPreview(log.Data))
	}
	for _, operation := range run.Operations {
		detail := operation.Error
		if detail == "" {
			detail = outputPreview(operation.Output)
		}
		fmt.Fprintf(table, "  %s %s %s\t%s\t%dms\t%s\n", operationMarks[operation.Status], terminalText(operation.Kind), terminalText(operation.Target), terminalText(operation.Status), operation.DurationMS, terminalText(detail))
	}
	return table.Flush()
}

func outputPreview(output json.RawMessage) string {
	var buffer bytes.Buffer
	if json.Compact(&buffer, output) != nil {
		buffer.Reset()
		buffer.Write(output)
	}
	preview := buffer.String()
	if len(preview) > 160 {
		preview = preview[:160] + "…"
	}
	return terminalText(preview)
}

func terminalText(text string) string {
	var safe strings.Builder
	for _, character := range text {
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f || (character >= 0x202a && character <= 0x202e) || (character >= 0x2066 && character <= 0x2069) {
			fmt.Fprintf(&safe, "\\u%04x", character)
		} else {
			safe.WriteRune(character)
		}
	}
	return safe.String()
}
