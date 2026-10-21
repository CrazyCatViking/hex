package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func (a *App) workspaceCommand() *cobra.Command {
	command := &cobra.Command{Use: "workspace", Short: "Discover and select directory-backed workspaces"}
	var jsonOutput bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List workspaces available to a directory account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			directory, account, err := a.workspaceDirectorySelection()
			if err != nil {
				return err
			}
			entries, err := a.workspaceEntries(cmd.Context(), directory, account)
			if err != nil {
				return err
			}
			if jsonOutput {
				return a.printJSON(entries)
			}
			out := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(out, "WORKSPACE\tORGANIZATION\tROLE\tSTATUS")
			for _, entry := range entries {
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", entry.ID, entry.Organization, entry.Role, entry.Status)
			}
			return out.Flush()
		},
	}
	list.Flags().BoolVar(&jsonOutput, "json", false, "Machine-readable workspace list")
	var pinProject bool
	use := &cobra.Command{
		Use:   "use <workspace>",
		Short: "Select an available workspace, or pin it in this project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			directory, account, err := a.workspaceDirectorySelection()
			if err != nil {
				return err
			}
			project, err := a.resolveWorkspace(directory, account, args[0], Project{})
			if err != nil {
				return err
			}
			if pinProject {
				if err := a.pinWorkspace(project.Workspace); err != nil {
					return err
				}
				fmt.Fprintf(a.Out, "Pinned workspace %s in hex.json.\n", project.Workspace.ID)
				return nil
			}
			store, file, err := readDirectories()
			if err != nil {
				return err
			}
			store.Active = &workspaceContext{workspacePin: *project.Workspace, Account: account}
			if err := writeJSONFile(file, store); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "Selected workspace %s (%s), account %s. Project pins remain unchanged.\n", project.Workspace.ID, project.Server, account)
			return nil
		},
	}
	use.Flags().BoolVar(&pinProject, "project", false, "Pin the stable directory/workspace identity in this project's hex.json instead of changing the default")
	command.AddCommand(list, use)
	return command
}

func (a *App) workspaceDirectorySelection() (workspaceDirectory, string, error) {
	store, _, err := readDirectories()
	if err != nil {
		return workspaceDirectory{}, "", err
	}
	selector, account := a.directory, a.account
	if selector == "" {
		project, err := readProjectFile(a.Dir, true)
		if err != nil {
			return workspaceDirectory{}, "", err
		}
		if project.Workspace != nil {
			if err := validateWorkspacePin(project.Workspace); err != nil {
				return workspaceDirectory{}, "", err
			}
			selector = project.Workspace.Directory
		}
	}
	if selector == "" && store.Active != nil {
		selector = store.Active.Directory
	}
	directory, err := store.selectDirectory(selector)
	if err != nil {
		return directory, "", err
	}
	if account == "" && store.Active != nil && store.Active.Directory == directory.Server {
		account = store.Active.Account
	}
	account, err = accountAlias(account)
	return directory, account, err
}

func (a *App) pinWorkspace(pin *workspacePin) error {
	file := filepath.Join(a.Dir, "hex.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read project to pin: %w", err)
	}
	var project map[string]json.RawMessage
	if err := json.Unmarshal(data, &project); err != nil || project == nil {
		return errors.New("invalid project configuration")
	}
	for _, field := range []string{"platform", "server", "siteBaseURL"} {
		if value, exists := project[field]; exists && string(value) != `""` && string(value) != "null" {
			return fmt.Errorf("project already pins %s; remove that direct connection setting before adding a directory workspace pin", field)
		}
	}
	encoded, err := json.Marshal(pin)
	if err != nil {
		return err
	}
	project["workspace"] = encoded
	return writeJSONFile(file, project)
}
