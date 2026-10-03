package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestPostgresIntegrationBudgetsAreSharedAndAtomic(t *testing.T) {
	connection := os.Getenv("HEX_TEST_POSTGRES_URL")
	if connection == "" {
		t.Skip("set HEX_TEST_POSTGRES_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := New(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	user := "integration-test-" + rand.Text()
	defer func() {
		if _, err := database.pool.Exec(context.Background(), `DELETE FROM hex_tool_calls WHERE user_id=$1`, user); err != nil {
			t.Error(err)
		}
	}()
	budget := hex.IntegrationBudget{CallsPerHour: 3, OutputBytesPerHour: 1000, RecordsPerHour: 10, ConcurrentCalls: 2}
	at := time.Now().UTC()
	results := make(chan struct {
		audit hex.ToolCallAudit
		err   error
	}, 12)
	var workers sync.WaitGroup
	for index := range 12 {
		workers.Go(func() {
			store := database
			if index%2 == 0 {
				store = second
			}
			audit := hex.ToolCallAudit{ID: rand.Text(), UserID: user, Tool: "summary", Bundle: "engineering", StartedAt: at, LeaseUntil: at.Add(time.Minute), Status: "pending"}
			err := store.ReserveToolCall(ctx, hex.ToolCallReservation{Audit: audit, Budget: budget, OutputBytes: 100, Records: 3})
			results <- struct {
				audit hex.ToolCallAudit
				err   error
			}{audit, err}
		})
	}
	workers.Wait()
	close(results)
	succeeded := []hex.ToolCallAudit{}
	for result := range results {
		if result.err == nil {
			succeeded = append(succeeded, result.audit)
		} else if !errors.Is(result.err, hex.ErrIntegrationBudget) {
			t.Fatal(result.err)
		}
	}
	if len(succeeded) != 2 {
		t.Fatalf("concurrent reservations escaped shared limit: %d", len(succeeded))
	}
	for _, audit := range succeeded {
		audit.Status, audit.OutputBytes, audit.Records = "succeeded", 25, 1
		if err := database.FinishToolCall(ctx, audit); err != nil {
			t.Fatal(err)
		}
	}
	third := hex.ToolCallAudit{ID: rand.Text(), UserID: user, Tool: "different-tool", Bundle: "different-bundle", StartedAt: time.Now().UTC(), LeaseUntil: at.Add(time.Minute), Status: "pending"}
	if err := second.ReserveToolCall(ctx, hex.ToolCallReservation{Audit: third, Budget: budget, OutputBytes: 100, Records: 3}); err != nil {
		t.Fatal(err)
	}
	fourth := third
	fourth.ID = rand.Text()
	if err := database.ReserveToolCall(ctx, hex.ToolCallReservation{Audit: fourth, Budget: budget, OutputBytes: 100, Records: 3}); !errors.Is(err, hex.ErrIntegrationBudget) {
		t.Fatalf("hourly calls were not shared: %v", err)
	}
	logs, err := second.ListToolAudit(ctx, hex.ToolAuditQuery{UserID: user})
	if err != nil || len(logs) != 3 {
		t.Fatalf("audit records did not persist across instances: %+v %v", logs, err)
	}
}
