package hex

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	if !validateIdentifiers(w, r) {
		return
	}

	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsedLimit, err := strconv.Atoi(value)
		if err != nil || parsedLimit < 1 || parsedLimit > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsedLimit
	}

	site := r.PathValue("site")
	collection := r.PathValue("collection")
	after := r.URL.Query().Get("after")
	documents, err := s.config.Database.List(r.Context(), site, collection, after, limit)
	if err != nil {
		writeServerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, documents)
}

func (s *Server) createDocument(w http.ResponseWriter, r *http.Request) {
	id, err := newID()
	if err != nil {
		writeServerError(w, err)
		return
	}

	r.SetPathValue("id", id)
	s.writeDocument(w, r, http.StatusCreated)
}

func (s *Server) putDocument(w http.ResponseWriter, r *http.Request) {
	s.writeDocument(w, r, http.StatusOK)
}

func (s *Server) writeDocument(w http.ResponseWriter, r *http.Request, status int) {
	if !validateIdentifiers(w, r) {
		return
	}

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a JSON object of at most 1 MiB")
		return
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		writeError(w, http.StatusBadRequest, "expected a JSON object of at most 1 MiB")
		return
	}

	site := r.PathValue("site")
	collection := r.PathValue("collection")
	id := r.PathValue("id")
	if err := s.config.Database.Put(r.Context(), site, collection, id, data); err != nil {
		writeServerError(w, err)
		return
	}

	writeJSON(w, status, Document{ID: id, Data: data})
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	if !validateIdentifiers(w, r) {
		return
	}

	site := r.PathValue("site")
	collection := r.PathValue("collection")
	id := r.PathValue("id")
	document, err := s.config.Database.Get(r.Context(), site, collection, id)
	if err != nil {
		writeServerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, document)
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	if !validateIdentifiers(w, r) {
		return
	}

	site := r.PathValue("site")
	collection := r.PathValue("collection")
	id := r.PathValue("id")
	if err := s.config.Database.Delete(r.Context(), site, collection, id); err != nil {
		writeServerError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
