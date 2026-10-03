package hex

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	htmlparser "golang.org/x/net/html"
)

const maxSiteIconBytes = 256 << 10

type siteFileReader interface {
	ReadSiteFile(context.Context, string, string) (io.ReadCloser, error)
}

func (s *Server) siteIconReader() siteFileReader {
	// Prefer the mounted directory over remote publishing APIs.
	if reader, ok := s.config.Sites.(siteFileReader); ok {
		return reader
	}
	return s.config.Publisher
}

func (s *Server) cardForSite(name, siteURL string, metadata *SiteMetadata, now time.Time) siteCard {
	card := cardFromMetadata(name, siteURL, metadata, now)
	if card.Kind != "File" && s.siteIconReader() != nil {
		card.IconURL = "/api/hex/sites/" + name + "/icon"
	}
	return card
}

// siteIcon serves a small image preview on the portal origin, where the viewer's
// existing session and CSP apply. It never fetches publisher-supplied URLs.
func (s *Server) siteIcon(w http.ResponseWriter, r *http.Request) {
	if !s.platformHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	site := r.PathValue("site")
	if !siteNamePattern.MatchString(site) {
		writeError(w, http.StatusBadRequest, "invalid site")
		return
	}
	identity := s.requestIdentity(r)
	if s.config.Identity != nil && identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	authorization, err := s.authorizeSite(r.Context(), identity, site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	if authorization.role == roleNone {
		writeError(w, http.StatusForbidden, "access to this site is restricted")
		return
	}
	reader := s.siteIconReader()
	if reader == nil {
		http.NotFound(w, r)
		return
	}
	siteURL, err := s.siteURL(site)
	if err != nil {
		writeServerError(w, err)
		return
	}
	candidates := []string{}
	if pathAllowed(authorization.role, identity, authorization.access, "/") &&
		pathAllowed(authorization.role, identity, authorization.access, "/index.html") {
		index, err := reader.ReadSiteFile(r.Context(), site, "index.html")
		if err == nil {
			candidates = siteIconCandidates(io.LimitReader(index, maxSiteIconBytes), siteURL)
			closeReader(index, "site icon index")
		} else if !errors.Is(err, ErrNotFound) {
			slog.Warn("read app icon declaration", "site", site, "error", err)
		}
	}
	candidates = append(candidates, "favicon.svg", "favicon.ico", "favicon.png", "apple-touch-icon.png")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	for _, candidate := range candidates {
		if !pathAllowed(authorization.role, identity, authorization.access, "/"+candidate) {
			continue
		}
		data, contentType, err := readSiteIcon(r.Context(), reader, site, candidate)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			slog.Warn("read app icon", "site", site, "path", candidate, "error", err)
			break
		}
		w.Header().Set("Content-Type", contentType)
		if _, err := w.Write(data); err != nil {
			slog.Error("write app icon", "error", err)
		}
		return
	}
	s.writeSiteIconFallback(w, r, site)
}

func siteIconCandidates(index io.Reader, siteURL string) []string {
	base, err := url.Parse(siteURL)
	if err != nil {
		return nil
	}
	origin := *base
	icons, touchIcons := []string{}, []string{}
	tokenizer := htmlparser.NewTokenizer(index)
	baseSet := false
	for {
		kind := tokenizer.Next()
		if kind == htmlparser.ErrorToken {
			if err := tokenizer.Err(); err != nil && !errors.Is(err, io.EOF) {
				slog.Warn("parse app icon declaration", "siteURL", siteURL, "error", err)
			}
			break
		}
		if kind != htmlparser.StartTagToken && kind != htmlparser.SelfClosingTagToken && kind != htmlparser.EndTagToken {
			continue
		}
		token := tokenizer.Token()
		if token.Data == "body" || kind == htmlparser.EndTagToken && token.Data == "head" {
			break
		}
		if kind == htmlparser.EndTagToken {
			continue
		}
		attributes := make(map[string]string)
		for _, attribute := range token.Attr {
			attributes[attribute.Key] = attribute.Val
		}
		if token.Data == "base" && !baseSet {
			if href, err := url.Parse(attributes["href"]); err == nil && attributes["href"] != "" {
				base = base.ResolveReference(href)
				baseSet = true
			}
		}
		if token.Data != "link" || attributes["href"] == "" {
			continue
		}
		relations := strings.Fields(strings.ToLower(attributes["rel"]))
		if slices.Contains(relations, "icon") && len(icons) < 8 {
			icons = append(icons, attributes["href"])
		} else if slices.Contains(relations, "apple-touch-icon") && len(touchIcons) < 4 {
			touchIcons = append(touchIcons, attributes["href"])
		}
	}
	candidates := []string{}
	for _, href := range append(icons, touchIcons...) {
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		resolved := base.ResolveReference(parsed)
		if resolved.Scheme != origin.Scheme || !strings.EqualFold(resolved.Host, origin.Host) || resolved.User != nil {
			continue
		}
		key := strings.TrimPrefix(resolved.Path, "/")
		if validKey(key) && siteIconContentType(key) != "" && !slices.Contains(candidates, key) {
			candidates = append(candidates, key)
		}
	}
	return candidates
}

func siteIconContentType(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

func readSiteIcon(ctx context.Context, reader siteFileReader, site, key string) ([]byte, string, error) {
	contentType := siteIconContentType(key)
	if contentType == "" {
		return nil, "", ErrNotFound
	}
	file, err := reader.ReadSiteFile(ctx, site, key)
	if err != nil {
		return nil, "", err
	}
	data, readError := io.ReadAll(io.LimitReader(file, maxSiteIconBytes+1))
	if err := errors.Join(readError, file.Close()); err != nil {
		return nil, "", err
	}
	if len(data) > maxSiteIconBytes || len(data) == 0 {
		return nil, "", ErrNotFound
	}
	if contentType == "image/svg+xml" {
		decoder := xml.NewDecoder(bytes.NewReader(data))
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, "", ErrNotFound
			}
			if start, ok := token.(xml.StartElement); ok {
				if start.Name.Local != "svg" || start.Name.Space != "" && start.Name.Space != "http://www.w3.org/2000/svg" {
					return nil, "", ErrNotFound
				}
				break
			}
		}
	} else if http.DetectContentType(data) != contentType {
		return nil, "", ErrNotFound
	}
	return data, contentType, nil
}

func (s *Server) writeSiteIconFallback(w http.ResponseWriter, r *http.Request, site string) {
	var metadata *SiteMetadata
	if reader, ok := s.config.Sites.(SiteMetadataReader); ok {
		var err error
		metadata, err = reader.ReadSiteMetadata(r.Context(), site)
		if err != nil {
			slog.Warn("read app icon fallback metadata", "site", site, "error", err)
		}
	}
	card := cardFromMetadata(site, "", metadata, time.Now())
	colors := map[string]string{"tone-0": "#1f8a68", "tone-1": "#4b63d6", "tone-2": "#b4542d", "tone-3": "#8d4bc4", "tone-4": "#2b7f9e", "tone-5": "#a3366a"}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, err := fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="8" fill="%s"/><text x="16" y="16" dy=".35em" text-anchor="middle" fill="white" font-family="system-ui,sans-serif" font-size="18" font-weight="750">%s</text></svg>`, colors[card.Tone], html.EscapeString(card.Initial))
	if err != nil {
		slog.Error("write app icon fallback", "error", err)
	}
}
