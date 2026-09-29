package hex

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// Artifacts are files, and folders without hex.json, published with
// hex publish <path>. The server names
// them randomly and creates them private to their creator; the creator then
// publishes to the name like any owned site and adds viewers to share it.
// Anyone who can sign in may create artifacts: random names cannot squat on
// readable app names, which HEX_PUBLISHER_GROUPS protects.

const (
	artifactNameLength   = 10
	artifactNameAttempts = 5
	artifactLetters      = "abcdefghijklmnopqrstuvwxyz"
	artifactCharacters   = artifactLetters + "0123456789"
)

type artifactRequest struct {
	Title string `json:"title"`
}

type artifactSummary struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Title     string    `json:"title,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitzero"`
	Viewers   []string  `json:"viewers"`
}

func (s *Server) createArtifact(w http.ResponseWriter, r *http.Request) {
	identity := s.requestIdentity(r)
	if identity == nil || identity.ID == "" {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var request artifactRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err == nil && len(data) > 0 {
		err = json.Unmarshal(data, &request)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a JSON object with an optional title")
		return
	}
	if len(request.Title) > 200 {
		writeError(w, http.StatusBadRequest, "titles are limited to 200 characters")
		return
	}

	site, err := s.reserveArtifactName(r)
	if err != nil {
		writeServerError(w, err)
		return
	}

	owner := "user:" + identity.ID
	access := NormalizeSiteAccess(SiteAccess{Owners: []string{owner}, Viewers: []string{owner}})
	if err := s.config.Access.PutSiteAccess(r.Context(), site, access); err != nil {
		writeServerError(w, err)
		return
	}
	s.policies.invalidate(site)

	hidden := false
	metadata := SiteMetadata{
		Title:        strings.TrimSpace(request.Title),
		Discoverable: &hidden,
		Kind:         KindArtifact,
		CreatedAt:    time.Now().UTC(),
		CreatedBy:    personOf(identity),
	}
	if err := s.writeMetadata(r.Context(), site, metadata); err != nil {
		writeServerError(w, err)
		return
	}

	slog.Info("artifact created", "site", site, "creator", identityName(identity))
	siteURL, _ := s.siteURL(site)
	writeJSON(w, http.StatusCreated, artifactSummary{Name: site, URL: siteURL, Title: metadata.Title, CreatedAt: metadata.CreatedAt, Viewers: access.Viewers})
}

// reserveArtifactName picks a random name that no site uses or owns.
func (s *Server) reserveArtifactName(r *http.Request) (string, error) {
	for range artifactNameAttempts {
		name, err := randomArtifactName()
		if err != nil {
			return "", err
		}
		_, owned, err := s.sitePolicy(r.Context(), name)
		if err != nil {
			return "", err
		}
		files, err := s.config.Publisher.ListSiteFiles(r.Context(), name)
		if err != nil {
			return "", err
		}
		if !owned && len(files) == 0 {
			return name, nil
		}
	}
	return "", errors.New("could not find a free artifact name")
}

// randomArtifactName is a DNS label starting with a letter.
func randomArtifactName() (string, error) {
	name := make([]byte, artifactNameLength)
	for i := range name {
		alphabet := artifactCharacters
		if i == 0 {
			alphabet = artifactLetters
		}
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("generate artifact name: %w", err)
		}
		name[i] = alphabet[index.Int64()]
	}
	return string(name), nil
}
