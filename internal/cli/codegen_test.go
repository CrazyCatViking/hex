package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const codegenCatalog = `[
  {
    "name": "crm", "title": "CRM", "description": "Deals and companies.", "requiresApproval": true,
    "endpoints": [
      {
        "name": "deals", "description": "Open deals.", "permission": "crm.deals", "write": false,
        "inputSchema": {"type": "object", "properties": {
          "stage": {"type": "string", "enum": ["open", "won", "lost"], "description": "Deal stage."},
          "limit": {"type": "integer"}
        }, "additionalProperties": false},
        "outputSchema": {"type": "object", "properties": {
          "deals": {"type": "array", "items": {"type": "object", "properties": {
            "id": {"type": "string"}, "amount": {"type": ["number", "null"]},
            "x-source": {"const": "crm"},
            "labels": {"type": "object", "additionalProperties": {"type": "string"}}
          }, "required": ["id", "amount"]}},
          "next": {"oneOf": [{"type": "string"}, {"type": "null"}]}
        }, "required": ["deals"]}
      },
      {
        "name": "update-deal", "description": "Change a deal.", "permission": "crm.write", "write": true,
        "inputSchema": {"type": "object", "properties": {"id": {"type": "string"}}, "required": ["id"]},
        "outputSchema": {"type": "object", "properties": {"ok": {"type": "boolean"}}, "required": ["ok"]}
      }
    ]
  },
  {
    "name": "product-board", "title": "Products", "connector": "pb",
    "endpoints": [
      {
        "name": "tree", "description": "The product tree.", "permission": "product-board.tree",
        "inputSchema": {"type": "object"},
        "outputSchema": {
          "type": "object",
          "$defs": {"node": {"type": "object", "properties": {
            "name": {"type": "string"}, "children": {"type": "array", "items": {"$ref": "#/$defs/node"}}
          }, "required": ["name"]}},
          "properties": {"roots": {"type": "array", "items": {"$ref": "#/$defs/node"}}},
          "required": ["roots"]
        }
      }
    ]
  }
]`

func TestGeneratedIntegrationModule(t *testing.T) {
	var catalog []catalogIntegration
	if err := json.Unmarshal([]byte(codegenCatalog), &catalog); err != nil {
		t.Fatal(err)
	}
	generated, err := generateIntegrationModule(catalog, nil)
	if err != nil {
		t.Fatal(err)
	}
	module := string(generated)
	for _, expected := range []string{
		"export interface IntegrationCaller {",
		"export interface CrmDealsInput {",
		"  /** Deal stage. */\n  stage?: \"open\" | \"won\" | \"lost\";",
		"  limit?: number;",
		"    amount: number | null;",
		"    \"x-source\"?: \"crm\";",
		"    labels?: Record<string, string>;",
		"  next?: string | null;",
		"export interface ProductBoardTreeOutputNode {\n  children?: ProductBoardTreeOutputNode[];\n  name: string;\n}",
		"  roots: ProductBoardTreeOutputNode[];",
		"export type ProductBoardTreeInput = Record<string, unknown>;",
		"      deals: (input: CrmDealsInput = {}) =>\n        integrations.call<CrmDealsOutput>(\"crm\", \"deals\", input),",
		"      updateDeal: (input: CrmUpdateDealInput) =>",
		"Permission: crm.write (changes data; needs the site's editor role)",
		"Needs a platform admin to approve each site.",
		"Calls with the viewer's own connected pb account.",
		"    productBoard: {",
	} {
		if !strings.Contains(module, expected) {
			t.Errorf("the module lacks %q:\n%s", expected, module)
		}
	}

	again, err := generateIntegrationModule(catalog, nil)
	if err != nil || string(again) != module {
		t.Fatal("generation is not deterministic")
	}

	only, err := generateIntegrationModule(catalog, []string{"crm.deals"})
	if err != nil || strings.Contains(string(only), "updateDeal") || strings.Contains(string(only), "productBoard") {
		t.Fatalf("--only did not limit the module: %v\n%s", err, only)
	}
	if _, err := generateIntegrationModule(catalog, []string{"crm.*.x"}); err == nil {
		t.Fatal("accepted an invalid pattern")
	}
	if _, err := generateIntegrationModule(catalog, []string{"chat.*"}); err == nil {
		t.Fatal("generated a module without endpoints")
	}
}

func TestCodegenCommand(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "catalog.json"), []byte(codegenCatalog), 0644); err != nil {
		t.Fatal(err)
	}
	output := run(t, directory, "integrations", "codegen", "--catalog", "catalog.json", "--out", "src/hex-integrations.ts")
	if !strings.Contains(output, "Wrote src/hex-integrations.ts") {
		t.Fatal(output)
	}
	if checked := run(t, directory, "integrations", "codegen", "--catalog", "catalog.json", "--out", "src/hex-integrations.ts", "--check"); !strings.Contains(checked, "up to date") {
		t.Fatal(checked)
	}

	path := filepath.Join(directory, "src", "hex-integrations.ts")
	if err := os.WriteFile(path, []byte("// edited\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err, _ := runFailing(t, directory, "integrations", "codegen", "--catalog", "catalog.json", "--out", "src/hex-integrations.ts", "--check")
	if !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("--check accepted a stale file: %v", err)
	}
	if err, _ := runFailing(t, directory, "integrations", "codegen", "--catalog", "catalog.json", "--check"); !strings.Contains(err.Error(), "--out") {
		t.Fatalf("--check without --out: %v", err)
	}

	printed := run(t, directory, "integrations", "codegen", "--catalog", "catalog.json", "--only", "product-board.*")
	if !strings.Contains(printed, "ProductBoardTreeOutput") || strings.Contains(printed, "CrmDeals") {
		t.Fatalf("unexpected standard output: %s", printed)
	}
}

func TestCodegenFromPlatform(t *testing.T) {
	directory := t.TempDir()
	var calls atomic.Int32
	platform := startIntegrationPlatform(t, &calls)
	saveReadProfile(t, directory, platform.URL)

	module := run(t, directory, "integrations", "codegen")
	if !strings.Contains(module, `integrations.call<CrmDealsOutput>("crm", "deals", input)`) || calls.Load() != 0 {
		t.Fatalf("unexpected module from the platform: %s", module)
	}
}
