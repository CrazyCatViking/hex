package automationjs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidationQueueHonorsCallerCancellation(t *testing.T) {
	validationSlots <- struct{}{}
	defer func() { <-validationSlots }()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Validate(ctx, `export default () => 42;`, "cancelled.js") }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected cancellation result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled validation kept waiting")
	}
}

func TestScriptRuntime(t *testing.T) {
	input := json.RawMessage(`{"site":"demo","run":{"dryRun":false}}`)
	calls := 0
	output, err := Execute(context.Background(), `export default async function(hex) {
	  let total = 0;
	  for (let i = 0; i < 3; i++) total += i;
	  const result = await hex.call("example.read", {total});
	  return {result, site: hex.site, available: [typeof fetch, typeof process, typeof require, typeof std, typeof os]};
	}`, "report.js", input, func(_ context.Context, operation string, input json.RawMessage) (any, error) {
		calls++
		if operation != "call" || string(input) != `{"name":"example.read","input":{"total":3}}` {
			t.Fatalf("unexpected call: %s %s", operation, input)
		}
		return "answer", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || string(output) != `{"result":"answer","site":"demo","available":["undefined","undefined","undefined","undefined","undefined"]}` {
		t.Fatalf("unexpected result: %s calls=%d", output, calls)
	}
}

func TestScriptValidationDoesNotExecute(t *testing.T) {
	if err := Validate(context.Background(), `throw new Error("must not run"); export default () => 1;`, "test.js"); err != nil {
		t.Fatal(err)
	}
	if err := Validate(context.Background(), `export default ( => 1;`, "broken.js"); err == nil || !strings.Contains(err.Error(), "broken.js") {
		t.Fatalf("syntax error lost filename: %v", err)
	}
	if err := Validate(context.Background(), `import * as os from "qjs:os"; export default () => os;`, "test.js"); err == nil {
		t.Fatal("imports were allowed")
	}
	_, err := Execute(context.Background(), `export default () => import("qjs:std");`, "import.js", json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("dynamic imports were allowed")
	}
}

func TestScriptHostCallCancellationAndOutputBounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Execute(ctx, `export default hex => hex.call("example.read");`, "wait.js", json.RawMessage(`{}`), func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err == nil {
		t.Fatal("host operation ignored the run deadline")
	}
	_, err = Execute(context.Background(), `export default hex => hex.call("example.read");`, "output.js", json.RawMessage(`{}`), func(context.Context, string, json.RawMessage) (any, error) {
		return strings.Repeat("x", MaxJSONBytes+1), nil
	})
	if err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("host output was unbounded: %v", err)
	}
}

func TestScriptComputationBudget(t *testing.T) {
	_, err := Execute(context.Background(), `export default () => { while (true) {} };`, "budget.js", json.RawMessage(`{}`), nil)
	if err == nil || !strings.Contains(err.Error(), "computation budget") {
		t.Fatalf("script without a parent deadline escaped its computation budget: %v", err)
	}
}

func TestScriptCancellationAndMemoryIsolation(t *testing.T) {
	for _, source := range []string{
		`export default () => { while (true) {} };`,
		`export default async () => { while (true) await Promise.resolve(); };`,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := Execute(ctx, source, "loop.js", json.RawMessage(`{}`), nil)
		cancel()
		if err == nil {
			t.Fatal("runaway script was not cancelled")
		}
	}
	_, err := Execute(context.Background(), `export default () => new Uint8Array(128 * 1024 * 1024);`, "memory.js", json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("memory limit was not enforced")
	}
	for range 2 {
		output, err := Execute(context.Background(), `export default () => { globalThis.counter = (globalThis.counter || 0) + 1; return counter; };`, "fresh.js", json.RawMessage(`{}`), nil)
		if err != nil || string(output) != "1" {
			t.Fatalf("runtime leaked state: %s %v", output, err)
		}
	}
}
