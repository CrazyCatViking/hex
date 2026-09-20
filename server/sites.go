package hex

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

var siteNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Site struct {
	Name     string        `json:"name"`
	URL      string        `json:"url"`
	Metadata *SiteMetadata `json:"metadata,omitempty"`
}

type SiteMetadata struct {
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Author      string    `json:"author,omitempty"`
	PublishedAt time.Time `json:"publishedAt"`
}

type SiteMetadataReader interface {
	ReadSiteMetadata(context.Context, string) (*SiteMetadata, error)
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	baseURL, err := parseSiteBaseURL(s.config.SiteBaseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}

	names, err := s.config.Sites.ListSites(r.Context())
	if err != nil {
		writeServerError(w, err)
		return
	}

	slices.Sort(names)
	sites := make([]Site, 0, len(names))
	for _, name := range slices.Compact(names) {
		if !siteNamePattern.MatchString(name) {
			continue
		}

		siteURL := *baseURL
		siteURL.Host = name + "." + baseURL.Host
		siteURL.Path = "/"
		site := Site{
			Name: name,
			URL:  siteURL.String(),
		}
		if reader, ok := s.config.Sites.(SiteMetadataReader); ok {
			site.Metadata, err = reader.ReadSiteMetadata(r.Context(), name)
			if err != nil {
				writeServerError(w, err)
				return
			}
		}
		sites = append(sites, site)
	}

	writeJSON(w, http.StatusOK, sites)
}

func parseSiteBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parse site base URL: %w", err)
	}
	validScheme := parsed.Scheme == "http" || parsed.Scheme == "https"
	hasExtraPath := (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != ""
	if !validScheme || parsed.Host == "" || parsed.User != nil || hasExtraPath {
		return nil, fmt.Errorf("site base URL must be an HTTP(S) origin")
	}
	if net.ParseIP(parsed.Hostname()) != nil {
		return nil, fmt.Errorf("site base URL requires a DNS hostname, not an IP address")
	}
	for _, label := range strings.Split(parsed.Hostname(), ".") {
		if !siteNamePattern.MatchString(label) {
			return nil, fmt.Errorf("invalid site base hostname")
		}
	}
	return parsed, nil
}
