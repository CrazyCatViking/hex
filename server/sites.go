package hex

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var siteNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Site struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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
		sites = append(sites, Site{
			Name: name,
			URL:  siteURL.String(),
		})
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
