package beadsbridge

import (
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// TestNoWIPFieldInBeadsBridgeWritePath is a mechanical guard for the
// operator ruling (pg2-4dz88.4, restated as an explicit acceptance
// criterion on pg2-4dz88.4.4): WIP is store-only and is NEVER synced to
// beads.
//
// store.PRPayload is the event payload this bridge's handleOne (bridge.go)
// consumes, and beads.MergeRequestFields is the type ReconcileMergeRequest
// writes through to project the merge-request bead — together they are
// the ENTIRE beads-bridge write path for a PR. Asserting via reflection
// that neither struct carries any field or JSON tag mentioning WIP proves
// the negative mechanically: a future change cannot silently wire WIP
// through this path without this test failing first, rather than relying
// on inspection staying correct forever.
func TestNoWIPFieldInBeadsBridgeWritePath(t *testing.T) {
	assertNoWIPField(t, reflect.TypeOf(store.PRPayload{}))
	assertNoWIPField(t, reflect.TypeOf(beads.MergeRequestFields{}))
}

func assertNoWIPField(t *testing.T, typ reflect.Type) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "wip") {
			t.Errorf("%s.%s: field name mentions WIP; WIP MUST stay store-only and never be synced to beads", typ.Name(), f.Name)
		}
		if tag := f.Tag.Get("json"); strings.Contains(strings.ToLower(tag), "wip") {
			t.Errorf("%s.%s: json tag %q mentions WIP; WIP MUST stay store-only and never be synced to beads", typ.Name(), f.Name, tag)
		}
	}
}
