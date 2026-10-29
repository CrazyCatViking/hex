package hex

import (
	"net/url"
	"testing"
)

func TestWorkspaceAudienceRequiresBrokerContext(t *testing.T) {
	for _, test := range []struct {
		name     string
		identity *Identity
		apiOnly  bool
		role     siteRole
		allowed  bool
	}{
		{"browser", &Identity{ID: "alice"}, false, roleViewer, false},
		{"brokered person", &Identity{ID: "alice"}, true, roleViewer, true},
		{"brokered site", &Identity{ID: "site:opaque", Provider: "site"}, true, roleViewer, true},
		{"local automation", &Identity{ID: "automation:task", Provider: "automation"}, false, roleViewer, false},
		{"anonymous", nil, true, roleViewer, false},
		{"no authority", &Identity{ID: "alice"}, true, roleNone, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := siteAuthorization{identity: test.identity, apiOnly: test.apiOnly, role: test.role}
			if got := auth.subjectsMatch(Subjects{Workspace: true}); got != test.allowed {
				t.Fatalf("allowed=%v", got)
			}
		})
	}
}

func TestWorkspaceSharingMergeAndRevocation(t *testing.T) {
	base := SiteAccess{Version: 2, Owners: []string{"user:owner"}}
	form := url.Values{"editor_action": {"workspace-sharing"}, "kind": {"action"}, "resource": {"submit"}, "mode": {"workspace"}}
	public, err := permissionEditorChange(base, form)
	if err != nil {
		t.Fatal(err)
	}
	if !workspaceShared(public, "action", "submit", "run") || !workspaceShared(public, "action-consumer", "submit", "run") {
		t.Fatal("action not shared")
	}
	merged, conflicts := mergePermissionPolicy(base, public, base)
	if len(conflicts) != 0 || !workspaceShared(merged, "action", "submit", "run") {
		t.Fatal("republishing lost portal sharing")
	}
	form.Set("mode", "restricted")
	private, err := permissionEditorChange(public, form)
	if err != nil {
		t.Fatal(err)
	}
	merged, conflicts = mergePermissionPolicy(public, private, public)
	if len(conflicts) != 0 || workspaceShared(merged, "action", "submit", "run") {
		t.Fatal("republishing resurrected sharing")
	}
	public.Grants["custom"] = PermissionGrant{Kind: "action", Resource: "*", Operations: []string{"run"}, Subjects: Subjects{Workspace: true}}
	if _, err := permissionEditorChange(public, form); err == nil {
		t.Fatal("restriction silently left wildcard sharing active")
	}
	base.Admission.Workspace = true
	if ValidatePublishedAccess(base) == nil {
		t.Fatal("API audience accepted as page admission")
	}
}
