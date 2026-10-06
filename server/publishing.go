package hex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// SiteFile describes one file of a published site. MD5 is the base64 Content-MD5
// of the file, supplied by the publisher.
type SiteFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	MD5  string `json:"md5,omitempty"`
}

// UploadTarget tells the publisher where to send one file. Protocol "hex"
// is a plain PUT of the file body to URL, authenticated like other API calls;
// "http" is a signed HTTP PUT with optional storage headers and no Hex
// credentials; "azure-files" accepts Azure Create File and Put Range requests.
type UploadTarget struct {
	Path     string            `json:"path"`
	Protocol string            `json:"protocol"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// SitePublisher writes published site files on behalf of authorized
// publishers. Paths are relative to the site's directory and use forward
// slashes. ListSiteFiles returns every regular file, including the
// server-owned dotfiles; sizes must be exact, MD5 is optional.
type SitePublisher interface {
	ListSiteFiles(ctx context.Context, site string) ([]SiteFile, error)
	ReadSiteFile(ctx context.Context, site, path string) (io.ReadCloser, error)
	WriteSiteFile(ctx context.Context, site, path string, size int64, source io.Reader) error
	DeleteSiteFile(ctx context.Context, site, path string) error
	DeleteSite(ctx context.Context, site string) error
}

// DirectUploader is implemented by publishers whose storage accepts uploads
// straight from the publisher, such as pre-signed URLs. Publishers without it
// receive uploads through the server.
type DirectUploader interface {
	UploadTargets(ctx context.Context, site string, files []SiteFile) ([]UploadTarget, error)
}

const (
	siteMetadataFile = ".hex-site.json"
	siteManifestFile = ".hex-manifest.json"
	siteHistoryFile  = ".hex-history.json"
	maxPublishFiles  = 20000
)

type publishRequest struct {
	Files                    []SiteFile    `json:"files"`
	Metadata                 *SiteMetadata `json:"metadata,omitempty"`
	Access                   *SiteAccess   `json:"access,omitempty"`
	SupportedUploadProtocols []string      `json:"supportedUploadProtocols,omitempty"`
	// Actions are the app's declared actions; omitting them removes
	// earlier ones.
	Actions []DeclaredAction `json:"actions,omitempty"`
	// Automations replace the site's automations; null leaves them
	// unchanged, so older publishers keep existing automations.
	Automations *[]Automation `json:"automations,omitempty"`
}

type publishPlan struct {
	Uploads   []UploadTarget `json:"uploads"`
	Unchanged int            `json:"unchanged"`
}

type publishResult struct {
	Name    string      `json:"name"`
	URL     string      `json:"url"`
	Deleted int         `json:"deleted"`
	Access  *SiteAccess `json:"access,omitempty"`
}

// startPublish authorizes a publication, claims unclaimed sites for the
// caller, and returns upload targets for the files that differ from the
// current publication.
func (s *Server) startPublish(w http.ResponseWriter, r *http.Request) {
	request, ok := s.readPublishRequest(w, r)
	if !ok {
		return
	}
	if request.Access != nil {
		if err := s.validateSiteAccess(*request.Access); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	site, identity, ok := s.publishCaller(w, r, true, request.Access)
	if !ok {
		return
	}
	if _, ok := s.applyPublishAccess(w, r, site, identity, request.Access); !ok {
		return
	}

	if err := s.removeConflictingFiles(r.Context(), site, request.Files); err != nil {
		writeServerError(w, err)
		return
	}

	changed, unchanged, err := s.changedFiles(r.Context(), site, request.Files)
	if err != nil {
		writeServerError(w, err)
		return
	}

	uploads, err := s.uploadTargets(r.Context(), site, changed, request.SupportedUploadProtocols)
	if err != nil {
		writeServerError(w, err)
		return
	}

	slog.Info("publication started", "site", site, "publisher", identityName(identity), "uploads", len(uploads), "unchanged", unchanged)
	writeJSON(w, http.StatusOK, publishPlan{Uploads: uploads, Unchanged: unchanged})
}

// uploadSiteFile receives one file for publishers without direct uploads.
func (s *Server) uploadSiteFile(w http.ResponseWriter, r *http.Request) {
	site, _, ok := s.publishCaller(w, r, false, nil)
	if !ok {
		return
	}

	path := r.PathValue("path")
	if !validKey(path) {
		writeError(w, http.StatusBadRequest, "invalid file path")
		return
	}
	if r.ContentLength < 0 || r.ContentLength > s.config.MaxPublishFileBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("files need a Content-Length of at most %d bytes", s.config.MaxPublishFileBytes))
		return
	}

	body := http.MaxBytesReader(w, r.Body, r.ContentLength)
	if err := s.config.Publisher.WriteSiteFile(r.Context(), site, path, r.ContentLength, body); err != nil {
		writeServerError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// completePublish verifies that every file of the manifest arrived, removes
// files that are no longer part of the site, and records the manifest,
// metadata and optional access policy.
func (s *Server) completePublish(w http.ResponseWriter, r *http.Request) {
	site, identity, ok := s.publishCaller(w, r, false, nil)
	if !ok {
		return
	}

	request, ok := s.readPublishRequest(w, r)
	if !ok {
		return
	}
	savedAccess, ok := s.applyPublishAccess(w, r, site, identity, request.Access)
	if !ok {
		return
	}

	current, err := s.config.Publisher.ListSiteFiles(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if missing := missingFiles(request.Files, current); len(missing) > 0 {
		writeError(w, http.StatusConflict, "files were not uploaded completely: "+strings.Join(missing, ", "))
		return
	}

	deleted, err := s.removeObsoleteFiles(r.Context(), site, request.Files, current)
	if err != nil {
		writeServerError(w, err)
		return
	}

	if err := s.writeSiteRecords(r.Context(), site, request, identity); err != nil {
		writeServerError(w, err)
		return
	}
	if err := s.writeDeclaredActions(r.Context(), site, request.Actions); err != nil {
		writeServerError(w, err)
		return
	}
	if request.Automations != nil {
		if err := s.replaceAutomations(r.Context(), site, *request.Automations, identity); err != nil {
			writeServerError(w, err)
			return
		}
	}

	result := publishResult{Name: site, Deleted: deleted, Access: savedAccess}
	if siteURL, err := s.siteURL(site); err == nil {
		result.URL = siteURL
	}
	slog.Info("publication completed", "site", site, "publisher", identityName(identity), "files", len(request.Files), "deleted", deleted)
	s.recordPublication(r.Context(), site)
	writeJSON(w, http.StatusOK, result)
}

// applyPublishAccess saves restrictions before touching served files or
// issuing direct-upload URLs. Interrupted uploads retain those restrictions.
func (s *Server) applyPublishAccess(w http.ResponseWriter, r *http.Request, site string, identity *Identity, requested *SiteAccess) (*SiteAccess, bool) {
	if requested == nil {
		return nil, true
	}
	if err := s.validateSiteAccess(*requested); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if s.config.Access == nil || identity == nil {
		writeError(w, http.StatusBadRequest, "publishing an access policy requires identity and access control")
		return nil, false
	}
	saved, status, err := s.replaceSiteAccess(r.Context(), identity, site, *requested)
	if err != nil {
		if status == 0 {
			writeServerError(w, err)
		} else {
			writeError(w, status, err.Error())
		}
		return nil, false
	}
	return &saved, true
}

// unpublishSite deletes the site's files. Its access policy is kept, so the
// name stays reserved for its owners.
func (s *Server) unpublishSite(w http.ResponseWriter, r *http.Request) {
	site, identity, ok := s.publishCaller(w, r, false, nil)
	if !ok {
		return
	}

	if err := s.removePublishedSite(r.Context(), site, identity); err != nil {
		writeServerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removePublishedSite is shared by the API and portal after authorization.
func (s *Server) removePublishedSite(ctx context.Context, site string, identity *Identity) error {
	if err := s.recoverPublicationHistory(ctx, site); err != nil {
		return err
	}
	// Stop future scheduling before deleting assets. Failed deletion is
	// retryable; deleted assets must never leave a future schedule behind.
	if err := s.replaceAutomations(ctx, site, nil, identity); err != nil {
		return err
	}
	if err := s.config.Publisher.DeleteSite(ctx, site); err != nil {
		return err
	}
	slog.Info("site unpublished", "site", site, "publisher", identityName(identity))
	s.recordAnalyticsEvents(ctx, []SiteEvent{analyticsEvent(site, "unpublished", time.Now().UTC(), "", personOf(identity))})
	return nil
}

// publishCaller validates the site name and authorizes the caller to
// publish it. Owners may always publish; with claim set, a caller allowed to
// create sites also claims an unowned name and becomes its owner.
func (s *Server) publishCaller(w http.ResponseWriter, r *http.Request, claim bool, initialAccess *SiteAccess) (string, *Identity, bool) {
	site := r.PathValue("site")
	if !siteNamePattern.MatchString(site) {
		writeError(w, http.StatusBadRequest, "site names are 1–63 lowercase letters, digits or hyphens")
		return "", nil, false
	}

	identity := s.requestIdentity(r)
	if s.config.Identity == nil {
		return site, nil, true
	}
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return "", nil, false
	}
	if s.config.Access == nil {
		if !s.canCreateSites(identity) {
			writeError(w, http.StatusForbidden, "you are not allowed to publish on this platform")
			return "", nil, false
		}
		return site, identity, true
	}

	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return "", nil, false
	}
	if exists {
		if s.siteRole(identity, access, true) != roleOwner {
			writeError(w, http.StatusForbidden, "only the site's owners can publish it")
			return "", nil, false
		}
		return site, identity, true
	}

	if !claim {
		writeError(w, http.StatusForbidden, "this site has no owner yet; start a publication to claim it")
		return "", nil, false
	}
	if !s.canCreateSites(identity) {
		writeError(w, http.StatusForbidden, "you are not allowed to create sites on this platform")
		return "", nil, false
	}
	requested := SiteAccess{}
	if initialAccess != nil {
		requested = *initialAccess
	}
	if _, status, err := s.replaceSiteAccess(r.Context(), identity, site, requested); err != nil {
		if status == 0 {
			writeServerError(w, err)
		} else {
			writeError(w, status, err.Error())
		}
		return "", nil, false
	}
	return site, identity, true
}

func (s *Server) readPublishRequest(w http.ResponseWriter, r *http.Request) (publishRequest, bool) {
	var request publishRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "publication manifests are limited to 16 MiB")
		return request, false
	}
	if err := json.Unmarshal(data, &request); err != nil {
		writeError(w, http.StatusBadRequest, "expected a JSON publication manifest: "+err.Error())
		return request, false
	}
	if err := s.validateManifest(request.Files); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return request, false
	}
	if err := s.validateDeclaredActions(r.PathValue("site"), request.Actions); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return request, false
	}
	if request.Automations != nil {
		if err := ValidateAutomations(*request.Automations); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return request, false
		}
		if len(*request.Automations) > 0 && !s.automationsEnabled() {
			writeError(w, http.StatusBadRequest, "this platform cannot run automations")
			return request, false
		}
	}
	return request, true
}

func (s *Server) validateManifest(files []SiteFile) error {
	if len(files) > maxPublishFiles {
		return fmt.Errorf("a site can contain at most %d files", maxPublishFiles)
	}

	paths := make(map[string]bool, len(files))
	var total int64
	for _, file := range files {
		if !validKey(file.Path) {
			return fmt.Errorf("invalid file path %q; paths are relative and cannot contain dot-prefixed segments", file.Path)
		}
		if paths[file.Path] {
			return fmt.Errorf("duplicate file path %q", file.Path)
		}
		paths[file.Path] = true

		if file.Size < 0 || file.Size > s.config.MaxPublishFileBytes {
			return fmt.Errorf("%s: files are limited to %d bytes", file.Path, s.config.MaxPublishFileBytes)
		}
		total += file.Size
		if digest, err := base64.StdEncoding.DecodeString(file.MD5); err != nil || len(digest) != 16 {
			return fmt.Errorf("%s: md5 must be the base64 MD5 digest of the file", file.Path)
		}
	}
	if total > s.config.MaxPublishBytes {
		return fmt.Errorf("sites are limited to %d bytes in total", s.config.MaxPublishBytes)
	}
	if !paths["index.html"] {
		return errors.New("a site must contain index.html")
	}
	for path := range paths {
		for parent := parentPath(path); parent != ""; parent = parentPath(parent) {
			if paths[parent] {
				return fmt.Errorf("%s is both a file and a directory", parent)
			}
		}
	}
	return nil
}

func parentPath(path string) string {
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return ""
	}
	return path[:index]
}

// changedFiles compares the manifest with the recorded manifest of the
// current publication and the files actually present, returning the files
// that need uploading, with index.html last.
func (s *Server) changedFiles(ctx context.Context, site string, files []SiteFile) ([]SiteFile, int, error) {
	previous, err := s.readManifest(ctx, site)
	if err != nil {
		return nil, 0, err
	}
	current, err := s.config.Publisher.ListSiteFiles(ctx, site)
	if err != nil {
		return nil, 0, err
	}
	sizes := make(map[string]int64, len(current))
	for _, file := range current {
		sizes[file.Path] = file.Size
	}

	var changed []SiteFile
	unchanged := 0
	for _, file := range files {
		recorded, known := previous[file.Path]
		size, present := sizes[file.Path]
		if known && present && recorded.MD5 == file.MD5 && recorded.Size == file.Size && size == file.Size {
			unchanged++
			continue
		}
		changed = append(changed, file)
	}

	slices.SortStableFunc(changed, func(left, right SiteFile) int {
		switch {
		case left.Path == "index.html":
			return 1
		case right.Path == "index.html":
			return -1
		default:
			return strings.Compare(left.Path, right.Path)
		}
	})
	return changed, unchanged, nil
}

func (s *Server) uploadTargets(ctx context.Context, site string, files []SiteFile, supported []string) ([]UploadTarget, error) {
	// Clients predating negotiation understand the original two transports.
	if supported == nil {
		supported = []string{"hex", "azure-files"}
	}
	if uploader, ok := s.config.Publisher.(DirectUploader); ok {
		targets, err := uploader.UploadTargets(ctx, site, files)
		if err != nil {
			return nil, err
		}
		for i, target := range targets {
			if !slices.Contains(supported, target.Protocol) {
				if !slices.Contains(supported, "hex") {
					return nil, fmt.Errorf("no supported upload transport for %s", target.Path)
				}
				targets[i] = hexUploadTarget(site, target.Path)
			}
		}
		return targets, nil
	}
	if len(files) > 0 && !slices.Contains(supported, "hex") {
		return nil, fmt.Errorf("this publisher requires the hex upload transport")
	}

	targets := make([]UploadTarget, 0, len(files))
	for _, file := range files {
		targets = append(targets, hexUploadTarget(site, file.Path))
	}
	return targets, nil
}

func hexUploadTarget(site, path string) UploadTarget {
	return UploadTarget{Path: path, Protocol: "hex", URL: "/api/hex/sites/" + site + "/publish/files/" + escapePath(path)}
}

func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func missingFiles(files, current []SiteFile) []string {
	sizes := make(map[string]int64, len(current))
	for _, file := range current {
		sizes[file.Path] = file.Size
	}

	var missing []string
	for _, file := range files {
		if size, ok := sizes[file.Path]; !ok || size != file.Size {
			missing = append(missing, file.Path)
			if len(missing) == 20 {
				break
			}
		}
	}
	return missing
}

// removeConflictingFiles deletes current files that would block the new
// layout: a file where the manifest needs a directory, or files inside a
// directory the manifest replaces with a file. They cannot be kept until the
// publication completes, because uploads would fail on them.
func (s *Server) removeConflictingFiles(ctx context.Context, site string, files []SiteFile) error {
	current, err := s.config.Publisher.ListSiteFiles(ctx, site)
	if err != nil {
		return err
	}

	wanted := make(map[string]bool, len(files))
	directories := make(map[string]bool)
	for _, file := range files {
		wanted[file.Path] = true
		for parent := parentPath(file.Path); parent != ""; parent = parentPath(parent) {
			directories[parent] = true
		}
	}

	for _, file := range current {
		conflict := directories[file.Path]
		for parent := parentPath(file.Path); parent != "" && !conflict; parent = parentPath(parent) {
			conflict = wanted[parent]
		}
		if !conflict {
			continue
		}
		if err := s.config.Publisher.DeleteSiteFile(ctx, site, file.Path); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("remove conflicting file %s: %w", file.Path, err)
		}
	}
	return nil
}

func (s *Server) removeObsoleteFiles(ctx context.Context, site string, files, current []SiteFile) (int, error) {
	keep := make(map[string]bool, len(files)+2)
	for _, file := range files {
		keep[file.Path] = true
	}
	keep[siteMetadataFile] = true
	keep[siteManifestFile] = true
	keep[siteHistoryFile] = true
	keep[siteActionsFile] = true

	deleted := 0
	for _, file := range current {
		if keep[file.Path] {
			continue
		}
		if err := s.config.Publisher.DeleteSiteFile(ctx, site, file.Path); err != nil && !errors.Is(err, ErrNotFound) {
			return deleted, fmt.Errorf("remove obsolete file %s: %w", file.Path, err)
		}
		deleted++
	}
	return deleted, nil
}

// writeSiteRecords stores the manifest, the metadata and the publication
// history. The creator, kind and creation time carry over from earlier
// publications; the publisher is the caller.
func (s *Server) writeSiteRecords(ctx context.Context, site string, request publishRequest, identity *Identity) error {
	manifest, err := json.Marshal(request.Files)
	if err != nil {
		return err
	}
	if err := s.writeRecord(ctx, site, siteManifestFile, manifest); err != nil {
		return err
	}

	now := time.Now().UTC()
	previous := s.readSiteMetadata(ctx, site)
	metadata := SiteMetadata{}
	if request.Metadata != nil {
		metadata = SiteMetadata{
			Title:        request.Metadata.Title,
			Description:  request.Metadata.Description,
			Author:       request.Metadata.Author,
			Discoverable: request.Metadata.Discoverable,
		}
	}
	metadata.PublishedAt = now
	metadata.PublishedBy = personOf(identity)
	metadata.Kind = previous.Kind
	if metadata.Kind == KindArtifact && metadata.Title == "" {
		// Artifacts are updated without a project file; keep their title.
		metadata.Title = previous.Title
	}
	metadata.CreatedAt = previous.CreatedAt
	metadata.CreatedBy = previous.CreatedBy
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = now
		metadata.CreatedBy = metadata.PublishedBy
	}
	if err := s.writeMetadata(ctx, site, metadata); err != nil {
		return err
	}

	var size int64
	for _, file := range request.Files {
		size += file.Size
	}
	return s.appendHistory(ctx, site, Publication{PublishedAt: now, PublishedBy: metadata.PublishedBy, Files: len(request.Files), Bytes: size})
}

func (s *Server) writeMetadata(ctx context.Context, site string, metadata SiteMetadata) error {
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	if len(encoded) > 64<<10 {
		return errors.New("site metadata exceeds 64 KiB")
	}
	return s.writeRecord(ctx, site, siteMetadataFile, encoded)
}

// readSiteMetadata returns the site's recorded metadata, or empty metadata
// for new sites and unreadable records.
func (s *Server) readSiteMetadata(ctx context.Context, site string) SiteMetadata {
	var metadata SiteMetadata
	if err := s.readRecord(ctx, site, siteMetadataFile, &metadata); err != nil {
		slog.Warn("ignoring unreadable site metadata", "site", site, "error", err)
	}
	return metadata
}

// Publication is one entry in a site's publication history.
type Publication struct {
	PublishedAt time.Time `json:"publishedAt"`
	PublishedBy *Person   `json:"publishedBy,omitempty"`
	Files       int       `json:"files"`
	Bytes       int64     `json:"bytes"`
}

const maxHistory = 50

func (s *Server) appendHistory(ctx context.Context, site string, publication Publication) error {
	history := []Publication{}
	if err := s.readRecord(ctx, site, siteHistoryFile, &history); err != nil {
		slog.Warn("starting a new publication history", "site", site, "error", err)
	}
	history = append([]Publication{publication}, history...)
	if len(history) > maxHistory {
		history = history[:maxHistory]
	}

	encoded, err := json.Marshal(history)
	if err != nil {
		return err
	}
	return s.writeRecord(ctx, site, siteHistoryFile, encoded)
}

// readRecord decodes one of the server-owned records; a missing record
// leaves value unchanged.
func (s *Server) readRecord(ctx context.Context, site, path string, value any) error {
	reader, err := s.config.Publisher.ReadSiteFile(ctx, site, path)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	defer closeReader(reader, path)
	return json.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(value)
}

// siteHistory lists who published the site and when, newest first. Only
// owners and admins may read it.
func (s *Server) siteHistory(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.accessCaller(w, r)
	if !ok {
		return
	}
	site := r.PathValue("site")
	access, exists, err := s.sitePolicy(r.Context(), site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if s.siteRole(identity, access, exists) != roleOwner {
		writeError(w, http.StatusForbidden, "only site owners can read the publication history")
		return
	}

	history := []Publication{}
	if err := s.readRecord(r.Context(), site, siteHistoryFile, &history); err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
}

func (s *Server) writeRecord(ctx context.Context, site, path string, data []byte) error {
	if err := s.config.Publisher.WriteSiteFile(ctx, site, path, int64(len(data)), strings.NewReader(string(data))); err != nil {
		return fmt.Errorf("write %s for %s: %w", path, site, err)
	}
	return nil
}

func (s *Server) readManifest(ctx context.Context, site string) (map[string]SiteFile, error) {
	reader, err := s.config.Publisher.ReadSiteFile(ctx, site, siteManifestFile)
	if errors.Is(err, ErrNotFound) {
		return map[string]SiteFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer closeReader(reader, siteManifestFile)

	var files []SiteFile
	if err := json.NewDecoder(io.LimitReader(reader, 16<<20)).Decode(&files); err != nil {
		// An unreadable manifest only costs a full upload.
		slog.Warn("ignoring unreadable site manifest", "site", site, "error", err)
		return map[string]SiteFile{}, nil
	}
	manifest := make(map[string]SiteFile, len(files))
	for _, file := range files {
		manifest[file.Path] = file
	}
	return manifest, nil
}

func (s *Server) siteURL(site string) (string, error) {
	baseURL, err := parseSiteBaseURL(s.config.SiteBaseURL)
	if err != nil {
		return "", err
	}
	siteURL := *baseURL
	siteURL.Host = site + "." + baseURL.Host
	siteURL.Path = "/"
	return siteURL.String(), nil
}

func identityName(identity *Identity) string {
	if identity == nil {
		return ""
	}
	if identity.Name != "" {
		return identity.Name
	}
	return identity.ID
}
