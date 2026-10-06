package hex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSharedIntegrationCallSuppressesDryRunWrites(t *testing.T) {
	registry := new(IntegrationRegistry)
	calls := 0
	if err := registry.Register(Integration{
		Name: "records", Title: "Records", Endpoints: []IntegrationEndpoint{{
			Name: "write", Description: "Change records.", Write: true,
			InputSchema:  json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`),
			OutputSchema: json.RawMessage(`{"type":"string"}`),
			Handler: func(context.Context, IntegrationCall, json.RawMessage) (any, error) {
				calls++
				return "real result", nil
			},
		}, {
			Name: "read", Description: "Read records.",
			InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"string"}`),
			Handler: func(context.Context, IntegrationCall, json.RawMessage) (any, error) {
				calls++
				return "read result", nil
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Config{Integrations: registry, IntegrationGrants: []IntegrationGrant{{Principal: "*", Permissions: []string{"*"}}}})
	endpoint := registry.endpoint("records", "write")
	caller := integrationCaller{site: "demo", automation: "test", dryRun: true}
	output, cached, err := server.callIntegration(context.Background(), caller, endpoint, json.RawMessage(`{"name":"example"}`))
	if err != nil || cached || calls != 0 {
		t.Fatalf("dry-run called the real handler or validated the simulation as a string: %s %t %v (%d calls)", output, cached, err, calls)
	}
	var simulated struct {
		WouldCall string          `json:"wouldCall"`
		Input     json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(output, &simulated); err != nil || simulated.WouldCall != "records.write" || string(simulated.Input) != `{"name":"example"}` {
		t.Fatalf("unexpected simulation: %s, %v", output, err)
	}
	if _, _, err := server.callIntegration(context.Background(), caller, endpoint, json.RawMessage(`{}`)); err == nil || calls != 0 {
		t.Fatal("dry-run must still validate input")
	}
	denied := New(Config{Integrations: registry})
	if _, _, err := denied.callIntegration(context.Background(), caller, endpoint, json.RawMessage(`{"name":"example"}`)); err == nil {
		t.Fatal("dry-run bypassed integration grants")
	}
	output, _, err = server.callIntegration(context.Background(), caller, registry.endpoint("records", "read"), json.RawMessage(`{}`))
	if err != nil || string(output) != `"read result"` || calls != 1 {
		t.Fatalf("dry-run read was not executed: %s %v (%d calls)", output, err, calls)
	}
	caller.dryRun = false
	if _, _, err := server.callIntegration(context.Background(), caller, endpoint, json.RawMessage(`{"name":"example"}`)); err != nil || calls != 2 {
		t.Fatalf("live automation failed to execute: %v (%d calls)", err, calls)
	}
	caller = integrationCaller{site: "demo", identity: &Identity{ID: "person"}, role: roleEditor, dryRun: true}
	if _, _, err := server.callIntegration(context.Background(), caller, endpoint, json.RawMessage(`{"name":"example"}`)); err != nil || calls != 3 {
		t.Fatalf("person call incorrectly simulated: %v (%d calls)", err, calls)
	}
}

type regressionAuditContextKey struct{}

type regressionAuditStore struct {
	records []IntegrationAuditRecord
	err     error
}

func (s *regressionAuditStore) RecordIntegrationAudit(ctx context.Context, record IntegrationAuditRecord) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) > integrationAuditWriteTimeout || time.Until(deadline) <= 0 {
		return errors.New("audit context was not bounded")
	}
	if ctx.Value(regressionAuditContextKey{}) != "request-value" {
		return errors.New("audit context lost request values")
	}
	s.records = append(s.records, record)
	return s.err
}

func (*regressionAuditStore) ListIntegrationAudit(context.Context, IntegrationAuditFilter) ([]IntegrationAuditRecord, error) {
	return nil, nil
}

func (*regressionAuditStore) DeleteIntegrationAuditBefore(context.Context, time.Time) (int, error) {
	return 0, nil
}

func TestCompletedIntegrationAuditSurvivesRequestCancellation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), regressionAuditContextKey{}, "request-value"))
			defer cancel()
			registry := new(IntegrationRegistry)
			store := new(regressionAuditStore)
			if err := registry.Register(Integration{
				Name: "records", Title: "Records", Audit: true, Endpoints: []IntegrationEndpoint{{
					Name: "write", Description: "Change records.", Write: true,
					InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`),
					Handler: func(context.Context, IntegrationCall, json.RawMessage) (any, error) {
						cancel()
						if failed {
							return nil, context.Canceled
						}
						return map[string]bool{"written": true}, nil
					},
				}},
			}); err != nil {
				t.Fatal(err)
			}
			server := New(Config{Integrations: registry, IntegrationAudit: store,
				IntegrationGrants: []IntegrationGrant{{Principal: "*", Permissions: []string{"*"}}},
			})
			caller := integrationCaller{site: "demo", automation: "test"}
			_, _, err := server.callIntegration(ctx, caller, registry.endpoint("records", "write"), json.RawMessage(`{}`))
			if failed && !errors.Is(err, context.Canceled) || !failed && err != nil {
				t.Fatalf("unexpected call error: %v", err)
			}
			if len(store.records) != 1 || store.records[0].Failed != failed {
				t.Fatalf("completed call was not audited: %+v", store.records)
			}
		})
	}
}
