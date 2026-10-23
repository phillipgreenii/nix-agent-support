package entity

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// The reverse lookup of the shared query.Aliases: the reference recorded for an
// id comes back as the wire string in the recorded spelling, and an id with no
// alias is reported as not found rather than as an error.
func TestIdentityAliases_RefReturnsTheRecordedReferenceOrNotFound(t *testing.T) {
	ctx := context.Background()
	id := NewIdentity(openStore(t), governor.NewManualClock(t0))
	al := NewAliases(id, nil)

	if ref, ok, err := al.Ref(ctx, "PR_a"); err != nil || ok || ref != "" {
		t.Errorf("Ref of an id with no alias = %q %v %v, want empty, not found, no error", ref, ok, err)
	}
	if err := al.Put(ctx, "PR_a", "Acme-Org/Widget#3"); err != nil {
		t.Fatal(err)
	}
	if ref, ok, err := al.Ref(ctx, "PR_a"); err != nil || !ok || ref != "Acme-Org/Widget#3" {
		t.Errorf("Ref of PR_a = %q %v %v, want Acme-Org/Widget#3 found", ref, ok, err)
	}
	// A rename is visible through the reverse lookup.
	if err := al.Put(ctx, "PR_a", "Acme-Org/Gadget#3"); err != nil {
		t.Fatal(err)
	}
	if ref, _, _ := al.Ref(ctx, "PR_a"); ref != "Acme-Org/Gadget#3" {
		t.Errorf("Ref after a rename = %q, want Acme-Org/Gadget#3", ref)
	}
}
