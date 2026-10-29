package hex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// This namespace cannot be a DNS site name, so app APIs cannot read or mutate
// portal preferences. The configured database already supplies workspace isolation.
const portalPreferencesSite = "@hex-portal"

var errPinLimit = errors.New("you can pin up to 100 apps; remove a pin first")

type portalPreferences struct {
	Pins   []string `json:"pins"`
	Recent []string `json:"recent"`
}

func preferenceID(identity *Identity) string {
	digest := sha256.Sum256([]byte(identity.Provider + "\x00" + identity.ID))
	return hex.EncodeToString(digest[:])
}

func (s *Server) preferencesEnabled(identity *Identity) bool {
	_, ok := s.config.Database.(GuardedDatabase)
	return ok && identity != nil && identity.ID != "" && identity.Provider != "automation"
}

func (s *Server) portalPreferences(ctx context.Context, identity *Identity) (portalPreferences, error) {
	var prefs portalPreferences
	if !s.preferencesEnabled(identity) {
		return prefs, nil
	}
	document, err := s.config.Database.Get(ctx, portalPreferencesSite, "preferences", preferenceID(identity))
	if errors.Is(err, ErrNotFound) {
		return prefs, nil
	}
	if err != nil {
		return prefs, fmt.Errorf("read portal preferences: %w", err)
	}
	if err := json.Unmarshal(document.Data, &prefs); err != nil {
		return prefs, fmt.Errorf("decode portal preferences: %w", err)
	}
	return prefs, nil
}

func (s *Server) updatePin(w http.ResponseWriter, r *http.Request) {
	s.updatePortalPreference(w, r, false)
}

func (s *Server) recordRecent(w http.ResponseWriter, r *http.Request) {
	s.updatePortalPreference(w, r, true)
}

func (s *Server) updatePortalPreference(w http.ResponseWriter, r *http.Request, recent bool) {
	identity := s.requestIdentity(r)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if !s.preferencesEnabled(identity) {
		writeError(w, http.StatusNotImplemented, "personal shortcuts need a database with guarded writes")
		return
	}
	site := r.PathValue("site")
	if !siteNamePattern.MatchString(site) {
		writeError(w, http.StatusBadRequest, "invalid site")
		return
	}
	var input struct {
		Pinned *bool `json:"pinned"`
	}
	if !recent {
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || input.Pinned == nil {
			writeError(w, http.StatusBadRequest, "expected a pinned boolean")
			return
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "expected one JSON object")
			return
		}
	}
	// Unpinning remains possible after revocation; adding shortcuts or history
	// always checks admission against the current policy, never inventory cache.
	pinned := input.Pinned != nil && *input.Pinned
	if recent || pinned {
		allowed, err := s.canViewSite(r.Context(), identity, site)
		if err != nil {
			writeServerError(w, err)
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "access to this site is restricted")
			return
		}
		if s.config.Sites == nil {
			writeError(w, http.StatusNotFound, "site is not published")
			return
		}
		sites, err := s.portalSites(r.Context())
		if err != nil {
			writeServerError(w, err)
			return
		}
		if !slices.ContainsFunc(sites, func(candidate Site) bool { return candidate.Name == site }) {
			writeError(w, http.StatusNotFound, "site is not published")
			return
		}
	}
	database := s.config.Database.(GuardedDatabase)
	_, err := database.Mutate(r.Context(), portalPreferencesSite, "preferences", preferenceID(identity), identity.ID, func(current *Document) (json.RawMessage, error) {
		var prefs portalPreferences
		if current != nil {
			if err := json.Unmarshal(current.Data, &prefs); err != nil {
				return nil, fmt.Errorf("decode portal preferences: %w", err)
			}
		}
		if recent {
			prefs.Recent = slices.DeleteFunc(prefs.Recent, func(name string) bool { return name == site })
			prefs.Recent = append([]string{site}, prefs.Recent...)
			prefs.Recent = prefs.Recent[:min(20, len(prefs.Recent))]
		} else {
			prefs.Pins = slices.DeleteFunc(prefs.Pins, func(name string) bool { return name == site })
			if pinned {
				if len(prefs.Pins) >= 100 {
					return nil, errPinLimit
				}
				prefs.Pins = append(prefs.Pins, site)
			}
		}
		return json.Marshal(prefs)
	})
	if err != nil {
		if errors.Is(err, errPinLimit) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, input)
}
