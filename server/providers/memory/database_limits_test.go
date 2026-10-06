package memory

import (
	"encoding/json"
	"errors"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestListRejectsOversizedDocumentBeforeCopy(t *testing.T) {
	database := NewDatabase()
	if _, err := database.Put(t.Context(), "demo", "values", "one", json.RawMessage(`{"value":"larger than the budget"}`), hex.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.List(t.Context(), "demo", "values", hex.ListOptions{Limit: 1, MaxDocumentBytes: 8}); !errors.Is(err, hex.ErrDocumentReadLimit) {
		t.Fatalf("read limit not enforced: %v", err)
	}
	page, err := database.List(t.Context(), "demo", "values", hex.ListOptions{Limit: 1})
	if err != nil || len(page) != 1 {
		t.Fatalf("ordinary listing changed: %+v %v", page, err)
	}
}
