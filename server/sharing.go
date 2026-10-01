package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strings"
)

// The portal presents a site's policy like document sharing: one list of
// people and groups, each with a role, plus a general access setting. These
// map onto the policy's owners, editors and viewers, where an empty viewer
// list means everyone signed in may view and an empty editor list means every
// viewer may edit.

const (
	RoleOwner  = "owner"
	RoleEditor = "editor"
	RoleViewer = "viewer"

	GeneralRestricted = "restricted"
	GeneralView       = "view"
	GeneralEdit       = "edit"
)

type sharingView struct {
	Site           string
	Platform       string
	General        string
	GeneralChoices []choice
	Entries        []shareEntry
	EmptyAttr      template.HTMLAttr
	Rules          string
	AdvancedAttr   template.HTMLAttr
	Message        string
	Error          string
}

type shareEntry struct {
	Principal   string
	Name        string
	Detail      string
	Kind        string
	Initials    string
	Tone        string
	Role        string
	RoleChoices []choice
}

func roleChoices(role string) []choice {
	return choices(role, [2]string{RoleOwner, "Owner"}, [2]string{RoleEditor, "Can edit"}, [2]string{RoleViewer, "Can view"})
}

// decodeSharing turns a policy into a general access setting and a role per
// principal, in the order owners, editors, viewers.
func decodeSharing(access SiteAccess, exists bool) (string, []string, map[string]string) {
	if !exists {
		return GeneralEdit, nil, map[string]string{}
	}

	general := GeneralRestricted
	switch {
	case len(access.Viewers) == 0 && len(access.Editors) == 0:
		general = GeneralEdit
	case len(access.Viewers) == 0:
		general = GeneralView
	}

	// A restricted policy without editors lets every viewer edit.
	viewerRole := RoleViewer
	if general == GeneralRestricted && len(access.Editors) == 0 {
		viewerRole = RoleEditor
	}

	roles := map[string]string{}
	var order []string
	assign := func(principals []string, role string) {
		for _, principal := range principals {
			if _, seen := roles[principal]; !seen {
				roles[principal] = role
				order = append(order, principal)
			}
		}
	}
	assign(access.Owners, RoleOwner)
	assign(access.Editors, RoleEditor)
	assign(access.Viewers, viewerRole)
	return general, order, roles
}

// encodeSharing builds owners, editors and viewers from roles and general
// access. Owners must be known. Owners are repeated as viewers or editors
// where a list must not be empty: a restricted site needs viewers, and only
// editors may change data unless everyone can edit.
func encodeSharing(general string, principals, roles []string, owners []string) (SiteAccess, error) {
	if len(principals) != len(roles) {
		return SiteAccess{}, errors.New("every person or group needs a role")
	}
	var editors, viewers []string
	for i, principal := range principals {
		switch roles[i] {
		case RoleOwner:
		case RoleEditor:
			editors = appendUnique(editors, principal)
		case RoleViewer:
			viewers = appendUnique(viewers, principal)
		default:
			return SiteAccess{}, fmt.Errorf("unknown role %q", roles[i])
		}
	}

	access := SiteAccess{Owners: owners, Editors: []string{}, Viewers: []string{}}
	switch general {
	case GeneralRestricted:
		access.Viewers = viewers
		if len(access.Viewers) == 0 {
			access.Viewers = slices.Clone(owners)
		}
		access.Editors = editors
		if len(access.Editors) == 0 {
			access.Editors = slices.Clone(owners)
		}
	case GeneralView:
		access.Editors = editors
		if len(access.Editors) == 0 {
			access.Editors = slices.Clone(owners)
		}
	case GeneralEdit:
	default:
		return SiteAccess{}, fmt.Errorf("unknown general access %q", general)
	}
	return access, nil
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func (s *Server) sharingView(ctx context.Context, site string, access SiteAccess, exists bool) sharingView {
	general, order, roles := decodeSharing(access, exists)
	platform := s.platformName()
	view := sharingView{Site: site, Platform: platform, General: general}
	view.GeneralChoices = choices(general,
		[2]string{GeneralRestricted, "Only people added above"},
		[2]string{GeneralView, "Everyone at " + platform + " can view"},
		[2]string{GeneralEdit, "Everyone at " + platform + " can view and edit"},
	)
	for _, label := range s.describePrincipals(ctx, order) {
		view.Entries = append(view.Entries, shareEntry{
			Principal:   label.Principal,
			Name:        label.Name,
			Detail:      label.Detail,
			Kind:        label.Kind,
			Initials:    initials(label.Name),
			Tone:        tone(label.Principal),
			Role:        roles[label.Principal],
			RoleChoices: roleChoices(roles[label.Principal]),
		})
	}
	view.EmptyAttr = flag("hidden", len(view.Entries) > 0)

	rules := SiteAccess{Paths: access.Paths, Collections: access.Collections, Files: access.Files, Channels: access.Channels}
	encoded, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		encoded = []byte("{}")
	}
	view.Rules = rulesText(encoded)
	view.AdvancedAttr = flag("open", view.Rules != "{}")
	return view
}

