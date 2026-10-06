package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type siteAccess struct {
	Owners  []string `json:"owners,omitempty"`
	Editors []string `json:"editors,omitempty"`
	Viewers []string `json:"viewers,omitempty"`
}

func (a *App) whoamiCommand() *cobra.Command {
	return a.readCommand("whoami", "Show the identity the platform resolves for you", "/api/hex/me", false)
}

func (a *App) accessCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "access",
		Short: "Manage who can view, edit and publish a site",
		Long: "Manage a site's access policy on the platform. A site without a policy is open " +
			"to every signed-in user. Owners manage the policy and publish the site, editors " +
			"write its data and viewers see it; empty viewers means everyone, and empty editors " +
			"means every viewer. Principals are user:<id or email>, group:<object id> or " +
			"role:<value>; use hex whoami to see your own. Path and data rules are easiest to " +
			"keep in the access section of hex.json, which hex publish applies.",
	}
	command.AddCommand(a.accessShowCommand(), a.accessSetCommand(), a.accessClearCommand(), a.permissionsCommand())
	return command
}

func (a *App) accessShowCommand() *cobra.Command {
	options := &accessOptions{}
	command := &cobra.Command{
		Use:   "show <site>",
		Short: "Show a site's access policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodGet, "/access", nil)
		},
	}
	options.register(command)
	return command
}

func (a *App) accessSetCommand() *cobra.Command {
	options := &accessOptions{}
	access := siteAccess{}
	var policyFile string
	command := &cobra.Command{
		Use:   "set <site>",
		Short: "Replace a site's access policy",
		Long: "Replace a site's access policy, either from flags or from a JSON file with the " +
			"same shape as the access section of hex.json. Omitted owners keep the current " +
			"owners, and the result must keep you able to manage the policy.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			usesFlags := len(access.Owners)+len(access.Editors)+len(access.Viewers) > 0
			if policyFile != "" && usesFlags {
				return errors.New("use either --file or --owner/--editor/--viewer")
			}
			if policyFile != "" {
				data, err := os.ReadFile(resolvePath(a.Dir, policyFile))
				if err != nil {
					return err
				}
				if !json.Valid(data) {
					return errors.New("the policy file must contain JSON")
				}
				return a.accessRequest(cmd.Context(), options, args[0], http.MethodPut, "/access", json.RawMessage(data))
			}
			if !usesFlags {
				return errors.New("provide --file, or at least one --owner, --editor or --viewer")
			}
			for _, principal := range append(append(append([]string{}, access.Owners...), access.Editors...), access.Viewers...) {
				if kind, _, typed := strings.Cut(principal, ":"); !typed || (kind != "user" && kind != "group" && kind != "role") {
					return fmt.Errorf("principal %q needs a user:, group: or role: prefix", principal)
				}
			}
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodPut, "/access", access)
		},
	}
	command.Flags().StringArrayVar(&access.Owners, "owner", nil, "Principal that manages and publishes the site (repeatable)")
	command.Flags().StringArrayVar(&access.Editors, "editor", nil, "Principal allowed to write the site's data (repeatable)")
	command.Flags().StringArrayVar(&access.Viewers, "viewer", nil, "Principal allowed to view the site (repeatable)")
	command.Flags().StringVar(&policyFile, "file", "", "JSON file with the complete policy")
	options.register(command)
	return command
}

func (a *App) accessClearCommand() *cobra.Command {
	options := &accessOptions{}
	command := &cobra.Command{
		Use:   "clear <site>",
		Short: "Remove a site's access policy, opening it to all signed-in users",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodDelete, "/access", nil)
		},
	}
	options.register(command)
	return command
}

func (a *App) permissionsCommand() *cobra.Command {
	options := &accessOptions{}
	command := &cobra.Command{
		Use:   "check <site>",
		Short: "Show what you may do on a site",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodGet, "/permissions", nil)
		},
	}
	options.register(command)
	return command
}

type accessOptions struct {
	profile  string
	server   string
	resource string
}

func (o *accessOptions) register(command *cobra.Command) {
	command.Flags().StringVar(&o.profile, "platform", "", "Saved platform profile")
	command.Flags().StringVar(&o.server, "server", "", "Override the API origin")
	command.Flags().StringVar(&o.resource, "resource", "", "Optional Azure CLI API resource")
}

func (a *App) accessRequest(ctx context.Context, options *accessOptions, site, method, suffix string, body any) error {
	if err := validateSiteName(site); err != nil {
		return err
	}
	project, err := a.commandConfig(options.profile, false)
	if err != nil {
		return err
	}
	if options.server != "" {
		project.Server = options.server
	}
	if options.resource != "" {
		project, err = project.withResource(options.resource)
		if err != nil {
			return err
		}
	}

	data, err := a.apiCall(ctx, project, method, "/api/hex/sites/"+site+suffix, body)
	if err != nil {
		return err
	}
	if data == nil {
		fmt.Fprintf(a.Out, "Access policy for %s removed; the site is open to all signed-in users.\n", site)
		return nil
	}
	return a.printJSON(json.RawMessage(data))
}
