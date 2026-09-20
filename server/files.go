package hex

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

func fileKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !validateIdentifiers(w, r) {
		return "", false
	}

	key := r.PathValue("key")
	if !validKey(key) {
		writeError(w, http.StatusBadRequest, "invalid file key")
		return "", false
	}

	return r.PathValue("site") + "/" + key, true
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	if !validateIdentifiers(w, r) {
		return
	}

	prefix := r.PathValue("site") + "/"
	objects, err := s.config.Files.List(r.Context(), prefix)
	if err != nil {
		writeServerError(w, err)
		return
	}

	for i := range objects {
		objects[i].Key = strings.TrimPrefix(objects[i].Key, prefix)
	}

	writeJSON(w, http.StatusOK, objects)
}

func (s *Server) putFile(w http.ResponseWriter, r *http.Request) {
	key, ok := fileKey(w, r)
	if !ok {
		return
	}

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.MaxUploadBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "upload exceeds limit or could not be read")
		return
	}

	if err := s.config.Files.Put(r.Context(), key, bytes.NewReader(data)); err != nil {
		writeServerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, Object{
		Key:  r.PathValue("key"),
		Size: int64(len(data)),
	})
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	key, ok := fileKey(w, r)
	if !ok {
		return
	}

	reader, err := s.config.Files.Open(r.Context(), key)
	if err != nil {
		writeServerError(w, err)
		return
	}
	defer closeReader(reader, key)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment")
	if _, err := io.Copy(w, reader); err != nil {
		slog.Error("stream file response", "key", key, "error", err)
	}
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	key, ok := fileKey(w, r)
	if !ok {
		return
	}

	if err := s.config.Files.Delete(r.Context(), key); err != nil {
		writeServerError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
