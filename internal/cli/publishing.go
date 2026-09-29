package cli

import (
	"context"
	"crypto/md5"
	"encoding/base64"
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
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/file"
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

type publishRequest struct {
	Files    []hex.SiteFile    `json:"files"`
	Metadata *hex.SiteMetadata `json:"metadata,omitempty"`
	Access   json.RawMessage   `json:"access,omitempty"`
}

type publishPlan struct {
	Uploads   []hex.UploadTarget `json:"uploads"`
	Unchanged int                `json:"unchanged"`
}

type publishResult struct {
	URL     string `json:"url"`
	Deleted int    `json:"deleted"`
}

const uploadConcurrency = 4

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
		return strings.Compare(left.Key, right.Key)
	})
	return source, nil
}

// manifest describes every source file by size and MD5, which lets the
// platform skip files that are already published.
func manifest(source sourceDirectory) ([]hex.SiteFile, error) {
	files := make([]hex.SiteFile, 0, len(source.Files))
	for _, entry := range source.Files {
		size, digest, err := fileDigest(entry.Path)
		if err != nil {
			return nil, err
		}
		files = append(files, hex.SiteFile{Path: entry.Key, Size: size, MD5: digest})
	}
	return files, nil
}

func fileDigest(path string) (int64, string, error) {
	input, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer input.Close()

	hash := md5.New()
	size, err := io.Copy(hash, input)
	if err != nil {
		return 0, "", fmt.Errorf("read %s: %w", path, err)
	}
	return size, base64.StdEncoding.EncodeToString(hash.Sum(nil)), nil
}

// publish asks the platform for upload targets, uploads the changed files
// directly to them (index.html last), and completes the publication.
func (a *App) publish(ctx context.Context, project Project, name string) (publishResult, error) {
	var result publishResult
	source, err := readSource(a.Dir, project.Directory)
	if err != nil {
		return result, err
	}
	files, err := manifest(source)
	if err != nil {
		return result, err
	}

	request := publishRequest{
		Files: files,
		Metadata: &hex.SiteMetadata{
			Title:        project.Title,
			Description:  project.Description,
			Author:       project.Author,
			Discoverable: project.Discoverable,
		},
		Access: project.Access,
	}
	sitePath := "/api/hex/sites/" + name + "/publish"

	data, err := a.apiCall(ctx, project, http.MethodPost, sitePath, request)
	if err != nil {
		return result, publishError(err)
	}
	var plan publishPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return result, fmt.Errorf("unexpected publication plan: %w", err)
	}

	fmt.Fprintf(a.Err, "Uploading %d changed files (%d unchanged)\n", len(plan.Uploads), plan.Unchanged)
	paths := make(map[string]string, len(source.Files))
	for _, entry := range source.Files {
		paths[entry.Key] = entry.Path
	}
	if err := a.uploadAll(ctx, project, plan.Uploads, paths); err != nil {
		return result, err
	}

	data, err = a.apiCall(ctx, project, http.MethodPost, sitePath+"/complete", request)
	if err != nil {
		return result, publishError(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("unexpected publication result: %w", err)
	}
	return result, nil
}

func publishError(err error) error {
	var status *apiStatusError
	if errors.As(err, &status) && status.Status == http.StatusNotFound {
		return errors.New("this platform does not accept publications through its API; update the platform or ask its operators")
	}
	return err
}

// uploadAll sends every file except index.html in parallel, then index.html,
// so visitors never load a new page that references missing assets.
func (a *App) uploadAll(ctx context.Context, project Project, uploads []hex.UploadTarget, paths map[string]string) error {
	var index []hex.UploadTarget
	var assets []hex.UploadTarget
	for _, upload := range uploads {
		if _, ok := paths[upload.Path]; !ok {
			return fmt.Errorf("the platform requested an unknown file %q", upload.Path)
		}
		if upload.Path == "index.html" {
			index = append(index, upload)
		} else {
			assets = append(assets, upload)
		}
	}

	if err := a.uploadParallel(ctx, project, assets, paths); err != nil {
		return err
	}
	return a.uploadParallel(ctx, project, index, paths)
}

