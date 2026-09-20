package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func (a *App) initCommand() *cobra.Command {
	var name, server, profile, resource, baseURL, publishingRoot, publishingURL string
	command := &cobra.Command{
		Use:   "init [directory]",
		Short: "Create a static site project and agent skill",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			directory := a.Dir
			if len(args) > 0 {
				directory = resolvePath(a.Dir, args[0])
			}
			if name == "" {
				name = filepath.Base(directory)
			}
			if err := validateSiteName(name); err != nil {
				return err
			}
			if publishingRoot != "" && publishingURL != "" {
				return errors.New("choose either --publish-root or --publish-url")
			}

			project := Project{Name: name, Directory: "public", Resource: resource}
			var connection *Connection
			var err error
			if profile != "" {
				connection, project.Platform, err = loadProfile(profile, false)
			} else if server == "" && publishingRoot == "" && publishingURL == "" {
				connection, project.Platform, err = loadProfile("", true)
			}
			if err != nil {
				return err
			}

			if connection == nil || server != "" {
				if server == "" {
					server = "http://localhost:8080"
				}
				parsed, err := origin(server, false)
				if err != nil {
					return err
				}
				project.Server = parsed.String()
			}
			if baseURL != "" {
				parsed, err := origin(baseURL, false)
				if err != nil {
					return err
				}
				project.SiteBaseURL = parsed.String()
			}

			if publishingRoot != "" {
				project.Publishing = &Publishing{Provider: "filesystem", Root: resolvePath(a.Dir, publishingRoot)}
			}
			if publishingURL != "" {
				project.Publishing = &Publishing{Provider: "azure-files", URL: publishingURL}
			}

			if err := a.initializeProject(directory, project); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "Initialized %s. Agent skill: .agents/skills/hex/SKILL.md\n", name)
			return nil
		},
	}

	flags := command.Flags()
	flags.StringVar(&name, "name", "", "Site name (defaults to directory name)")
	flags.StringVar(&server, "server", "", "Management API origin")
	flags.StringVar(&profile, "platform", "", "Saved platform profile")
	flags.StringVar(&resource, "resource", "", "Optional Azure CLI API resource")
	flags.StringVar(&baseURL, "site-base-url", "", "Parent site origin")
	flags.StringVar(&publishingRoot, "publish-root", "", "Local publishing root")
	flags.StringVar(&publishingURL, "publish-url", "", "Azure Files share/prefix URL")
	return command
}

func (a *App) initializeProject(directory string, project Project) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return err
	}
	if err := writeNewFile(filepath.Join(directory, "hex.json"), append(data, '\n')); err != nil {
		return fmt.Errorf("create project configuration: %w", err)
	}

	public := filepath.Join(directory, "public")
	if err := os.MkdirAll(public, 0755); err != nil {
		return err
	}
	for _, name := range []string{"index.html", "app.js", "hex-client.js"} {
		content, err := assets.ReadFile("assets/" + name)
		if err != nil {
			return err
		}
		if name == "app.js" {
			encodedName, err := json.Marshal(project.Name)
			if err != nil {
				return err
			}
			content = bytes.ReplaceAll(content, []byte(`"__HEX_SITE_NAME__"`), encodedName)
		}
		if err := writeNewFile(filepath.Join(public, name), content); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create starter file %s: %w", name, err)
		}
	}
	return installSkills(directory)
}

func installSkills(directory string) error {
	content, err := assets.ReadFile("assets/SKILL.md")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(directory, ".agents", "skills", "hex", "SKILL.md"), content, 0644)
}

func (a *App) skillsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "skills",
		Short: "Install or refresh the Hex agent skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := installSkills(a.Dir); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, "Installed .agents/skills/hex/SKILL.md")
			return nil
		},
	}
}
