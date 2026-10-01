package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	hex "github.com/crazycatviking/hex/server"
)

// Files, and folders without hex.json, are published to a random, private
// site ("artifact") that the creator shares by adding viewers.

type artifact struct {
	Name string `json:"name"`
}

const maxInlineText = 1 << 20

func validateUpload(update string, with []string) error {
	for _, principal := range with {
		if kind, _, typed := strings.Cut(principal, ":"); !typed || (kind != "user" && kind != "group" && kind != "role") {
			return fmt.Errorf("--with %q needs a user:, group: or role: prefix", principal)
		}
	}
	if update != "" {
		return validateSiteName(update)
	}
	return nil
}

// publishPath publishes a file, or a folder without hex.json, at a private
// link with a random name, or replaces the content of one published earlier.
func (a *App) publishPath(ctx context.Context, project Project, target, title, update string, with []string, assumeYes bool) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	var forbidden []string
	if info.IsDir() {
		if forbidden, err = findForbidden(target); err != nil {
			return err
		}
	}

	temporary, err := os.MkdirTemp("", "hex-publish-*")
	if err != nil {
		return err
	}
	defer a.removeTemporary(temporary)

	displayTitle := title
	if displayTitle == "" && update != "" {
		displayTitle = a.artifactTitle(ctx, project, update)
	}
	if displayTitle == "" {
		displayTitle = filepath.Base(target)
	}
	files, err := shareFiles(target, displayTitle, temporary)
	if err != nil {
		return err
	}
	if err := a.checkPublication(ctx, files, forbidden, assumeYes); err != nil {
		return err
	}

	name := update
	if name == "" {
		created, err := a.createArtifact(ctx, project, displayTitle)
		if err != nil {
			return err
		}
		name = created.Name
		title = displayTitle
	}

	access, err := a.shareAccess(ctx, project, with)
	if err != nil {
		return err
	}
	result, err := a.publishFiles(ctx, project, name, files, &hex.SiteMetadata{Title: title}, access)
	if err != nil {
		if update == "" {
			return fmt.Errorf("%w\nThe site %s was created but not uploaded; retry with hex publish <path> --update %s", err, name, name)
		}
		return err
	}

	fmt.Fprintln(a.Out, result.URL)
	switch {
	case len(with) > 0:
		fmt.Fprintf(a.Err, "Shared with you and %s. Replace it later with: hex publish <path> --update %s\n", strings.Join(with, ", "), name)
	case update == "":
		fmt.Fprintf(a.Err, "Only you can open it; share it with --with or hex access. Replace it later with: hex publish <path> --update %s\n", name)
	}
	return nil
}

// artifactTitle is the current title of one of the caller's sites, or empty
// when it cannot be found.
func (a *App) artifactTitle(ctx context.Context, project Project, name string) string {
	sites, err := a.mySites(ctx, project)
	if err != nil {
		return ""
	}
	for _, site := range sites {
		if site.Name == name {
			return site.Title
		}
	}
	return ""
}

func (a *App) createArtifact(ctx context.Context, project Project, title string) (artifact, error) {
	data, err := a.apiCall(ctx, project, http.MethodPost, "/api/hex/artifacts", map[string]string{"title": title})
	if err != nil {
		var status *apiStatusError
		if errors.As(err, &status) && status.Status == http.StatusNotFound {
			return artifact{}, errors.New("this platform cannot publish files and folders without hex.json; it needs sign-in, access control and publishing")
		}
		return artifact{}, err
	}
	var created artifact
	if err := json.Unmarshal(data, &created); err != nil || validateSiteName(created.Name) != nil {
		return artifact{}, errors.New("unexpected response when creating the artifact")
	}
	return created, nil
}

// shareAccess turns --with into the artifact's viewers, keeping the creator
// able to open it. Without --with the current policy is left unchanged.
func (a *App) shareAccess(ctx context.Context, project Project, with []string) (json.RawMessage, error) {
	if len(with) == 0 {
		return nil, nil
	}
	data, err := a.apiRequest(ctx, project, "/api/hex/me")
	if err != nil {
		return nil, fmt.Errorf("look up your identity to share the artifact: %w", err)
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &me); err != nil || me.ID == "" {
		return nil, errors.New("the platform did not return your identity")
	}
	return json.Marshal(map[string][]string{"viewers": append([]string{"user:" + me.ID}, with...)})
}

