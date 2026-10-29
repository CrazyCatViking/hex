package hex

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
)

func (s *Server) describePermissionPolicy(w http.ResponseWriter, identity *Identity, access SiteAccess, role siteRole, exists, manage bool) {
	a := siteAuthorization{identity: identity, access: access, role: role}
	document := permissionsDocument{
		Version: 2, Role: roleNames[role], Roles: a.applicationRoles(), Admin: s.isAdmin(identity),
		Publish: manage || !exists && s.canCreateSites(identity), Paths: []pathPermission{},
		Collections: map[string]dataPermission{}, Files: map[string]dataPermission{}, Channels: map[string]dataPermission{},
	}
	if role == roleNone {
		document.Roles = []string{}
		access.Grants = nil
	}
	resources := map[string][]string{"collection": {"*"}, "file": {"*"}, "channel": {"*"}, "path": {}}
	for _, grant := range access.Grants {
		resources[grant.Kind] = appendUnique(resources[grant.Kind], grant.Resource)
		for _, excluded := range grant.Except {
			resources[grant.Kind] = appendUnique(resources[grant.Kind], excluded)
		}
	}
	for kind, names := range resources {
		slices.Sort(names)
		for _, name := range names {
			if kind == "path" {
				if name != "*" {
					document.Paths = append(document.Paths, pathPermission{Prefix: name, Allowed: s.pathAllowed(role, identity, access, name)})
				}
				continue
			}
			permission := dataPermission{Read: grantNames[a.operationGrant(kind, name, "read")], Write: "none"}
			switch kind {
			case "collection":
				permission.Create = grantNames[a.operationGrant(kind, name, "create")]
				permission.Update = grantNames[a.operationGrant(kind, name, "update")]
				permission.Delete = grantNames[a.operationGrant(kind, name, "delete")]
				if permission.Create == permission.Update && permission.Update == permission.Delete {
					permission.Write = permission.Update
				}
				document.Collections[name] = permission
			case "file":
				permission.Upload = grantNames[a.operationGrant(kind, name, "upload")]
				permission.Delete = grantNames[a.operationGrant(kind, name, "delete")]
				if permission.Upload == permission.Delete {
					permission.Write = permission.Upload
				}
				document.Files[name] = permission
			case "channel":
				permission.Send = grantNames[a.operationGrant(kind, name, "send")]
				permission.Write = permission.Send
				document.Channels[name] = permission
			}
		}
	}
	writeJSON(w, http.StatusOK, document)
}

// checkPermission predicts the current caller's record access. It is not an
// authorization token: subsequent reads and mutations still check live rules.
func (s *Server) checkPermission(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Collection string          `json:"collection"`
		Operation  string          `json:"operation"`
		ID         string          `json:"id,omitempty"`
		Data       json.RawMessage `json:"data,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "expected a permission check with collection, operation, optional id and data")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || !validCollectionName(input.Collection) || !slices.Contains([]string{"read", "create", "update", "delete"}, input.Operation) || input.ID != "" && !validCollectionName(input.ID) {
		writeError(w, http.StatusBadRequest, "invalid permission check")
		return
	}
	a := requestAuthorization(r)
	var before *Document
	if input.ID != "" {
		document, err := s.config.Database.Get(r.Context(), r.PathValue("site"), input.Collection, input.ID)
		if err == nil {
			before = &document
		} else if !errors.Is(err, ErrNotFound) {
			writeServerError(w, err)
			return
		}
	}
	allowed := false
	if input.Operation == "read" {
		allowed = before != nil && a.canReadDocument(input.Collection, *before)
	} else if input.Operation == "create" && before == nil || input.Operation != "create" && before != nil {
		after := Document{ID: input.ID, CreatedBy: a.creator(), Data: input.Data}
		if before != nil {
			after.CreatedBy = before.CreatedBy
		}
		if input.Operation == "delete" {
			after = *before
		} else if err := ValidateDocumentData(input.Data); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		allowed = a.canMutate(input.Collection, input.Operation, before, after)
	}
	writeJSON(w, http.StatusOK, map[string]any{"allowed": allowed, "version": 2})
}
