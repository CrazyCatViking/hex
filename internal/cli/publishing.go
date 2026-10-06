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
	Actions  json.RawMessage   `json:"actions,omitempty"`
	// Automations is omitted, not empty, for projects without any, so the
	// platform keeps automations deployed through the API.
	Automations              *[]hex.Automation `json:"automations,omitempty"`
	SupportedUploadProtocols []string          `json:"supportedUploadProtocols"`
}

type uploadTarget struct {
	hex.UploadTarget
	Headers map[string]string `json:"headers,omitempty"`
}

type publishPlan struct {
	Uploads   []uploadTarget `json:"uploads"`
	Unchanged int            `json:"unchanged"`
}

type publishResult struct {
	URL string `json:"url"`
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
	source.Files, err = collectFiles(source.Directory, func(path string, entry fs.DirEntry) bool {
		if source.Directory != project || filepath.Dir(path) != project {
			return false
		}
		if entry.IsDir() {
			return entry.Name() == automationsDirectory
		}
		return rootProjectFile(entry.Name())
	})
	if err != nil {
		return source, err
	}
	if !slices.ContainsFunc(source.Files, func(file sourceFile) bool { return file.Key == "index.html" }) {
		return source, errors.New("publish directory must contain index.html")
	}
	slices.SortFunc(source.Files, func(left, right sourceFile) int {
		return strings.Compare(left.Key, right.Key)
	})
	return source, nil
}

// collectFiles lists the regular files under directory with their
// forward-slash keys. Hidden files and node_modules are left out, as is
// anything skip selects; symlinks and special files are refused.
func collectFiles(directory string, skip func(path string, entry fs.DirEntry) bool) ([]sourceFile, error) {
	var files []sourceFile
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if path == directory {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") || strings.EqualFold(entry.Name(), "node_modules") || (skip != nil && skip(path, entry)) {
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
		key, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		files = append(files, sourceFile{Key: filepath.ToSlash(key), Path: path})
		return nil
	})
	return files, err
}

