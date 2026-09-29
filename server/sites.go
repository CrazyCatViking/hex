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

// SiteMetadata describes a published site. Title, Description, Author and
// Discoverable come from the publisher; the server records the rest from the
// identity provider when one is configured, so they cannot be claimed by a
// publisher.
type SiteMetadata struct {
	Title        string    `json:"title,omitempty"`
	Description  string    `json:"description,omitempty"`
	Author       string    `json:"author,omitempty"`
	Discoverable *bool     `json:"discoverable,omitempty"`
	PublishedAt  time.Time `json:"publishedAt"`
	PublishedBy  *Person   `json:"publishedBy,omitempty"`
	CreatedAt    time.Time `json:"createdAt,omitzero"`
	CreatedBy    *Person   `json:"createdBy,omitempty"`
	// Kind is "artifact" for files and folders published without hex.json,
	// empty for apps.
	Kind string `json:"kind,omitempty"`
}

// Person is a user as resolved by the identity provider.
type Person struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

const KindArtifact = "artifact"

func personOf(identity *Identity) *Person {
	if identity == nil || identity.ID == "" {
		return nil
	}
	return &Person{ID: identity.ID, Name: identity.Name, Email: identity.Email}
}

// DisplayAuthor is who the site is shown as coming from: the verified
// creator when known, otherwise the publisher-supplied author.
func (m *SiteMetadata) DisplayAuthor() string {
	if m == nil {
		return ""
	}
	if m.CreatedBy != nil && m.CreatedBy.Name != "" {
		return m.CreatedBy.Name
	}
	return m.Author
}

type SiteMetadataReader interface {
	ReadSiteMetadata(context.Context, string) (*SiteMetadata, error)
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.discoverSites(r.Context(), s.requestIdentity(r))
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sites)
}

func (s *Server) discoverSites(ctx context.Context, viewer *Identity) ([]Site, error) {
	baseURL, err := parseSiteBaseURL(s.config.SiteBaseURL)
	if err != nil {
		return nil, err
	}
	if s.config.Sites == nil {
		return []Site{}, nil
	}
	names, err := s.config.Sites.ListSites(ctx)
	if err != nil {
		return nil, err
	}

	slices.Sort(names)
	sites := make([]Site, 0, len(names))
	for _, name := range slices.Compact(names) {
		if !siteNamePattern.MatchString(name) {
			continue
		}

		visible, err := s.canViewSite(ctx, viewer, name)
		if err != nil {
			return nil, err
		}
		if !visible {
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
			site.Metadata, err = reader.ReadSiteMetadata(ctx, name)
			if err != nil {
				return nil, err
			}
		}
		if site.Metadata != nil && (site.Metadata.Kind == KindArtifact || (site.Metadata.Discoverable != nil && !*site.Metadata.Discoverable)) {
			continue
		}
		sites = append(sites, site)
	}

	return sites, nil
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
