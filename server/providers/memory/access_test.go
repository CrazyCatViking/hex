package memory

import (
	"context"
	"errors"
	"testing"

	hex "github.com/crazycatviking/hex/server"
)

func TestRejectedPolicyUpdateIsDetached(t *testing.T) {
	ctx := context.Background()
	store := NewAccessStore()
	policy := hex.SiteAccess{Owners: []string{"user:owner"}}
	if err := store.PutSiteAccess(ctx, "demo", policy); err != nil {
		t.Fatal(err)
	}
	err := store.UpdateSiteAccess(ctx, "demo", func(current hex.SiteAccess, _ bool) (*hex.SiteAccess, error) {
		current.Owners[0] = "user:attacker"
		return &current, hex.ErrForbidden
	})
	if !errors.Is(err, hex.ErrForbidden) {
		t.Fatalf("expected rejected update: %v", err)
	}
	stored, err := store.GetSiteAccess(ctx, "demo")
	if err != nil || stored.Owners[0] != "user:owner" {
		t.Fatalf("rejected update changed policy: %+v, %v", stored, err)
	}
}
