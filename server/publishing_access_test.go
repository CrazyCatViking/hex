package hex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/easyauth"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

type accessPublication struct {
	Files         []hex.SiteFile  `json:"files"`
	Access        json.RawMessage `json:"access,omitempty"`
	ReplaceAccess bool            `json:"replaceAccess,omitempty"`
}

type accessResult struct {
	Access      hex.SiteAccess `json:"access"`
	AccessDrift []string       `json:"accessDrift"`
}

// publishAccess publishes one page with an access section and returns the
// completed publication.
func publishAccess(t *testing.T, server http.Handler, headers http.Header, site, access string, replace bool) accessResult {
	t.Helper()
	files := map[string]string{"index.html": "page"}
	body := encode(t, accessPublication{Files: manifest(files), Access: migratedAccessJSON(t, access), ReplaceAccess: replace})

	response := requestAs(t, server, headers, "POST", "/api/hex/sites/"+site+"/publish", body, 200)
	var plan publishPlan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	for _, upload := range plan.Uploads {
		requestAs(t, server, headers, "PUT", upload.URL, []byte(files[upload.Path]), 204)
	}
	response = requestAs(t, server, headers, "POST", "/api/hex/sites/"+site+"/publish/complete", body, 200)
	var result accessResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func showAccess(t *testing.T, server http.Handler, headers http.Header, site string) hex.SiteAccess {
	t.Helper()
	response := requestAs(t, server, headers, "GET", "/api/hex/sites/"+site+"/access", nil, 200)
	if strings.Contains(response.Body.String(), "published") {
		t.Fatalf("the published access section leaked to clients: %s", response.Body.String())
	}
	var access hex.SiteAccess
	if err := json.Unmarshal(response.Body.Bytes(), &access); err != nil {
		t.Fatal(err)
	}
	return access
}

func setAccess(t *testing.T, server http.Handler, headers http.Header, site, access string) {
	t.Helper()
	requestAs(t, server, headers, "PUT", "/api/hex/sites/"+site+"/access", []byte(access), 200)
}

func policyMembers(access hex.SiteAccess) []string {
	if access.Admission.SignedIn {
		return nil
	}
	var principals []string
	for _, binding := range access.Bindings {
		if binding.Role == "legacy-viewer" {
			principals = append(principals, binding.Principal)
		}
	}
	slices.Sort(principals)
	return principals
}

func policyHasMembers(access hex.SiteAccess, expected ...string) bool {
	slices.Sort(expected)
	return slices.Equal(policyMembers(access), expected)
}

func policyWriteAudience(access hex.SiteAccess, resource string) string {
	for _, grant := range access.Grants {
		if grant.Kind != "collection" || grant.Resource != resource || !slices.Contains(grant.Operations, "update") {
			continue
		}
		if grant.Subjects.Owners {
			return "owners"
		}
		if slices.Contains(grant.Subjects.Roles, "legacy-editor") {
			return "editors"
		}
		if slices.Contains(grant.Subjects.Roles, "legacy-viewer") {
			return "viewers"
		}
	}
	return ""
}

func hasDrift(drift []string, fragment string) bool {
	for _, difference := range drift {
		if strings.Contains(difference, fragment) {
			return true
		}
	}
	return false
}

func TestRepublishingKeepsAccessRemovedOutsideHexJSON(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")
	hexJSON := `{"viewers":["user:owner-id","group:sales","group:support"]}`

	publishAccess(t, server, owner, "demo", hexJSON, false)
	setAccess(t, server, owner, "demo", `{"viewers":["user:owner-id","group:sales"]}`)

	result := publishAccess(t, server, owner, "demo", hexJSON, false)
	access := showAccess(t, server, owner, "demo")
	if slices.Contains(policyMembers(access), "group:support") {
		t.Fatalf("republishing re-added a viewer removed outside hex.json: %+v", access)
	}
	if !hasDrift(result.AccessDrift, "group:support") {
		t.Fatalf("the removed viewer was not reported: %v", result.AccessDrift)
	}
}

func TestRepublishingMergesHexJSONChanges(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")

	publishAccess(t, server, owner, "demo", `{"viewers":["user:owner-id","group:sales","group:support"],"collections":{"settings":{"write":"owners"}}}`, false)
	setAccess(t, server, owner, "demo", `{"viewers":["user:owner-id","group:sales","group:finance"],"collections":{"settings":{"write":"owners"}}}`)

	// hex.json adds a viewer and a channel rule; the portal's removal of
	// support and addition of finance are kept.
	publishAccess(t, server, owner, "demo", `{"viewers":["user:owner-id","group:sales","group:support","group:hr"],"collections":{"settings":{"write":"owners"}},"channels":{"chat":{"write":"viewers"}}}`, false)
	access := showAccess(t, server, owner, "demo")
	viewers := strings.Join(policyMembers(access), ",")
	if !policyHasMembers(access, "user:owner-id", "group:sales", "group:finance", "group:hr") {
		t.Fatalf("viewers were not merged: %s", viewers)
	}
	chat := false
	for _, grant := range access.Grants {
		if grant.Kind == "channel" && grant.Resource == "chat" {
			chat = true
		}
	}
	if !chat || policyWriteAudience(access, "settings") != "owners" {
		t.Fatalf("rules were not merged: %+v", access)
	}

	// hex.json removing a viewer it granted removes it from the site.
	publishAccess(t, server, owner, "demo", `{"viewers":["user:owner-id","group:support","group:hr"],"collections":{"settings":{"write":"owners"}},"channels":{"chat":{"write":"viewers"}}}`, false)
	if access := showAccess(t, server, owner, "demo"); !policyHasMembers(access, "user:owner-id", "group:finance", "group:hr") {
		t.Fatalf("a viewer removed from hex.json stayed: %v", policyMembers(access))
	}
}

func TestConflictingAccessChangesNeedReplace(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")

	publishAccess(t, server, owner, "demo", `{"collections":{"settings":{"write":"owners"}}}`, false)
	setAccess(t, server, owner, "demo", `{"collections":{"settings":{"write":"editors"}}}`)

	conflicting := `{"collections":{"settings":{"write":"viewers"}}}`
	body := encode(t, accessPublication{Files: manifest(map[string]string{"index.html": "page"}), Access: migratedAccessJSON(t, conflicting)})
	response := requestAs(t, server, owner, "POST", "/api/hex/sites/demo/publish", body, 409)
	if !strings.Contains(response.Body.String(), "grant ") || !strings.Contains(response.Body.String(), "--replace-access") {
		t.Fatalf("the conflict does not explain itself: %s", response.Body.String())
	}
	if write := policyWriteAudience(showAccess(t, server, owner, "demo"), "settings"); write != "editors" {
		t.Fatalf("a conflicting publication changed the policy: %s", write)
	}

	result := publishAccess(t, server, owner, "demo", conflicting, true)
	if write := policyWriteAudience(showAccess(t, server, owner, "demo"), "settings"); write != "viewers" || len(result.AccessDrift) != 0 {
		t.Fatalf("replacing did not apply hex.json as it is: %s %v", write, result.AccessDrift)
	}
}

func TestSitesWithoutPublishedAccessRequireExplicitReconciliation(t *testing.T) {
	store, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policies := memory.NewAccessStore()
	server := hex.New(hex.Config{Files: store, Sites: store, Publisher: store, Database: memory.NewDatabase(), Identity: easyauth.Resolver{}, Access: policies})
	owner := principalHeaders("owner-id")

	// A policy saved before publications recorded their access section,
	// whose support viewer was since removed in the portal.
	legacy := hex.SiteAccess{Owners: []string{"user:owner-id"}, Viewers: []string{"user:owner-id"}}
	if err := policies.PutSiteAccess(context.Background(), "demo", legacy); err != nil {
		t.Fatal(err)
	}

	hexJSON := `{"viewers":["user:owner-id","group:support"]}`
	policy := migratedAccessJSON(t, hexJSON)
	body := encode(t, accessPublication{Files: manifest(map[string]string{"index.html": "page"}), Access: policy})
	response := requestAs(t, server, owner, "POST", "/api/hex/sites/demo/publish", body, 409)
	if !strings.Contains(response.Body.String(), "no recorded local permission baseline") {
		t.Fatal(response.Body.String())
	}
	requestAs(t, server, owner, "POST", "/api/hex/sites/demo/access/baseline", policy, 200)
	result := publishAccess(t, server, owner, "demo", hexJSON, false)
	if access := showAccess(t, server, owner, "demo"); !policyHasMembers(access, "user:owner-id") {
		t.Fatalf("the first publication changed a legacy policy: %v", policyMembers(access))
	}
	if !hasDrift(result.AccessDrift, "group:support") {
		t.Fatalf("the difference was not reported: %v", result.AccessDrift)
	}

	// Later changes to hex.json apply.
	publishAccess(t, server, owner, "demo", `{"viewers":["user:owner-id","group:support","group:hr"]}`, false)
	if access := showAccess(t, server, owner, "demo"); !policyHasMembers(access, "user:owner-id", "group:hr") {
		t.Fatalf("a later hex.json change was not merged: %v", policyMembers(access))
	}
}

func TestAccessAddedToHexJSONLaterApplies(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")

	publishSite(t, server, owner, "demo", map[string]string{"index.html": "v1"}, "")
	setAccess(t, server, owner, "demo", `{"editors":["group:team"]}`)
	publishAccess(t, server, owner, "demo", `{"paths":[{"prefix":"/admin/","viewers":"owners"}]}`, false)

	access := showAccess(t, server, owner, "demo")
	team := false
	admin := false
	for _, binding := range access.Bindings {
		if binding.Role == "legacy-editor" && binding.Principal == "group:team" {
			team = true
		}
	}
	for _, grant := range access.Grants {
		if grant.Kind == "path" && grant.Resource == "/admin/" {
			admin = true
		}
	}
	if !admin || !team {
		t.Fatalf("an access section added later was not merged: %+v", access)
	}
}

func TestOpeningAndRestrictingOnBothSidesConflicts(t *testing.T) {
	server, _ := setupWithAccess(t)
	owner := principalHeaders("owner-id")

	publishAccess(t, server, owner, "demo", `{"viewers":["user:owner-id","group:sales"]}`, false)
	// Everyone signed in may view after this portal change.
	setAccess(t, server, owner, "demo", `{"viewers":[]}`)

	body := encode(t, accessPublication{Files: manifest(map[string]string{"index.html": "page"}), Access: migratedAccessJSON(t, `{"viewers":["user:owner-id","group:sales","group:hr"]}`)})
	requestAs(t, server, owner, "POST", "/api/hex/sites/demo/publish", body, 409)
	if access := showAccess(t, server, owner, "demo"); !access.Admission.SignedIn {
		t.Fatalf("a conflicting publication restricted an open site: %+v", access.Admission)
	}
}