// shareFiles lists what to publish for a file or folder, adding a generated
// index.html (written to temporary) when the target has none.
func shareFiles(target, title, temporary string) ([]sourceFile, error) {
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symlinks cannot be published: %s", target)
	}

	if info.IsDir() {
		files, err := collectFiles(target, nil)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("%s has no files to share", target)
		}
		for _, file := range files {
			if file.Key == "index.html" {
				return files, nil
			}
		}
		index, err := writeGeneratedPage(temporary, listingPage(title, files))
		if err != nil {
			return nil, err
		}
		return append(files, index), nil
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", target)
	}
	name := filepath.Base(target)
	if strings.HasPrefix(name, ".") {
		return nil, errors.New("hidden files cannot be shared")
	}
	extension := strings.ToLower(filepath.Ext(name))
	if extension == ".html" || extension == ".htm" {
		return []sourceFile{{Key: "index.html", Path: target}}, nil
	}

	page, err := filePage(title, name, target, info.Size())
	if err != nil {
		return nil, err
	}
	index, err := writeGeneratedPage(temporary, page)
	if err != nil {
		return nil, err
	}
	return []sourceFile{{Key: name, Path: target}, index}, nil
}

func writeGeneratedPage(directory string, page generatedPage) (sourceFile, error) {
	path := filepath.Join(directory, "index.html")
	output, err := os.Create(path)
	if err != nil {
		return sourceFile{}, err
	}
	executeError := pageTemplate.Execute(output, page)
	if err := errors.Join(executeError, output.Close()); err != nil {
		return sourceFile{}, fmt.Errorf("generate the artifact page: %w", err)
	}
	return sourceFile{Key: "index.html", Path: path}, nil
}

type generatedPage struct {
	Title   string
	File    *pageFile
	Preview string
	Text    string
	Files   []pageFile
}

type pageFile struct {
	Name string
	Link string
	Size string
}

var previewKinds = map[string]string{
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image", ".svg": "image", ".avif": "image",
	".pdf": "pdf",
	".mp4": "video", ".webm": "video",
	".mp3": "audio", ".wav": "audio", ".ogg": "audio", ".m4a": "audio",
	".txt": "text", ".md": "text", ".csv": "text", ".json": "text", ".log": "text", ".yaml": "text", ".yml": "text", ".xml": "text",
}

func filePage(title, name, path string, size int64) (generatedPage, error) {
	page := generatedPage{
		Title:   title,
		File:    &pageFile{Name: name, Link: linkTo(name), Size: formatSize(size)},
		Preview: previewKinds[strings.ToLower(filepath.Ext(name))],
	}
	if page.Preview == "text" {
		if size > maxInlineText {
			page.Preview = ""
			return page, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return page, err
		}
		page.Text = string(data)
	}
	return page, nil
}

func listingPage(title string, files []sourceFile) generatedPage {
	page := generatedPage{Title: title}
	for _, file := range files {
		size := ""
		if info, err := os.Stat(file.Path); err == nil {
			size = formatSize(info.Size())
		}
		page.Files = append(page.Files, pageFile{Name: file.Key, Link: linkTo(file.Key), Size: size})
	}
	return page
}

// linkTo is a relative link to a published file, escaped per segment.
func linkTo(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "./" + strings.Join(segments, "/")
}

func formatSize(size int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

var pageTemplate = template.Must(template.New("artifact").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root { color-scheme: light dark; --muted: #6b7280; --line: #e5e7eb; --accent: #2563eb; }
  @media (prefers-color-scheme: dark) { :root { --muted: #9ca3af; --line: #374151; --accent: #60a5fa; } }
  body { font: 16px/1.5 system-ui, sans-serif; margin: 0 auto; max-width: 60rem; padding: 2rem 1rem; }
  h1 { font-size: 1.5rem; margin: 0 0 .25rem; overflow-wrap: anywhere; }
  .meta { color: var(--muted); margin: 0 0 1.5rem; }
  a { color: var(--accent); }
  .download { display: inline-block; margin-bottom: 1.5rem; }
  img, video, iframe { max-width: 100%; border: 1px solid var(--line); border-radius: 6px; }
  iframe { width: 100%; height: 80vh; }
  audio { width: 100%; }
  pre { white-space: pre-wrap; overflow-wrap: anywhere; border: 1px solid var(--line); border-radius: 6px; padding: 1rem; }
  table { width: 100%; border-collapse: collapse; }
  td { padding: .4rem 0; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }
  td.size { color: var(--muted); text-align: right; white-space: nowrap; padding-left: 1rem; }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
{{with .File}}
<p class="meta">{{.Name}} · {{.Size}}</p>
<a class="download" href="{{.Link}}" download>Download</a>
{{end}}
{{if eq .Preview "image"}}<img src="{{.File.Link}}" alt="{{.File.Name}}">
{{else if eq .Preview "pdf"}}<iframe src="{{.File.Link}}" title="{{.File.Name}}"></iframe>
{{else if eq .Preview "video"}}<video src="{{.File.Link}}" controls></video>
{{else if eq .Preview "audio"}}<audio src="{{.File.Link}}" controls></audio>
{{else if eq .Preview "text"}}<pre>{{.Text}}</pre>
{{end}}
{{if .Files}}
<p class="meta">{{len .Files}} files</p>
<table>
{{range .Files}}<tr><td><a href="{{.Link}}">{{.Name}}</a></td><td class="size">{{.Size}}</td></tr>
{{end}}</table>
{{end}}
</body>
</html>
`))
