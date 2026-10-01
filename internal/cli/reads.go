package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	hex "github.com/crazycatviking/hex/server"
	"github.com/spf13/cobra"
)

var identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type siteCommandOptions struct {
	platform string
	site     string
}

func (o *siteCommandOptions) register(command *cobra.Command) {
	command.PersistentFlags().StringVar(&o.platform, "platform", "", "Saved platform profile")
	command.PersistentFlags().StringVar(&o.site, "site", "", "Site name (required)")
}

func (o *siteCommandOptions) config(a *App) (Project, error) {
	if o.site == "" {
		return Project{}, errors.New("provide --site")
	}
	if err := validateSiteName(o.site); err != nil {
		return Project{}, err
	}
	return a.commandConfig(o.platform, false)
}

func validateIdentifier(kind, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s must contain 1–64 letters, digits, underscores or hyphens, starting with a letter or digit", kind)
	}
	return nil
}

func (a *App) dataCommand() *cobra.Command {
	options := &siteCommandOptions{}
	command := &cobra.Command{Use: "data", Short: "Read application documents"}
	options.register(command)
	command.AddCommand(a.dataReadCommand(options, false), a.dataReadCommand(options, true))
	return command
}

func (a *App) dataReadCommand(options *siteCommandOptions, get bool) *cobra.Command {
	var collection, id, after string
	var limit int
	name, description := "list", "List a page of documents"
	if get {
		name, description = "get", "Read one document"
	}
	command := &cobra.Command{
		Use: name, Short: description, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateIdentifier("collection", collection); err != nil {
				return err
			}
			path := "/api/sites/" + options.site + "/db/" + collection
			if get {
				if err := validateIdentifier("id", id); err != nil {
					return err
				}
				path += "/" + id
			} else {
				if limit < 1 || limit > 100 {
					return errors.New("--limit must be between 1 and 100")
				}
				if after != "" {
					if err := validateIdentifier("after", after); err != nil {
						return err
					}
				}
				query := url.Values{"limit": {strconv.Itoa(limit)}}
				if after != "" {
					query.Set("after", after)
				}
				path += "?" + query.Encode()
			}
			project, err := options.config(a)
			if err != nil {
				return err
			}
			data, err := a.apiRequest(cmd.Context(), project, path)
			if err != nil {
				if !get && errors.Is(err, errAPIResponseTooLarge) {
					return fmt.Errorf("%w; reduce --limit and paginate with --after <last-document-id>", err)
				}
				return err
			}
			return a.printJSON(data)
		},
	}
	command.Flags().StringVar(&collection, "collection", "", "Collection name (required)")
	if get {
		command.Flags().StringVar(&id, "id", "", "Document ID (required)")
	} else {
		command.Flags().IntVar(&limit, "limit", 100, "Page size, 1–100")
		command.Flags().StringVar(&after, "after", "", "Return documents after this ID")
	}
	return command
}

func (a *App) filesCommand() *cobra.Command {
	options := &siteCommandOptions{}
	command := &cobra.Command{Use: "files", Short: "List and download application files"}
	options.register(command)
	var prefix string
	list := &cobra.Command{
		Use: "list", Short: "List readable application files", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := options.config(a)
			if err != nil {
				return err
			}
			data, err := a.apiRequest(cmd.Context(), project, "/api/sites/"+options.site+"/files")
			if err != nil {
				return err
			}
			var objects []hex.Object
			if err := json.Unmarshal(data, &objects); err != nil {
				return err
			}
			filtered := make([]hex.Object, 0, len(objects))
			for _, object := range objects {
				if strings.HasPrefix(object.Key, prefix) {
					filtered = append(filtered, object)
				}
			}
			return a.printJSON(filtered)
		},
	}
	list.Flags().StringVar(&prefix, "prefix", "", "Only include keys starting with this prefix")
	var key, output string
	get := &cobra.Command{
		Use: "get", Short: "Download an application file", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if key == "." || !fs.ValidPath(key) || strings.ContainsAny(key, "\\\x00") {
				return errors.New("invalid file key")
			}
			parts := strings.Split(key, "/")
			for i, part := range parts {
				if strings.HasPrefix(part, ".") {
					return errors.New("file keys cannot contain dot-prefixed segments")
				}
				parts[i] = url.PathEscape(part)
			}
			project, err := options.config(a)
			if err != nil {
				return err
			}
			server, err := origin(project.Server, false)
			if err != nil {
				return err
			}
			return a.downloadResponse(cmd.Context(), project, server.String()+"/api/sites/"+options.site+"/files/"+strings.Join(parts, "/"), output)
		},
	}
	get.Flags().StringVar(&key, "key", "", "File key (required)")
	get.Flags().StringVar(&output, "output", "", "Save the file instead of writing to stdout")
	command.AddCommand(list, get)
	return command
}
