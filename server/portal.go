package hex

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

//go:embed portal/* installers/*
var platformAssets embed.FS

var portalTemplates = template.Must(template.ParseFS(platformAssets, "portal/*.html"))

type portalView struct {
	Name                string
	InstallersAvailable bool
	Statistics          siteStatistics
	Sites               []portalSite
	Search              string
	Sort                string
}

type portalSite struct {
	Name         string
	URL          string
	Title        string
	Description  string
	Author       string
	Published    string
	PublishedISO string
	Timestamp    time.Time
}

type siteStatistics struct {
	Sites           int `json:"sites"`
	Authors         int `json:"authors"`
	UpdatedRecently int `json:"updatedRecently"`
}

func (s *Server) landingPage(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.renderPortal(w, r, "index.html")
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	s.renderPortal(w, r, "catalog")
}

func (s *Server) renderPortal(w http.ResponseWriter, r *http.Request, name string) {
	view, err := s.portalView(r.Context(), r.URL.Query())
	if err != nil {
		writeServerError(w, err)
		return
	}
	var output bytes.Buffer
	if err := portalTemplates.ExecuteTemplate(&output, name, view); err != nil {
		writeServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(output.Bytes()); err != nil {
		slog.Error("write portal", "error", err)
	}
}

func (s *Server) portalView(ctx context.Context, query url.Values) (portalView, error) {
	sites, err := s.discoverSites(ctx)
	if err != nil {
		return portalView{}, err
	}
	_, connectionError := s.connectionSettings()
	_, releaseError := s.cliReleaseURL()
	view := portalView{
		Name:                s.platformName(),
		InstallersAvailable: connectionError == nil && releaseError == nil,
		Statistics:          summarizeSites(sites, time.Now()),
		Search:              query.Get("search"),
		Sort:                query.Get("sort"),
	}
	search := strings.ToLower(strings.TrimSpace(view.Search))
	for _, site := range sites {
		card := portalSite{
			Name:        site.Name,
			URL:         site.URL,
			Title:       site.Name,
			Description: "A tool built by your team.",
			Author:      "Your team",
			Published:   "Published app",
		}
		if metadata := site.Metadata; metadata != nil {
			if metadata.Title != "" {
				card.Title = metadata.Title
			}
			if metadata.Description != "" {
				card.Description = metadata.Description
			}
			if metadata.Author != "" {
				card.Author = metadata.Author
			}
			if !metadata.PublishedAt.IsZero() {
				card.Timestamp = metadata.PublishedAt
				card.Published = metadata.PublishedAt.Format("Jan 2, 2006")
				card.PublishedISO = metadata.PublishedAt.Format(time.RFC3339)
			}
		}
		text := strings.ToLower(strings.Join([]string{card.Name, card.Title, card.Description, card.Author}, " "))
		if strings.Contains(text, search) {
			view.Sites = append(view.Sites, card)
		}
	}
	slices.SortFunc(view.Sites, func(left, right portalSite) int {
		if view.Sort != "name" && !left.Timestamp.Equal(right.Timestamp) {
			return right.Timestamp.Compare(left.Timestamp)
		}
		return strings.Compare(strings.ToLower(left.Title), strings.ToLower(right.Title))
	})
	return view, nil
}

func (s *Server) platformName() string {
	if s.config.Connection != nil && strings.TrimSpace(s.config.Connection.Name) != "" {
		return s.config.Connection.Name
	}
	return "Hex"
}

func (s *Server) platformHost(host string) bool {
	origins := []string{s.config.SiteBaseURL}
	if s.config.Connection != nil {
		origins = append(origins, s.config.Connection.Server)
	}
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err == nil && strings.EqualFold(host, parsed.Host) {
			return true
		}
	}
	return false
}

func (s *Server) portalAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/hex/")
	http.ServeFileFS(w, r, platformAssets, "portal/"+name)
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	sites, err := s.discoverSites(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}
	_, connectionError := s.connectionSettings()
	_, releaseError := s.cliReleaseURL()
	writeJSON(w, http.StatusOK, struct {
		Name                string         `json:"name"`
		Sites               []Site         `json:"sites"`
		Statistics          siteStatistics `json:"statistics"`
		InstallersAvailable bool           `json:"installersAvailable"`
	}{s.platformName(), sites, summarizeSites(sites, time.Now()), connectionError == nil && releaseError == nil})
}

func summarizeSites(sites []Site, now time.Time) siteStatistics {
	statistics := siteStatistics{Sites: len(sites)}
	authors := make(map[string]bool)
	since := now.AddDate(0, 0, -30)
	for _, site := range sites {
		if site.Metadata == nil {
			continue
		}
		if author := strings.ToLower(strings.TrimSpace(site.Metadata.Author)); author != "" {
			authors[author] = true
		}
		published := site.Metadata.PublishedAt
		if !published.Before(since) && !published.After(now) {
			statistics.UpdatedRecently++
		}
	}
	statistics.Authors = len(authors)
	return statistics
}
