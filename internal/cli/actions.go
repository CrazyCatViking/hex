package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	hex "github.com/crazycatviking/hex/server"
	"github.com/spf13/cobra"
)

func (a *App) actionsCommand() *cobra.Command {
	options := &siteCommandOptions{}
	command := &cobra.Command{Use: "actions", Short: "Discover and perform app-defined actions"}
	options.register(command)
	list := &cobra.Command{
		Use: "list", Short: "List the actions available to you", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := options.config(a)
			if err != nil {
				return err
			}
			data, err := a.apiRequest(cmd.Context(), project, "/api/sites/"+options.site+"/actions")
			if err != nil {
				return err
			}
			return a.printJSON(data)
		},
	}
	var query string
	catalog := &cobra.Command{
		Use: "catalog", Short: "List exported actions and events shared in the workspace, with their owners", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(options.platform, false)
			if err != nil {
				return err
			}
			data, err := a.apiRequest(cmd.Context(), project, "/api/hex/workspace-apis?q="+url.QueryEscape(query))
			if err != nil {
				return err
			}
			return a.printJSON(data)
		},
	}
	catalog.Flags().StringVar(&query, "query", "", "Only entries whose site, name or description contain this text")
	consumer := &consumerOptions{}
	available := &cobra.Command{
		Use: "available", Short: "List exported actions on --site that this app (or --from) can call now", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := options.config(a)
			if err != nil {
				return err
			}
			path, err := consumer.path(project, options.site)
			if err != nil {
				return err
			}
			data, err := a.apiRequest(cmd.Context(), project, path)
			if err != nil {
				return err
			}
			return a.printJSON(data)
		},
	}
	consumer.register(available)
	command.AddCommand(list, catalog, available, a.actionCommand(options, false), a.actionCommand(options, true), a.siteActionCodegenCommand(options))
	return command
}

// consumerOptions name the calling app whose connections to a target are
// checked; the default is the current project.
type consumerOptions struct {
	from   string
	asSite bool
}

func (o *consumerOptions) register(command *cobra.Command) {
	command.Flags().StringVar(&o.from, "from", "", "Calling app (default: this project's site)")
	command.Flags().BoolVar(&o.asSite, "as-site", false, "Check calls made as the app, as its automations make them")
}

func (o *consumerOptions) path(project Project, target string) (string, error) {
	from := o.from
	if from == "" {
		from = project.Name
	}
	if from == "" {
		return "", errors.New("provide --from or run inside a Hex project")
	}
	if err := validateSiteName(from); err != nil {
		return "", err
	}
	path := "/api/sites/" + from + "/site-api/actions/" + target
	if o.asSite {
		path += "?mode=site"
	}
	return path, nil
}

func (a *App) actionCommand(options *siteCommandOptions, execute bool) *cobra.Command {
	var input string
	name, description := "describe", "Show an action's description and input/output schemas"
	if execute {
		name, description = "run", "Validate input and perform an app-defined action"
	}
	command := &cobra.Command{
		Use: name + " <action>", Short: description, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateIdentifier("action", args[0]); err != nil {
				return err
			}
			var payload json.RawMessage
			if execute {
				if !strings.HasPrefix(input, "@") || len(input) == 1 {
					return errors.New("provide --input @<JSON-file>")
				}
				data, err := readLimitedFile(resolvePath(a.Dir, strings.TrimPrefix(input, "@")), hex.MaxActionInputBytes)
				if err != nil {
					return fmt.Errorf("read action input: %w", err)
				}
				if !json.Valid(data) {
					return errors.New("action input must contain exactly one JSON value")
				}
				payload = data
			}
			project, err := options.config(a)
			if err != nil {
				return err
			}
			path := "/api/sites/" + options.site + "/actions/" + args[0]
			data, err := a.apiRequest(cmd.Context(), project, path)
			if err != nil {
				return err
			}
			if !execute {
				return a.printJSON(data)
			}
			var definition hex.ActionDefinition
			if err := json.Unmarshal(data, &definition); err != nil {
				return fmt.Errorf("invalid action contract: %w", err)
			}
			if definition.Name != args[0] {
				return errors.New("server returned a contract for a different action")
			}
			if err := definition.ValidateInput(payload); err != nil {
				return fmt.Errorf("invalid action input: %w", err)
			}
			// Validate the output schema before invoking a mutating operation.
			if err := definition.ValidateSchemas(); err != nil {
				return err
			}
			result, err := a.apiSend(cmd.Context(), project, http.MethodPost, path, bytes.NewReader(payload), int64(len(payload)), "application/json")
			if err != nil {
				return err
			}
			if err := definition.ValidateOutput(result); err != nil {
				return fmt.Errorf("action response violates its output contract: %w", err)
			}
			return a.printJSON(result)
		},
	}
	if execute {
		command.Flags().StringVar(&input, "input", "", "JSON input file, written as @<path> (required)")
	}
	return command
}
