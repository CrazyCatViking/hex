package hex

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsedLimit, err := strconv.Atoi(value)
		if err != nil || parsedLimit < 1 || parsedLimit > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsedLimit
	}

	authorization := requestAuthorization(r)
	collection := r.PathValue("collection")
	options := ListOptions{After: r.URL.Query().Get("after"), Limit: limit}
	documents, err := s.listAuthorizedDocuments(r.Context(), authorization, r.PathValue("site"), collection, options)
	if err != nil {
		writePermissionError(w, err)
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
	document, err := s.mutateAuthorizedDocument(r.Context(), requestAuthorization(r), site, collection, id, data, false)
	if err != nil {
		writePermissionError(w, err)
		return
	}

	writeJSON(w, status, document)
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	authorization := requestAuthorization(r)
	if _, allowed := authorization.readFilter(r.PathValue("collection")); !allowed {
		writeError(w, http.StatusForbidden, "reading this collection is restricted")
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
	// Other people's documents under a creator-only rule look missing, so
	// their IDs cannot be probed.
	if !authorization.canReadDocument(collection, document) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	writeJSON(w, http.StatusOK, document)
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	site := r.PathValue("site")
	collection := r.PathValue("collection")
	id := r.PathValue("id")
	_, err := s.mutateAuthorizedDocument(r.Context(), requestAuthorization(r), site, collection, id, nil, true)
	if err != nil {
		writePermissionError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writePermissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, "this operation is not permitted by the site's permission policy")
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrAuthorizationUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		writeServerError(w, err)
	}
}

// documentWriteOptions applies the collection's write rule: full write
// access records the caller as creator of new documents, and a creator-only
// rule additionally limits changes to the caller's own documents.
func documentWriteOptions(w http.ResponseWriter, r *http.Request) (WriteOptions, bool) {
	authorization := requestAuthorization(r)
	options, err := authorization.collectionWriteOptions(r.PathValue("collection"))
	if err != nil {
		writeError(w, http.StatusForbidden, "writing this collection is restricted")
		return WriteOptions{}, false
	}
	return options, true
}
