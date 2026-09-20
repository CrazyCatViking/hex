package hex

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write JSON response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeServerError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	slog.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

func closeReader(reader io.Closer, description string) {
	if err := reader.Close(); err != nil {
		slog.Error("close reader", "resource", description, "error", err)
	}
}
