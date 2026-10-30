// A loopback-only browser-test fixture. No deployed identities or credentials.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/crazycatviking/hex/server/providers/local"
	"github.com/crazycatviking/hex/server/providers/memory"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	directory, err := os.MkdirTemp("", "hex-permission-portal-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	files, err := local.New(directory)
	if err != nil {
		return err
	}
	defer files.Close()
	if err := files.WriteSiteFile(ctx, "demo", "index.html", 4, bytes.NewBufferString("demo")); err != nil {
		return err
	}
	declared := `[{"name":"list-expenses","description":"Read expense records","operation":"list","collection":"expenses","version":1,"exported":true,"select":["amount"],"input":{"type":"object"},"output":{"type":"object"}}]`
	if err := files.WriteSiteFile(ctx, "demo", ".hex-actions.json", int64(len(declared)), bytes.NewBufferString(declared)); err != nil {
		return err
	}
	if err := files.WriteSiteFile(ctx, "consumer", "index.html", 8, bytes.NewBufferString("consumer")); err != nil {
		return err
	}
	policies := memory.NewAccessStore()
	if err := policies.PutSiteAccess(ctx, "consumer", hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}}); err != nil {
		return err
	}
	policy := hex.SiteAccess{Version: 2, Owners: []string{"user:owner"}, Admission: hex.Subjects{Owners: true}, Roles: map[string]hex.ApplicationRole{"reviewers": {Description: "Review project expenses"}}, Bindings: map[string]hex.RoleBinding{"alex-a": {Principal: "user:alex", Role: "reviewers", Scope: &hex.RoleScope{Kind: "project", ID: "A"}}}, Grants: map[string]hex.PermissionGrant{
		"pages":            {Kind: "path", Resource: "*", Except: []string{"/admin/"}, Operations: []string{"read"}, Subjects: hex.Subjects{Owners: true}},
		"admin":            {Kind: "path", Resource: "/admin/", Operations: []string{"read"}, Subjects: hex.Subjects{Owners: true}},
		"project-expenses": {Kind: "collection", Resource: "expenses", Operations: []string{"read", "update"}, Subjects: hex.Subjects{Roles: []string{"reviewers"}}, Scope: &hex.PermissionScope{Kind: "project", Field: "data.projectId"}, Fields: []string{"amount"}, Where: &hex.RecordCondition{All: []hex.RecordCondition{{Field: "data.status", Op: "eq", Value: json.RawMessage(`"draft"`)}, {Any: []hex.RecordCondition{{Field: "createdBy", Op: "eq", Value: json.RawMessage(`"$actor.id"`)}, {Field: "data.shared", Op: "eq", Value: json.RawMessage(`true`)}}}}}},
	}}
	// Match migrated owner policies: several broad grants should be one clear
	// audience summary, not a page full of unrelated technical rule cards.
	for kind, operations := range map[string][]string{"collection": {"read", "create", "update", "delete"}, "file": {"read", "upload", "delete"}, "channel": {"read", "send"}, "action": {"run"}, "integration": {"read", "write"}} {
		policy.Grants["existing-owner-"+kind] = hex.PermissionGrant{Kind: kind, Resource: "*", Operations: operations, Subjects: hex.Subjects{Owners: true}}
	}
	// Persist the earlier v2 shape so the browser also exercises automatic,
	// independent site-admission migration rather than just a fresh policy.
	policy.Admission.Roles = []string{"reviewers"}
	baseline := hex.CloneSiteAccess(policy)
	policy.Published = &baseline
	if err := policies.PutSiteAccess(ctx, "demo", policy); err != nil {
		return err
	}
	if err := files.WriteSiteFile(ctx, "conversion", "index.html", 4, bytes.NewBufferString("demo")); err != nil {
		return err
	}
	if err := policies.PutSiteAccess(ctx, "conversion", hex.CloneSiteAccess(policy)); err != nil {
		return err
	}
	people := memory.NewPeopleStore()
	for _, person := range []hex.Person{{ID: "owner", Name: "Site Owner", Email: "owner@example.test"}, {ID: "alex", Name: "Alex Andersen", Email: "alex@example.test"}, {ID: "bea", Name: "Bea <img src=x onerror=alert(1)>", Email: "bea@example.test"}} {
		if err := people.RememberPerson(ctx, person); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	baseURL := fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	actions := &hex.ActionRegistry{}
	if err := actions.Register("demo", hex.Action{Definition: hex.ActionDefinition{Name: "expense-summary", Description: "Read the expense summary", Version: 1, Exported: true, ReadOnly: true, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: func(context.Context, hex.ActionContext, json.RawMessage) (any, error) { return map[string]any{}, nil }}); err != nil {
		return err
	}
	platform := hex.New(hex.Config{Actions: actions, Automations: memory.NewAutomationStore(), Identity: hex.StaticIdentity{Identity: hex.Identity{ID: "owner", Name: "Site Owner"}}, Access: policies, Database: memory.NewDatabase(), Sites: files, Publisher: files, Files: files, People: people, Groups: []hex.NamedGroup{{ID: "finance", Name: "Finance"}}, SiteBaseURL: baseURL})
	eventRequest := httptest.NewRequest(http.MethodPut, baseURL+"/api/sites/demo/events/types/expense-approved", bytes.NewBufferString(`{"name":"expense-approved","description":"An expense was approved","version":1,"schema":{"type":"object"}}`))
	eventRequest.Header.Set("Content-Type", "application/json")
	eventRequest.Header.Set("X-Hex-Request", "1")
	eventResponse := httptest.NewRecorder()
	platform.ServeHTTP(eventResponse, eventRequest)
	if eventResponse.Code != http.StatusOK {
		return fmt.Errorf("define fixture event: %s", eventResponse.Body.String())
	}
	server := &http.Server{Handler: platform, ReadHeaderTimeout: 5 * time.Second}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.Serve(listener) }()
	fmt.Println(baseURL)
	select {
	case err := <-errorsCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		return server.Shutdown(shutdown)
	}
	return nil
}
