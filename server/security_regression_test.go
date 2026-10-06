package hex_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/memory"
)

type pausedWithdrawalStore struct {
	*memory.IntegrationStore
	entered chan struct{}
	resume  chan struct{}
}

func (s *pausedWithdrawalStore) WithdrawIntegrationApproval(ctx context.Context, site, integration string) error {
	close(s.entered)
	select {
	case <-s.resume:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.IntegrationStore.WithdrawIntegrationApproval(ctx, site, integration)
}

func TestApprovalWithdrawalCannotRaceAdminApproval(t *testing.T) {
	for _, portal := range []bool{false, true} {
		t.Run(map[bool]string{false: "api", true: "portal"}[portal], func(t *testing.T) {
			var paused *pausedWithdrawalStore
			server, store := setupPortalIntegrations(t, true, func(config *hex.Config) {
				paused = &pausedWithdrawalStore{IntegrationStore: config.IntegrationStore.(*memory.IntegrationStore), entered: make(chan struct{}), resume: make(chan struct{})}
				config.IntegrationStore = paused
			})
			formRequest(t, server, integrationFormHeaders("owner", false), "POST", portalLedgerApproval+"/request", nil, 200)
			method, path := "DELETE", "/api/hex/sites/demo/integrations/ledger/approval"
			if portal {
				method, path = "POST", portalLedgerApproval+"/revoke"
			}
			request := httptest.NewRequest(method, path, nil)
			request.Header = integrationFormHeaders("owner", false)
			request.Header.Set("X-Hex-Request", "1")
			finished := make(chan *httptest.ResponseRecorder, 1)
			go func() { response := httptest.NewRecorder(); server.ServeHTTP(response, request); finished <- response }()
			select {
			case <-paused.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("withdrawal did not reach atomic store operation")
			}
			requestAs(t, server, integrationFormHeaders("admin", true), "PUT", "/api/hex/sites/demo/integrations/ledger/approval", nil, 200)
			close(paused.resume)
			select {
			case response := <-finished:
				if response.Code != http.StatusForbidden {
					t.Fatalf("owner revoked approved integration: %d %s", response.Code, response.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("withdrawal did not finish")
			}
			approval, err := store.GetIntegrationApproval(context.Background(), "demo", "ledger")
			if err != nil || approval.Status != hex.ApprovalApproved {
				t.Fatalf("approved record lost: %+v %v", approval, err)
			}
			requestAs(t, server, integrationFormHeaders("admin", true), "DELETE", "/api/hex/sites/demo/integrations/ledger/approval", nil, 204)
		})
	}
}

func TestAuditCSVFormulaSafetyPreservesJSON(t *testing.T) {
	store := memory.NewIntegrationAuditStore()
	if err := store.RecordIntegrationAudit(context.Background(), hex.IntegrationAuditRecord{
		ID: "formula", At: time.Now(), Site: "demo", Integration: "docs", Endpoint: "read", Caller: "user:alice", CallerName: "=1+1",
		Records: []string{"@SUM(A1)"}, Input: json.RawMessage(`"=1+1"`),
	}); err != nil {
		t.Fatal(err)
	}
	server := setupAudit(t, store)
	admin := principalHeaders("admin", "admin-group")
	response := requestAs(t, server, admin, "GET", "/admin/integration-audit?format=csv", nil, 200)
	rows, err := csv.NewReader(response.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][5] != "'=1+1" || rows[1][8] != "'@SUM(A1)" {
		t.Fatalf("unsafe export: %+v", rows)
	}
	response = requestAs(t, server, admin, "GET", "/api/hex/manage/integration-audit", nil, 200)
	if !strings.Contains(response.Body.String(), `"callerName":"=1+1"`) {
		t.Fatal("CSV protection changed stored/JSON data")
	}
}
