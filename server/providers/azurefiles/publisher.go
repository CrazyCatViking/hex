// Package azurefiles publishes sites to an Azure Files share. The server
// writes with its own Entra identity (which needs Storage File Data
// Privileged Contributor on the storage account) and hands publishers
// short-lived user delegation SAS URLs, one per file, so file contents go
// straight from the publisher to storage without an account key.
package azurefiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/directory"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/file"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/fileerror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/service"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/share"
	hex "github.com/crazycatviking/hex/server"
)

const (
	// sitesRoot is where published sites live inside the share, matching
	// the layout the mounted share is served from.
	sitesRoot = "public/sites"

	uploadValidity     = time.Hour
	delegationValidity = 12 * time.Hour
	rangeSize          = 4 << 20
	keyTimeFormat      = "2006-01-02T15:04:05Z"
)

type Publisher struct {
	share      *share.Client
	shareName  string
	account    string
	serviceURL string
	credential azcore.TokenCredential
	http       *http.Client

	mu              sync.Mutex
	delegation      delegationKey
	delegationRenew time.Time
}

// New connects to the share at shareURL, such as
// https://account.file.core.windows.net/sites.
func New(shareURL string, credential azcore.TokenCredential) (*Publisher, error) {
	parsed, err := url.Parse(shareURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" {
		return nil, errors.New("the Azure Files share URL must be an HTTPS URL without credentials or query parameters")
	}
	shareName := strings.Trim(parsed.Path, "/")
	if shareName == "" || strings.Contains(shareName, "/") {
		return nil, errors.New("the Azure Files share URL must name exactly one share")
	}

	intent := service.ShareTokenIntentBackup
	client, err := service.NewClient("https://"+parsed.Host+"/", credential, &service.ClientOptions{FileRequestIntent: &intent})
	if err != nil {
		return nil, fmt.Errorf("create Azure Files client: %w", err)
	}

	return &Publisher{
		share:      client.NewShareClient(shareName),
		shareName:  shareName,
		account:    strings.SplitN(parsed.Host, ".", 2)[0],
		serviceURL: "https://" + parsed.Host + "/",
		credential: credential,
		http:       &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (p *Publisher) ListSiteFiles(ctx context.Context, site string) ([]hex.SiteFile, error) {
	files := []hex.SiteFile{}
	err := p.walk(ctx, site, func(relative string, size int64) {
		files = append(files, hex.SiteFile{Path: relative, Size: size})
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("list files of site %s: %w", site, err)
	}
	return files, nil
}

func (p *Publisher) ReadSiteFile(ctx context.Context, site, name string) (io.ReadCloser, error) {
	client, err := p.fileClient(site, name)
	if err != nil {
		return nil, err
	}

	response, err := client.DownloadStream(ctx, nil)
	if missing(err) {
		return nil, hex.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", site, name, err)
	}
	return response.Body, nil
}

// WriteSiteFile creates the file and streams it in 4 MiB ranges, keeping
// memory bounded for server-side writes.
func (p *Publisher) WriteSiteFile(ctx context.Context, site, name string, size int64, source io.Reader) error {
	client, err := p.fileClient(site, name)
	if err != nil {
		return err
	}
	if err := p.ensureDirectories(ctx, site, []string{parent(name)}); err != nil {
		return err
	}

	if _, err := client.Create(ctx, size, nil); err != nil {
		return fmt.Errorf("create %s/%s: %w", site, name, err)
	}

	buffer := make([]byte, rangeSize)
	var offset int64
	for offset < size {
		chunk := min(int64(rangeSize), size-offset)
		if _, err := io.ReadFull(source, buffer[:chunk]); err != nil {
			return fmt.Errorf("read upload of %s/%s: %w", site, name, err)
		}
		body := streaming.NopCloser(bytes.NewReader(buffer[:chunk]))
		if _, err := client.UploadRange(ctx, offset, body, nil); err != nil {
			return fmt.Errorf("upload %s/%s: %w", site, name, err)
		}
		offset += chunk
	}
	return nil
}

// DeleteSiteFile removes the file and any directories it leaves empty.
func (p *Publisher) DeleteSiteFile(ctx context.Context, site, name string) error {
	client, err := p.fileClient(site, name)
	if err != nil {
		return err
	}

	_, err = client.Delete(ctx, nil)
	if missing(err) {
		return hex.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete %s/%s: %w", site, name, err)
	}

	for directoryPath := parent(name); directoryPath != ""; directoryPath = parent(directoryPath) {
		_, err := p.directoryClient(p.sitePath(site, directoryPath)).Delete(ctx, nil)
		if err != nil {
			break
		}
	}
	return nil
}

func (p *Publisher) DeleteSite(ctx context.Context, site string) error {
	if err := validSite(site); err != nil {
		return err
	}

	var files, directories []string
	err := p.walk(ctx, site, func(relative string, _ int64) {
		files = append(files, relative)
	}, func(relative string) {
		directories = append(directories, relative)
	})
	if err != nil {
		return fmt.Errorf("list site %s for deletion: %w", site, err)
	}

	for _, name := range files {
		client, err := p.fileClient(site, name)
		if err != nil {
			return err
		}
		if _, err := client.Delete(ctx, nil); err != nil && !missing(err) {
			return fmt.Errorf("delete %s/%s: %w", site, name, err)
		}
	}

	// Deepest directories first, then the site directory itself.
	slices.SortFunc(directories, func(left, right string) int {
		return strings.Count(right, "/") - strings.Count(left, "/")
	})
	for _, directoryPath := range append(directories, "") {
		_, err := p.directoryClient(p.sitePath(site, directoryPath)).Delete(ctx, nil)
		if err != nil && !missing(err) {
			return fmt.Errorf("delete directory %s/%s: %w", site, directoryPath, err)
		}
	}
	return nil
}

// UploadTargets creates the directories the files need and signs one
// create-and-write SAS per file, valid for an hour.
func (p *Publisher) UploadTargets(ctx context.Context, site string, files []hex.SiteFile) ([]hex.UploadTarget, error) {
	if len(files) == 0 {
		return []hex.UploadTarget{}, nil
	}

	directories := make([]string, 0, len(files))
	for _, siteFile := range files {
		directories = append(directories, parent(siteFile.Path))
	}
	if err := p.ensureDirectories(ctx, site, directories); err != nil {
		return nil, err
	}

	key, err := p.userDelegation(ctx)
	if err != nil {
		return nil, err
	}

	expiry := time.Now().UTC().Add(uploadValidity)
	targets := make([]hex.UploadTarget, 0, len(files))
	for _, siteFile := range files {
		client, err := p.fileClient(site, siteFile.Path)
		if err != nil {
			return nil, err
		}

		query, err := fileSAS(key, p.account, p.shareName, p.sitePath(site, siteFile.Path), "cw", expiry)
		if err != nil {
			return nil, fmt.Errorf("sign upload for %s/%s: %w", site, siteFile.Path, err)
		}

		targets = append(targets, hex.UploadTarget{
			Path:     siteFile.Path,
			Protocol: "azure-files",
			URL:      client.URL() + "?" + query,
		})
	}
	return targets, nil
}

// userDelegation returns a cached user delegation key, renewing it well
// before any SAS signed with it could outlive it.
func (p *Publisher) userDelegation(ctx context.Context) (delegationKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now().UTC()
	if p.delegation.Value != "" && now.Before(p.delegationRenew) {
		return p.delegation, nil
	}

	key, err := fetchDelegationKey(ctx, p.http, p.credential, p.serviceURL, now.Add(-5*time.Minute), now.Add(delegationValidity))
	if err != nil {
		return delegationKey{}, fmt.Errorf("obtain Azure Files user delegation key: %w", err)
	}

	p.delegation = key
	p.delegationRenew = now.Add(delegationValidity - 2*uploadValidity)
	return key, nil
}

// ensureDirectories creates the sites root, the site directory and every
// listed directory (relative to the site) including their parents.
func (p *Publisher) ensureDirectories(ctx context.Context, site string, relatives []string) error {
	if err := validSite(site); err != nil {
		return err
	}

	needed := map[string]bool{"public": true, sitesRoot: true, p.sitePath(site, ""): true}
	for _, relative := range relatives {
		for directoryPath := relative; directoryPath != ""; directoryPath = parent(directoryPath) {
			needed[p.sitePath(site, directoryPath)] = true
		}
	}

	ordered := slices.Collect(func(yield func(string) bool) {
		for directoryPath := range needed {
			if !yield(directoryPath) {
				return
			}
		}
	})
	slices.SortFunc(ordered, func(left, right string) int {
		if depth := strings.Count(left, "/") - strings.Count(right, "/"); depth != 0 {
			return depth
		}
		return strings.Compare(left, right)
	})

	for _, directoryPath := range ordered {
		_, err := p.directoryClient(directoryPath).Create(ctx, nil)
		if err != nil && !fileerror.HasCode(err, fileerror.ResourceAlreadyExists) {
			return fmt.Errorf("create directory %s: %w", directoryPath, err)
		}
	}
	return nil
}

// walk visits the files and subdirectories of a site, breadth first, with
// paths relative to the site. A site that does not exist has no entries.
func (p *Publisher) walk(ctx context.Context, site string, visitFile func(string, int64), visitDirectory func(string)) error {
	if err := validSite(site); err != nil {
		return err
	}

	pending := []string{""}
	for len(pending) > 0 {
		relative := pending[0]
		pending = pending[1:]

		pager := p.directoryClient(p.sitePath(site, relative)).NewListFilesAndDirectoriesPager(nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if missing(err) {
				break
			}
			if err != nil {
				return err
			}

			for _, entry := range page.Segment.Files {
				if entry.Name == nil || entry.Properties == nil || entry.Properties.ContentLength == nil {
					continue
				}
				visitFile(path.Join(relative, *entry.Name), *entry.Properties.ContentLength)
			}
			for _, entry := range page.Segment.Directories {
				if entry.Name == nil {
					continue
				}
				child := path.Join(relative, *entry.Name)
				if visitDirectory != nil {
					visitDirectory(child)
				}
				pending = append(pending, child)
			}
		}
	}
	return nil
}

func (p *Publisher) sitePath(site, relative string) string {
	return path.Join(sitesRoot, site, relative)
}

// directoryClient addresses a directory one segment at a time; the SDK
// escapes slashes in a single directory name.
func (p *Publisher) directoryClient(directoryPath string) *directory.Client {
	client := p.share.NewRootDirectoryClient()
	for _, segment := range strings.Split(directoryPath, "/") {
		if segment != "" {
			client = client.NewSubdirectoryClient(segment)
		}
	}
	return client
}

func (p *Publisher) fileClient(site, name string) (*file.Client, error) {
	if err := validSite(site); err != nil {
		return nil, err
	}
	cleaned := path.Clean(name)
	if name == "" || cleaned != name || path.IsAbs(name) || cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return nil, fmt.Errorf("invalid site file path %q", name)
	}
	return p.directoryClient(p.sitePath(site, parent(name))).NewFileClient(path.Base(name)), nil
}

func validSite(site string) error {
	if site == "" || site == "." || site == ".." || strings.ContainsAny(site, `/\`) {
		return fmt.Errorf("invalid site name %q", site)
	}
	return nil
}

func parent(name string) string {
	directoryPath := path.Dir(name)
	if directoryPath == "." {
		return ""
	}
	return directoryPath
}

func missing(err error) bool {
	return err != nil && fileerror.HasCode(err, fileerror.ResourceNotFound, fileerror.ParentNotFound, fileerror.ShareNotFound)
}
