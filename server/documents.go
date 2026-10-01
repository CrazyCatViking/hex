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
	switch authorization.collection(collection).read {
	case grantNone:
		writeError(w, http.StatusForbidden, "reading this collection is restricted")
		return
	case grantOwn:
		options.CreatedBy = authorization.creator()
	}

	documents, err := s.config.Database.List(r.Context(), r.PathValue("site"), collection, options)
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
	options, ok := documentWriteOptions(w, r)
	if !ok {
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
	document, err := s.config.Database.Put(r.Context(), site, collection, id, data, options)
	if errors.Is(err, ErrForbidden) {
		writeError(w, http.StatusForbidden, "only the creator can change this document")
		return
	}
	if err != nil {
		writeServerError(w, err)
		return
	}

	writeJSON(w, status, document)
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	authorization := requestAuthorization(r)
	read := authorization.collection(r.PathValue("collection")).read
	if read == grantNone {
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
	if read == grantOwn && document.CreatedBy != authorization.creator() {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	writeJSON(w, http.StatusOK, document)
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	options, ok := documentWriteOptions(w, r)
	if !ok {
		return
	}

	site := r.PathValue("site")
	collection := r.PathValue("collection")
	id := r.PathValue("id")
	err := s.config.Database.Delete(r.Context(), site, collection, id, options)
	if errors.Is(err, ErrForbidden) {
		writeError(w, http.StatusForbidden, "only the creator can delete this document")
		return
	}
	if err != nil {
		writeServerError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
