package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

// siteJSONCommand builds a command that sends one request for the --site
// (default: the project) and prints the JSON response.
func (a *App) siteJSONCommand(options *automationOptions, use, short string, args cobra.PositionalArgs, request func(site string, args []string) (method, path string, body any, err error)) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, positional []string) error {
		for _, value := range positional {
			if err := validateIdentifier("argument", value); err != nil {
				return err
			}
		}
		project, site, err := options.target(a, false)
		if err != nil {
			return err
		}
		method, path, body, err := request(site, positional)
		if err != nil {
			return err
		}
		data, err := a.apiCall(cmd.Context(), project, method, path, body)
		if err != nil {
			return err
		}
		return a.printJSON(data)
	}}
}

func pageQuery(after string) string {
	if after == "" {
		return ""
	}
	return "?after=" + url.QueryEscape(after)
}

func (a *App) eventsCommand() *cobra.Command {
	options := &automationOptions{}
	command := &cobra.Command{
		Use:   "events",
		Short: "Define, publish and subscribe to durable events between sites",
		Long: "Events are versioned, schema-validated messages delivered at least once to subscribed automations. " +
			"Find events other sites offer with hex actions catalog.",
	}
	command.PersistentFlags().StringVar(&options.platform, "platform", "", "Saved platform profile")
	command.PersistentFlags().StringVar(&options.site, "site", "", "Site name (defaults to the name in hex.json)")

	var typesAfter, recordsAfter string
	types := a.siteJSONCommand(options, "types", "List the site's event types with their subscriptions (owners)", cobra.NoArgs, func(site string, _ []string) (string, string, any, error) {
		return http.MethodGet, "/api/sites/" + site + "/events/types" + pageQuery(typesAfter), nil, nil
	})
	types.Flags().StringVar(&typesAfter, "after", "", "Continue after this cursor")

	var definition string
	define := a.siteJSONCommand(options, "define <event>", "Register an immutable event version from {version,description,schema} (owners)", cobra.ExactArgs(1), func(site string, args []string) (string, string, any, error) {
		if definition == "" {
			return "", "", nil, errors.New("provide --input with {\"version\",\"description\",\"schema\"}")
		}
		body, err := a.jsonInput(definition)
		return http.MethodPut, "/api/sites/" + site + "/events/types/" + args[0], body, err
	})
	define.Flags().StringVar(&definition, "input", "", "Definition JSON or @<file>")

	subscriptions := a.siteJSONCommand(options, "subscriptions", "List the site's own subscriptions to other sites' events (owners)", cobra.NoArgs, func(site string, _ []string) (string, string, any, error) {
		return http.MethodGet, "/api/sites/" + site + "/events/subscriptions", nil, nil
	})

	var subscription struct {
		Producer   string          `json:"producer"`
		Event      string          `json:"event"`
		Version    int             `json:"version"`
		Automation string          `json:"automation"`
		Disabled   bool            `json:"disabled,omitempty"`
		Where      json.RawMessage `json:"where,omitempty"`
	}
	var where string
	subscribe := a.siteJSONCommand(options, "subscribe <subscription-id>", "Create or refresh a subscription that runs a deployed automation (owners)", cobra.ExactArgs(1), func(site string, args []string) (string, string, any, error) {
		if subscription.Producer == "" || subscription.Event == "" || subscription.Version < 1 || subscription.Automation == "" {
			return "", "", nil, errors.New("provide --producer, --event, --version and --automation")
		}
		subscription.Where = nil
		if where != "" {
			condition, err := a.jsonInput(where)
			if err != nil {
				return "", "", nil, err
			}
			subscription.Where = condition
		}
		return http.MethodPut, "/api/sites/" + site + "/events/subscriptions/" + args[0], subscription, nil
	})
	subscribe.Flags().StringVar(&subscription.Producer, "producer", "", "Site that publishes the event")
	subscribe.Flags().StringVar(&subscription.Event, "event", "", "Event name")
	subscribe.Flags().IntVar(&subscription.Version, "version", 0, "Event version to pin")
	subscribe.Flags().StringVar(&subscription.Automation, "automation", "", "Deployed automation that handles the event")
	subscribe.Flags().BoolVar(&subscription.Disabled, "disabled", false, "Stop delivery without removing the subscription")
	subscribe.Flags().StringVar(&where, "where", "", "Record condition on event data.* as JSON or @<file>")

	var publication struct {
		Version        int             `json:"version"`
		Data           json.RawMessage `json:"data"`
		IdempotencyKey string          `json:"idempotencyKey"`
	}
	var data string
	publish := a.siteJSONCommand(options, "publish <event>", "Publish an event; requires event/publish permission", cobra.ExactArgs(1), func(site string, args []string) (string, string, any, error) {
		if publication.Version < 1 || publication.IdempotencyKey == "" || data == "" {
			return "", "", nil, errors.New("provide --version, --input and --idempotency-key")
		}
		var err error
		publication.Data, err = a.jsonInput(data)
		return http.MethodPost, "/api/sites/" + site + "/events/publish/" + args[0], publication, err
	})
	publish.Flags().IntVar(&publication.Version, "version", 0, "Event version")
	publish.Flags().StringVar(&data, "input", "", "Event data JSON or @<file>")
	publish.Flags().StringVar(&publication.IdempotencyKey, "idempotency-key", "", "Stable key for this business event; reuse returns the original event")

	list := a.siteJSONCommand(options, "list", "List events the site published (owners)", cobra.NoArgs, func(site string, _ []string) (string, string, any, error) {
		return http.MethodGet, "/api/sites/" + site + "/events/records" + pageQuery(recordsAfter), nil, nil
	})
	list.Flags().StringVar(&recordsAfter, "after", "", "Continue after this cursor")
	inspect := a.siteJSONCommand(options, "inspect <event-id>", "Show an event's deliveries, attempts and errors (owners)", cobra.ExactArgs(1), func(site string, args []string) (string, string, any, error) {
		return http.MethodGet, "/api/sites/" + site + "/events/records/" + args[0], nil, nil
	})
	replay := a.siteJSONCommand(options, "replay <event-id> <subscription-id>", "Queue a dead delivery again after fixing its handler (consumer owners)", cobra.ExactArgs(2), func(site string, args []string) (string, string, any, error) {
		return http.MethodPost, "/api/sites/" + site + "/events/records/" + args[0] + "/replay/" + args[1], struct{}{}, nil
	})
	command.AddCommand(types, define, subscriptions, subscribe, publish, list, inspect, replay)
	return command
}

