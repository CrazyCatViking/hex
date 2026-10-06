package hex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type hardeningDatabase struct {
	Database
	data     json.RawMessage
	err      error
	reads    int
	bytes    int
	deadline time.Duration
}

func (d *hardeningDatabase) List(ctx context.Context, _, _ string, options ListOptions) ([]Document, error) {
	if deadline, ok := ctx.Deadline(); ok {
		d.deadline = time.Until(deadline)
	}
	if d.err != nil {
		return nil, d.err
	}
	if options.Limit != 1 || options.MaxDocumentBytes <= 0 {
		return nil, errors.New("unbounded provider read")
	}
	if len(d.data) > options.MaxDocumentBytes {
		return nil, ErrDocumentReadLimit
	}
	d.reads++
	d.bytes += len(d.data)
	return []Document{{ID: fmt.Sprintf("doc-%06d", d.reads), Data: d.data}}, nil
}

func (d *hardeningDatabase) Put(context.Context, string, string, string, json.RawMessage, WriteOptions) (Document, error) {
	return Document{}, d.err
}

func hardeningRunner(database Database, source string) *automationRunner {
	return &automationRunner{server: New(Config{Database: database}), site: "demo", automation: Automation{Name: "review", Script: &AutomationScript{Source: source}}, run: &AutomationRun{ID: "review", StartedAt: time.Now()}, caller: integrationCaller{site: "demo", automation: "review", role: roleOwner}}
}

func TestAutomationHostReadBudgetsCannotBeCaught(t *testing.T) {
	for _, padding := range []int{0, 32 << 10, 2 << 20} {
		data, err := json.Marshal(map[string]any{"match": false, "padding": strings.Repeat("x", padding)})
		if err != nil {
			t.Fatal(err)
		}
		database := &hardeningDatabase{data: data}
		runner := hardeningRunner(database, `export default async hex => { try { await hex.db.query("values",{where:{match:true},limit:1}); } catch {} return "caught"; };`)
		_, err = runner.executeScript(t.Context())
		if !errors.Is(err, errAutomationHostBudget) {
			t.Fatalf("caught host limit did not fail run: %v", err)
		}
		if database.reads > maxScannedDocuments || database.bytes > maxScannedBytes {
			t.Fatalf("budget exceeded before decode: documents=%d bytes=%d", database.reads, database.bytes)
		}
		if database.deadline <= 0 || database.deadline > hostOperationTimeout {
			t.Fatalf("missing host deadline: %s", database.deadline)
		}
	}
}

func TestAutomationProviderErrorsAreRedacted(t *testing.T) {
	const secret = "private-provider-detail=canary"
	database := &hardeningDatabase{err: errors.New(secret)}
	runner := hardeningRunner(database, `export default async hex => {
	  const messages=[];
	  for (const call of [()=>hex.db.query("values"),()=>hex.db.save("values",{})]) {
	    try { await call(); } catch(error) { messages.push(error.message); }
	  }
	  return messages;
	};`)
	output, err := runner.executeScript(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	trace, err := json.Marshal(runner.run.Operations)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output)+string(trace), secret) || !strings.Contains(string(output), "platform log") {
		t.Fatalf("provider error leaked: %s %s", output, trace)
	}
}

type hardeningMarshaler struct{ called *bool }

func (m hardeningMarshaler) MarshalJSON() ([]byte, error) { *m.called = true; return []byte(`{}`), nil }

func TestAutomationResultsBoundBeforeMarshaling(t *testing.T) {
	ctx := context.WithValue(t.Context(), automationResultLimitKey{}, true)
	if _, err := marshalHostResult(ctx, map[string]any{"data": strings.Repeat("x", maxAutomationOutput+1)}); err == nil {
		t.Fatal("oversized result accepted")
	}
	called := false
	if _, err := marshalHostResult(ctx, hardeningMarshaler{called: &called}); err == nil || called {
		t.Fatal("custom marshaler executed in bounded path")
	}
	if _, err := marshalHostResult(t.Context(), hardeningMarshaler{called: &called}); err != nil || !called {
		t.Fatal("ordinary HTTP marshaling contract changed")
	}
	if _, err := marshalHostResult(ctx, map[string]any{"time": time.Now(), "raw": json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationValidationAdmissionAndCancellation(t *testing.T) {
	for range cap(validationAdmissions) {
		validationAdmissions <- struct{}{}
	}
	defer func() {
		for range cap(validationAdmissions) {
			<-validationAdmissions
		}
	}()
	script := AutomationScript{Source: `export default () => 42;`}
	if err := validateAutomationScript(t.Context(), script); !errors.Is(err, ErrAutomationValidationBusy) {
		t.Fatalf("full admission queue not rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateAutomationsContext(ctx, []Automation{{Name: "cancelled", Script: &script}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation ignored: %v", err)
	}
}
