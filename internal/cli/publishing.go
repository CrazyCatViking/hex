package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/spf13/cobra"
)

type sourceFile struct {
	Key  string
	Path string
}

type sourceDirectory struct {
	Directory string
	Files     []sourceFile
}

func validatePublishing(settings Publishing) error {
	switch settings.Provider {
	case "filesystem":
		if settings.Root == "" || settings.URL != "" {
			return errors.New("filesystem publishing requires a root and no URL")
		}
	case "azure-files":
		parsed, err := url.Parse(settings.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || parsed.Path == "/" || settings.Root != "" {
			return errors.New("Azure Files publishing requires an HTTPS share URL without credentials, query parameters or a filesystem root")
		}
	default:
		return errors.New("configure publishing.provider as filesystem or azure-files; publishing does not use the Hex API")
	}
	return nil
}

func containsPath(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && filepath.IsLocal(relative)
}

func readSource(project, directory string) (sourceDirectory, error) {
	var source sourceDirectory
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		return source, err
	}
	directory, err = publishDirectory(project, directory)
	if err != nil {
		return source, err
	}
	source.Directory, err = filepath.EvalSymlinks(resolvePath(project, directory))
	if err != nil {
		return source, fmt.Errorf("open publish directory %q: %w", directory, err)
	}
	if !containsPath(project, source.Directory) {
		return source, errors.New("publish directory must be the project root or a subdirectory of the project")
	}
	foundIndex := false
	err = filepath.WalkDir(source.Directory, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if path == source.Directory {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") || strings.EqualFold(entry.Name(), "node_modules") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if source.Directory == project && filepath.Dir(path) == project && rootProjectFile(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks cannot be published: %s", path)
		}
		if strings.Contains(entry.Name(), `\`) {
			return fmt.Errorf("unsupported filename: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("not a regular file: %s", path)
		}
		key, err := filepath.Rel(source.Directory, path)
		if err != nil {
			return err
		}
		key = filepath.ToSlash(key)
		foundIndex = foundIndex || key == "index.html"
		source.Files = append(source.Files, sourceFile{Key: key, Path: path})
		return nil
	})
	if err != nil {
		return source, err
	}
	if !foundIndex {
		return source, errors.New("publish directory must contain index.html")
	}
	slices.SortFunc(source.Files, func(left, right sourceFile) int {
		if left.Key == right.Key {
			return 0
		}
		if left.Key == "index.html" {
			return 1
		}
		if right.Key == "index.html" {
			return -1
		}
		return strings.Compare(left.Key, right.Key)
	})
	return source, nil
}

func filesystemDestination(project, root, name string, create bool) (string, error) {
	if err := validateSiteName(name); err != nil {
		return "", err
	}
	root = resolvePath(project, root)
	if create {
		if err := os.MkdirAll(root, 0755); err != nil {
			return "", err
		}
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	destination := filepath.Join(root, name)
	info, err := os.Lstat(destination)
	if create && errors.Is(err, fs.ErrNotExist) {
		return destination, nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("site destination must be a real directory")
	}
	return destination, nil
}

type destinationEntry struct {
	Key       string
	Directory bool
}

func destinationEntries(directory string) ([]destinationEntry, error) {
	var entries []destinationEntry
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if path == directory {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("publishing destination contains a symlink: %s", path)
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported destination entry: %s", path)
		}
		key, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		entries = append(entries, destinationEntry{Key: key, Directory: entry.IsDir()})
		return nil
	})
	return entries, err
}

func copyFile(source, destination string) (result error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, input.Close()) }()
	output, err := os.CreateTemp(filepath.Dir(destination), ".hex-*")
	if err != nil {
		return err
	}
	temporary := output.Name()
	defer func() {
		if err := os.Remove(temporary); err != nil && !errors.Is(err, fs.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, copyError := io.Copy(output, input)
	permissionError := output.Chmod(0644)
	if err := errors.Join(copyError, permissionError, output.Close()); err != nil {
		return err
	}
	return os.Rename(temporary, destination)
}

func syncFilesystem(ctx context.Context, source sourceDirectory, destination string) error {
	if containsPath(source.Directory, destination) || containsPath(destination, source.Directory) {
		return errors.New("source and destination directories must not overlap")
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	previous, err := destinationEntries(destination)
	if err != nil {
		return err
	}
	files := make(map[string]bool)
	directories := make(map[string]bool)
	for _, file := range source.Files {
		key := filepath.FromSlash(file.Key)
		files[key] = true
		for parent := filepath.Dir(key); parent != "."; parent = filepath.Dir(parent) {
			directories[parent] = true
		}
	}
	for i := len(previous) - 1; i >= 0; i-- {
		entry := previous[i]
		keep := files[entry.Key]
		if entry.Directory {
			keep = directories[entry.Key]
		}
		if !keep {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := os.RemoveAll(filepath.Join(destination, entry.Key)); err != nil {
				return err
			}
		}
	}
	for _, file := range source.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := copyFile(file.Path, filepath.Join(destination, filepath.FromSlash(file.Key))); err != nil {
			return err
		}
	}
	return nil
}

func azureSiteURL(settings Publishing, name string) (string, error) {
	if err := validatePublishing(settings); err != nil {
		return "", err
	}
	if err := validateSiteName(name); err != nil {
		return "", err
	}
	destination, err := url.Parse(settings.URL)
	if err != nil {
		return "", err
	}
	destination.Path = strings.TrimRight(destination.Path, "/") + "/" + name
	destination.RawPath = ""
	destination.RawQuery = strings.TrimPrefix(os.Getenv("HEX_PUBLISH_SAS"), "?")
	return destination.String(), nil
}

func (a *App) publish(ctx context.Context, project Project, name string) error {
	if project.Publishing == nil {
		return errors.New("configure a publishing provider with hex setup or in hex.json")
	}
	if err := validatePublishing(*project.Publishing); err != nil {
		return err
	}
	source, err := readSource(a.Dir, project.Directory)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "hex-publish-*")
	if err != nil {
		return err
	}
	defer a.removeTemporary(temporary)
	metadata := hex.SiteMetadata{
		Title: project.Title, Description: project.Description, Author: project.Author,
		Discoverable: project.Discoverable,
		PublishedAt:  time.Now().UTC(),
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxConfigBytes {
		return errors.New("site metadata exceeds 64 KiB")
	}
	metadataPath := filepath.Join(temporary, ".hex-site.json")
	if err := os.WriteFile(metadataPath, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("prepare site metadata: %w", err)
	}
	if project.Publishing.Provider == "filesystem" {
		destination, err := filesystemDestination(a.Dir, project.Publishing.Root, name, true)
		if err != nil {
			return err
		}
		source.Files = append(source.Files, sourceFile{Key: ".hex-site.json", Path: metadataPath})
		return syncFilesystem(ctx, source, destination)
	}

	destination, err := azureSiteURL(*project.Publishing, name)
	if err != nil {
		return err
	}
	for _, file := range source.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := copyFile(file.Path, filepath.Join(temporary, filepath.FromSlash(file.Key))); err != nil {
			return err
		}
	}
	return a.storageCommand(ctx, "sync", temporary, destination, "--recursive=true", "--delete-destination=true")
}

func (a *App) publishCommand() *cobra.Command {
	var profile, server, baseURL string
	command := &cobra.Command{
		Use:   "publish [site]",
		Short: "Synchronize directly to the configured storage provider",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := a.commandConfig(profile, true)
			if err != nil {
				return err
			}
			name := project.Name
			if len(args) > 0 {
				name = args[0]
			}
			base := project.SiteBaseURL
			if base == "" {
				base = project.Server
			}
			if server != "" && project.SiteBaseURL == "" {
				base = server
			}
			if baseURL != "" {
				base = baseURL
			}
			website, err := siteURL(base, name)
			if err != nil {
				return err
			}
			if err := a.publish(cmd.Context(), project, name); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, website)
			return nil
		},
	}
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	command.Flags().StringVar(&server, "server", "", "Override the platform origin")
	command.Flags().StringVar(&baseURL, "site-base-url", "", "Override the parent site origin")
	return command
}

func (a *App) deleteCommand() *cobra.Command {
	var profile string
	var confirmed bool
	command := &cobra.Command{
		Use:   "delete [site]",
		Short: "Unpublish a site directly from storage",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirmed {
				return errors.New("use --yes to unpublish this site")
			}
			project, err := a.commandConfig(profile, true)
			if err != nil {
				return err
			}
			name := project.Name
			if len(args) > 0 {
				name = args[0]
			}
			if err := validateSiteName(name); err != nil {
				return err
			}
			if project.Publishing == nil {
				return errors.New("no publishing provider configured")
			}
			if err := validatePublishing(*project.Publishing); err != nil {
				return err
			}
			if project.Publishing.Provider == "filesystem" {
				destination, err := filesystemDestination(a.Dir, project.Publishing.Root, name, false)
				if err != nil {
					return err
				}
				if err := os.RemoveAll(destination); err != nil {
					return err
				}
			} else {
				destination, err := azureSiteURL(*project.Publishing, name)
				if err != nil {
					return err
				}
				if err := a.storageCommand(cmd.Context(), "remove", destination, "--recursive=true"); err != nil {
					return err
				}
			}
			fmt.Fprintln(a.Out, "Unpublished", name)
			return nil
		},
	}
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	command.Flags().BoolVar(&confirmed, "yes", false, "Confirm unpublishing")
	return command
}