// manifest describes every source file by size and MD5, which lets the
// platform skip files that are already published.
func manifest(sources []sourceFile) ([]hex.SiteFile, error) {
	files := make([]hex.SiteFile, 0, len(sources))
	for _, entry := range sources {
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

// publish publishes the project in directory with the metadata and access
// policy from its hex.json.
func (a *App) publish(ctx context.Context, project Project, directory, name string, assumeYes bool) (publishResult, error) {
	source, err := readSource(directory, project.Directory)
	if err != nil {
		return publishResult{}, err
	}
	if err := a.checkPublication(ctx, source.Files, nil, assumeYes); err != nil {
		return publishResult{}, err
	}
	project.automations, err = projectAutomations(directory, project)
	if err != nil {
		return publishResult{}, err
	}
	metadata := &hex.SiteMetadata{
		Title:        project.Title,
		Description:  project.Description,
		Author:       project.Author,
		Discoverable: project.Discoverable,
	}
	return a.publishFiles(ctx, project, name, source.Files, metadata, project.Access)
}

// publishFiles asks the platform for upload targets, uploads the changed
// files directly to them (index.html last), and completes the publication.
func (a *App) publishFiles(ctx context.Context, project Project, name string, sources []sourceFile, metadata *hex.SiteMetadata, access json.RawMessage) (publishResult, error) {
	var result publishResult
	files, err := manifest(sources)
	if err != nil {
		return result, err
	}

	request := publishRequest{
		Files: files, Metadata: metadata, Access: access,
		Actions: project.Actions, Automations: project.automations,
		SupportedUploadProtocols: []string{"hex", "http", "azure-files"},
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
	paths := make(map[string]string, len(sources))
	for _, entry := range sources {
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
func (a *App) uploadAll(ctx context.Context, project Project, uploads []uploadTarget, paths map[string]string) error {
	var index []uploadTarget
	var assets []uploadTarget
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

func (a *App) uploadParallel(ctx context.Context, project Project, uploads []uploadTarget, paths map[string]string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	work := make(chan uploadTarget)
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

func (a *App) upload(ctx context.Context, project Project, target uploadTarget, path string) error {
	switch target.Protocol {
	case "hex":
		return a.uploadThroughPlatform(ctx, project, target.URL, path)
	case "azure-files":
		return uploadToAzureFiles(ctx, target.URL, path)
	case "http":
		return a.uploadToHTTP(ctx, target.URL, target.Headers, path)
	default:
		return fmt.Errorf("unsupported upload protocol %q; update the Hex CLI with hex update", target.Protocol)
	}
}

// uploadToHTTP sends a plain PUT to a signed storage URL using only the
// headers supplied by the storage provider, without platform credentials.
func (a *App) uploadToHTTP(ctx context.Context, target string, headers map[string]string, path string) error {
	parsed, err := url.Parse(target)
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLocalHost(parsed.Hostname()))) {
		return errors.New("the platform returned an invalid storage upload URL")
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target, input)
	if err != nil {
		return err
	}
	request.ContentLength = info.Size()
	if info.Size() == 0 {
		request.Body = http.NoBody
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	client := *a.HTTP
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("storage upload failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("storage upload failed (HTTP %d); the upload link may have expired, so run hex publish again", response.StatusCode)
	}
	return nil
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
	var profile, server, baseURL, name, update string
	var with []string
	var assumeYes bool
	command := &cobra.Command{
		Use:   "publish [path]",
		Short: "Publish a project, file or folder",
		Long: "Publish through the platform. A folder with hex.json (by default the current " +
			"folder) is published as that project's site: the first publisher of a name owns it, " +
			"and the access policy in hex.json is applied. Any other file or folder is published " +
			"at a new private link with a random name, titled with its file or folder name or " +
			"--name; --with shares it and --update replaces an earlier one. Publications that " +
			"include dependency folders, repositories or files that look like secrets are " +
			"refused, and unusually large ones need confirmation.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := a.Dir
			if len(args) > 0 {
				target = resolvePath(a.Dir, args[0])
			}
			isProject, err := projectDirectory(target)
			if err != nil {
				return err
			}

			if isProject {
				if name != "" || update != "" || len(with) > 0 {
					return errors.New("--name, --update and --with are for files and folders without hex.json; a project's name and access come from its hex.json")
				}
				return a.publishProject(cmd, target, profile, server, baseURL, assumeYes)
			}
			if len(args) == 0 {
				return errors.New("no hex.json here: run hex init to create a project, or name a file or folder to publish, such as hex publish ./report.pdf")
			}
			if err := validateUpload(update, with); err != nil {
				return err
			}
			project, err := a.commandConfig(profile, false)
			if err != nil {
				return err
			}
			if server != "" {
				project.Server = server
			}
			return a.publishPath(cmd.Context(), project, target, name, update, with, assumeYes)
		},
	}
	flags := command.Flags()
	flags.StringVarP(&name, "name", "n", "", "Title for a file or folder (defaults to its name)")
	flags.StringArrayVar(&with, "with", nil, "Share a file or folder with user:<email or id> or group:<object id> (repeatable)")
	flags.StringVar(&update, "update", "", "Replace the content of a file or folder published earlier, by its site name")
	flags.BoolVarP(&assumeYes, "yes", "y", false, "Publish unusually large content without asking")
	flags.StringVar(&profile, "platform", "", "Saved platform profile")
	flags.StringVar(&server, "server", "", "Override the platform origin")
	flags.StringVar(&baseURL, "site-base-url", "", "Override the parent site origin")
	return command
}

// projectDirectory reports whether target is a folder with a hex.json.
func projectDirectory(target string) (bool, error) {
	info, err := os.Stat(target)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	_, err = os.Stat(filepath.Join(target, "hex.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (a *App) publishProject(cmd *cobra.Command, directory, profile, server, baseURL string, assumeYes bool) error {
	project, err := a.commandConfigIn(directory, profile, true)
	if err != nil {
		return err
	}
	if err := validateSiteName(project.Name); err != nil {
		return err
	}
	if server != "" {
		project.Server = server
	}

	result, err := a.publish(cmd.Context(), project, directory, project.Name, assumeYes)
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
		website, err = siteURL(base, project.Name)
		if err != nil {
			return fmt.Errorf("published %s, but its URL could not be derived: %w", project.Name, err)
		}
	}
	fmt.Fprintln(a.Out, website)
	return nil
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
			// A named site, such as an artifact, needs no hex.json.
			project, err := a.commandConfig(profile, len(args) == 0)
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
