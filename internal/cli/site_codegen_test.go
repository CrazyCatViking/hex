package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestSiteActionCodegenCompilesAndPinsVersions(t *testing.T) {
	definitions := []hex.ActionDefinition{
		{Name: "reserve-stock", Description: "Reserve stock", Exported: true, Version: 3, InputSchema: json.RawMessage(`{"type":"object","properties":{"quantity":{"type":"integer"}},"required":["quantity"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		{Name: "private", Description: "Not exported"},
	}
	generated, err := generateSiteActionModule("inventory", definitions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(generated), "private") || !strings.Contains(string(generated), `version: 3`) {
		t.Fatal(string(generated))
	}
	compiler := filepath.Join("..", "..", "node_modules", "typescript", "bin", "tsc")
	if _, err := os.Stat(compiler); err != nil {
		t.Skip("npm dependencies are not installed")
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "api.ts"), generated, 0600); err != nil {
		t.Fatal(err)
	}
	usage := `import {typedSiteAPI, SiteAPICaller} from "./api";
declare const sites: SiteAPICaller;
const api = typedSiteAPI(sites);
api["reserve-stock"]({quantity: 2}, {idempotencyKey:"one"}).then(value => value.id.toUpperCase());
// @ts-expect-error Mutations require an idempotency key.
api["reserve-stock"]({quantity:2});
// @ts-expect-error Schema input types are enforced.
api["reserve-stock"]({quantity:"two"}, {idempotencyKey:"one"});
`
	path := filepath.Join(directory, "use.ts")
	if err := os.WriteFile(path, []byte(usage), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("node", compiler, "--strict", "--noEmit", "--target", "ES2022", path).CombinedOutput()
	if err != nil {
		t.Fatalf("generated client does not typecheck: %s %v", output, err)
	}
}
