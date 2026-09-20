package hex

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func validateIdentifiers(w http.ResponseWriter, r *http.Request) bool {
	for _, key := range []string{"site", "collection", "id", "channel"} {
		value := r.PathValue(key)
		if value != "" && !namePattern.MatchString(value) {
			writeError(w, http.StatusBadRequest, "invalid "+key)
			return false
		}
	}

	return true
}

func validKey(key string) bool {
	if key == "." || !fs.ValidPath(key) || strings.ContainsAny(key, "\\\x00") {
		return false
	}

	for _, part := range strings.Split(key, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}

	return true
}

func newID() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}

	return hex.EncodeToString(randomBytes[:]), nil
}