func (a *App) workflowsCommand() *cobra.Command {
	options := &automationOptions{}
	command := &cobra.Command{
		Use:   "workflows",
		Short: "Start and inspect durable workflows within a site",
		Long:  "A workflow runs a deployed automation with checkpoints, timers and child workflows. Workflows belong to one site.",
	}
	command.PersistentFlags().StringVar(&options.platform, "platform", "", "Saved platform profile")
	command.PersistentFlags().StringVar(&options.site, "site", "", "Site name (defaults to the name in hex.json)")

	var after string
	list := a.siteJSONCommand(options, "list", "List the site's workflows (owners)", cobra.NoArgs, func(site string, _ []string) (string, string, any, error) {
		return http.MethodGet, "/api/sites/" + site + "/workflows" + pageQuery(after), nil, nil
	})
	list.Flags().StringVar(&after, "after", "", "Continue after this cursor")
	get := a.siteJSONCommand(options, "get <workflow-id>", "Show a workflow's status, steps, output and error", cobra.ExactArgs(1), func(site string, args []string) (string, string, any, error) {
		return http.MethodGet, "/api/sites/" + site + "/workflows/" + args[0], nil, nil
	})
	var start struct {
		Name           string          `json:"name"`
		Input          json.RawMessage `json:"input"`
		IdempotencyKey string          `json:"idempotencyKey"`
	}
	var input string
	startCommand := a.siteJSONCommand(options, "start <automation>", "Start a workflow; requires workflow/run permission", cobra.ExactArgs(1), func(site string, args []string) (string, string, any, error) {
		if start.IdempotencyKey == "" {
			return "", "", nil, errors.New("provide --idempotency-key")
		}
		var err error
		start.Name = args[0]
		start.Input, err = a.jsonInput(input)
		return http.MethodPost, "/api/sites/" + site + "/workflows", start, err
	})
	startCommand.Flags().StringVar(&input, "input", "", "Workflow input JSON or @<file> (default {})")
	startCommand.Flags().StringVar(&start.IdempotencyKey, "idempotency-key", "", "Stable key; reuse returns the existing workflow")
	command.AddCommand(list, get, startCommand)
	for _, control := range []string{"retry", "cancel"} {
		command.AddCommand(a.siteJSONCommand(options, control+" <workflow-id>", map[string]string{"retry": "Retry a failed workflow (owners)", "cancel": "Cancel a workflow (owners)"}[control], cobra.ExactArgs(1), func(site string, args []string) (string, string, any, error) {
			return http.MethodPost, "/api/sites/" + site + "/workflows/" + args[0] + "/" + control, struct{}{}, nil
		}))
	}
	return command
}
