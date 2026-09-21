package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

type siteAccess struct {
	Owners []string `json:"owners"`
	Groups []string `json:"groups"`
}

func (a *App) whoamiCommand() *cobra.Command {
	return a.readCommand("whoami", "Show the identity the platform resolves for you", "/api/hex/me", false)
}

func (a *App) accessCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "access",
		Short: "Manage who can view a site",
		Long: "Manage a site's access entry on the platform. A site without an entry is open " +
			"to every authenticated user. Owners manage the entry; the listed groups may view " +
			"the site and use its data APIs. Values are identity-provider group or role " +
			"identifiers, or individual user IDs; use hex whoami to see your own.",
	}
	command.AddCommand(a.accessShowCommand(), a.accessSetCommand(), a.accessClearCommand())
	return command
}

func (a *App) accessShowCommand() *cobra.Command {
	options := &accessOptions{}
	command := &cobra.Command{
		Use:   "show <site>",
		Short: "Show a site's access entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodGet, nil)
		},
	}
	options.register(command)
	return command
}

func (a *App) accessSetCommand() *cobra.Command {
	options := &accessOptions{}
	access := siteAccess{Owners: []string{}, Groups: []string{}}
	command := &cobra.Command{
		Use:   "set <site>",
		Short: "Replace a site's access entry",
		Long: "Replace a site's access entry. The entry must keep you able to manage it, so " +
			"include your own ID or one of your groups as an owner. An entry with owners but " +
			"no groups reserves ownership without restricting viewers.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(access.Owners) == 0 && len(access.Groups) == 0 {
				return fmt.Errorf("provide at least one --owner or --group")
			}
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodPut, access)
		},
	}
	command.Flags().StringArrayVar(&access.Owners, "owner", nil, "ID that manages the entry (repeatable)")
	command.Flags().StringArrayVar(&access.Groups, "group", nil, "Group allowed to view the site (repeatable)")
	options.register(command)
	return command
}

func (a *App) accessClearCommand() *cobra.Command {
	options := &accessOptions{}
	command := &cobra.Command{
		Use:   "clear <site>",
		Short: "Remove a site's access entry, opening it to all authenticated users",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodDelete, nil)
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

func (a *App) accessRequest(ctx context.Context, options *accessOptions, site, method string, body any) error {
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
		project.Resource = options.resource
	}

	data, err := a.apiCall(ctx, project, method, "/api/hex/sites/"+site+"/access", body)
	if err != nil {
		return err
	}
	if data == nil {
		fmt.Fprintf(a.Out, "Access entry for %s removed; the site is open to all authenticated users.\n", site)
		return nil
	}
	return a.printJSON(json.RawMessage(data))
}