// rulesText drops the owner, editor and viewer lists from the advanced rule
// editor, which edits them as roles instead.
func rulesText(encoded []byte) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return "{}"
	}
	for _, name := range []string{"owners", "editors", "viewers"} {
		delete(fields, name)
	}
	if len(fields) == 0 {
		return "{}"
	}
	pretty, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(pretty)
}

// manageUpdateSharing saves the sharing form and re-renders it with the
// outcome. Mistakes are shown in the form instead of being applied.
func (s *Server) manageUpdateSharing(w http.ResponseWriter, r *http.Request) {
	identity, site, access, exists, ok := s.manageCaller(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}

	failed := func(err error) {
		view := s.sharingView(r.Context(), site, access, exists)
		view.Error = err.Error()
		view.Rules = r.PostForm.Get("rules")
		view.AdvancedAttr = "open"
		s.renderFragment(w, "sharing", view)
	}

	requested, err := accessFromSharing(r)
	if err != nil {
		failed(err)
		return
	}
	saved, status, err := s.replaceSiteAccess(r.Context(), identity, site, requested)
	if err != nil {
		if status == 0 {
			writeServerError(w, err)
			return
		}
		failed(err)
		return
	}

	slog.Info("site sharing changed in the portal", "site", site, "by", identityName(identity))
	view := s.sharingView(r.Context(), site, saved, true)
	view.Message = "Saved. Changes apply to new requests."
	s.renderFragment(w, "sharing", view)
}

func accessFromSharing(r *http.Request) (SiteAccess, error) {
	principals := r.PostForm["principal"]
	roles := r.PostForm["role"]
	if len(principals) != len(roles) {
		return SiteAccess{}, errors.New("every person or group needs a role")
	}

	var owners []string
	for i, principal := range principals {
		principal = strings.TrimSpace(principal)
		principals[i] = principal
		if roles[i] == RoleOwner {
			owners = appendUnique(owners, principal)
		}
	}
	if len(owners) == 0 {
		return SiteAccess{}, errors.New("a site needs at least one owner; make someone an owner before removing the last one")
	}

	access, err := encodeSharing(r.PostForm.Get("general"), principals, roles, owners)
	if err != nil {
		return SiteAccess{}, err
	}

	rules := strings.TrimSpace(r.PostForm.Get("rules"))
	if rules != "" && rules != "{}" {
		var advanced SiteAccess
		decoder := json.NewDecoder(strings.NewReader(rules))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&advanced); err != nil {
			return SiteAccess{}, fmt.Errorf("the advanced rules are not valid: %v", err)
		}
		if len(advanced.Owners)+len(advanced.Editors)+len(advanced.Viewers) > 0 {
			return SiteAccess{}, errors.New("set owners, editors and viewers with the roles above, not in the advanced rules")
		}
		access.Paths = advanced.Paths
		access.Collections = advanced.Collections
		access.Files = advanced.Files
		access.Channels = advanced.Channels
	}
	return access, nil
}
