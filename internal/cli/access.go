package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	hex "github.com/crazycatviking/hex/server"

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
		Short: "Manage site membership, application roles and permission grants",
		Long: "Version 2 policies separate site owners from application roles and explicit operation grants. " +
			"Use --file for the complete policy. No grant means no application access. " +
			"Principals use stable user IDs, group IDs or identity-provider role values. " +
			"Publishing merges local changes while preserving portal edits. Existing policies migrate automatically.",
	}
	command.AddCommand(a.accessShowCommand(), a.accessSetCommand(), a.accessClearCommand(), a.permissionsCommand(), a.accessReconcileCommand())
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

func (a *App) accessReconcileCommand() *cobra.Command {
	options := &accessOptions{}
	var file string
	command := &cobra.Command{
		Use:   "reconcile <site>",
		Short: "Record a local baseline without changing live permissions",
		Long:  "Explicitly establish the accepted local policy for three-way publication merging. Live access is preserved; differences from this file are treated as portal changes on the next publication.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return errors.New("provide --file with a local version 2 policy")
			}
			data, err := os.ReadFile(resolvePath(a.Dir, file))
			if err != nil {
				return err
			}
			var policy hex.SiteAccess
			if err := json.Unmarshal(data, &policy); err != nil {
				return err
			}
			if err := hex.ValidatePublishedAccess(policy); err != nil {
				return err
			}
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodPost, "/access/baseline", policy)
		},
	}
	command.Flags().StringVar(&file, "file", "", "Local policy JSON (not the enclosing hex.json)")
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
			policy := hex.MigrateSiteAccess(hex.SiteAccess{Owners: access.Owners, Editors: access.Editors, Viewers: access.Viewers})
			return a.accessRequest(cmd.Context(), options, args[0], http.MethodPut, "/access", policy)
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
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		var policy hex.SiteAccess
		if err := json.Unmarshal(encoded, &policy); err != nil {
			return fmt.Errorf("invalid access policy: %w", err)
		}
		if policy.Version == 2 {
			if err := a.requirePermissionVersion(ctx, project); err != nil {
				return err
			}
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

// Old servers ignore unknown policy fields and could interpret version 2 as
// empty legacy lists. Never send a new policy without live version support.
func (a *App) requirePermissionVersion(ctx context.Context, project Project) error {
	data, err := a.apiRequest(ctx, project, "/api/hex/capabilities")
	if err != nil {
		return fmt.Errorf("verify platform permission support: %w", err)
	}
	var capabilities Capabilities
	if err := json.Unmarshal(data, &capabilities); err != nil {
		return fmt.Errorf("read platform permission support: %w", err)
	}
	if capabilities.PermissionVersion < 2 {
		return errors.New("this platform does not support version 2 permission policies; update the server before publishing or changing access. Sending the policy to an older server could open the site")
	}
	return nil
}