func (a *App) uploadParallel(ctx context.Context, project Project, uploads []hex.UploadTarget, paths map[string]string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	work := make(chan hex.UploadTarget)
	var failure error
	var once sync.Once
	var workers sync.WaitGroup
	for range min(uploadConcurrency, len(uploads)) {
		workers.Go(func() {
			for upload := range work {
				if err := a.upload(ctx, project, upload, paths[upload.Path]); err != nil {
					once.Do(func() {
						failure = fmt.Errorf("upload %s: %w", upload.Path, err)
						cancel()
					})
				}
			}
		})
	}

	for _, upload := range uploads {
		select {
		case work <- upload:
		case <-ctx.Done():
		}
	}
	close(work)
	workers.Wait()

	if failure != nil {
		return failure
	}
	return ctx.Err()
}

func (a *App) upload(ctx context.Context, project Project, target hex.UploadTarget, path string) error {
	switch target.Protocol {
	case "hex":
		return a.uploadThroughPlatform(ctx, project, target.URL, path)
	case "azure-files":
		return uploadToAzureFiles(ctx, target.URL, path)
	default:
		return fmt.Errorf("unsupported upload protocol %q; update the Hex CLI with hex update", target.Protocol)
	}
}

// uploadThroughPlatform sends a file to the platform's own upload endpoint.
// Only same-origin API paths are accepted, so the API token never leaves the
// platform.
func (a *App) uploadThroughPlatform(ctx context.Context, project Project, target, path string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/api/hex/sites/") {
		return fmt.Errorf("refusing platform upload to %q", target)
	}

	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}

	_, err = a.apiSend(ctx, project, http.MethodPut, parsed.RequestURI(), input, info.Size(), "application/octet-stream")
	return err
}

// uploadToAzureFiles writes a file through a pre-signed Azure Files URL. The
// URL carries its own authorization, so no Hex or Azure credential is sent.
func uploadToAzureFiles(ctx context.Context, target, path string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLocalHost(parsed.Hostname()))) {
		return errors.New("the platform returned an invalid storage upload URL")
	}

	// Azure Files requires the backup request intent for user delegation SAS
	// requests, as for other Entra-authorized ones.
	intent := file.ShareTokenIntentBackup
	client, err := file.NewClientWithNoCredential(target, &file.ClientOptions{FileRequestIntent: &intent})
	if err != nil {
		return err
	}
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}

	// UploadFile only writes ranges; the file must first exist at its size.
	if _, err := client.Create(ctx, info.Size(), nil); err != nil {
		return fmt.Errorf("Azure Files upload failed; the upload link may have expired, so run hex publish again: %w", err)
	}
	if err := client.UploadFile(ctx, input, nil); err != nil {
		return fmt.Errorf("Azure Files upload failed; the upload link may have expired, so run hex publish again: %w", err)
	}
	return nil
}

func (a *App) publishCommand() *cobra.Command {
	var profile, server, baseURL string
	command := &cobra.Command{
		Use:   "publish [site]",
		Short: "Publish the site through the platform",
		Long: "Publish the site through the platform. The platform checks that you own the site " +
			"(the first publisher of a new name becomes its owner), then files are uploaded " +
			"directly to storage. An access policy in hex.json is applied with the publication.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			if server != "" {
				project.Server = server
			}

			result, err := a.publish(cmd.Context(), project, name)
			if err != nil {
				return err
			}
			website := result.URL
			if baseURL != "" || website == "" {
				base := project.SiteBaseURL
				if base == "" {
					base = project.Server
				}
				if baseURL != "" {
					base = baseURL
				}
				website, err = siteURL(base, name)
				if err != nil {
					return fmt.Errorf("published %s, but its URL could not be derived: %w", name, err)
				}
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
		Short: "Unpublish a site you own",
		Long:  "Unpublish a site you own. Its access policy is kept, so the name stays reserved for its owners.",
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
			if _, err := a.apiCall(cmd.Context(), project, http.MethodDelete, "/api/hex/sites/"+name, nil); err != nil {
				return publishError(err)
			}
			fmt.Fprintln(a.Out, "Unpublished", name)
			return nil
		},
	}
	command.Flags().StringVar(&profile, "platform", "", "Saved platform profile")
	command.Flags().BoolVar(&confirmed, "yes", false, "Confirm unpublishing")
	return command
}
