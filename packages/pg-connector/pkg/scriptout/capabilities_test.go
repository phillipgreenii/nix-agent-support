package scriptout

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// noopHandle is a minimal OpHandler.Handle stand-in for tests that only
// care about which op names are registered in a table, never about what
// running them actually does.
func noopHandle(context.Context, json.RawMessage) (any, error) { return nil, nil }

func TestDispatchTable_Ops_ReflectsRegisteredKeysSorted(t *testing.T) {
	table := DispatchTable{
		"show":         {},
		"categorize":   {},
		"capabilities": {},
	}
	got := table.Ops()
	want := []string{"capabilities", "categorize", "show"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Ops() = %v, want %v", got, want)
	}
}

func TestDispatchTable_Ops_Empty(t *testing.T) {
	table := DispatchTable{}
	got := table.Ops()
	if len(got) != 0 {
		t.Fatalf("Ops() = %v, want empty", got)
	}
}

// mustHandleCapabilities invokes table's own capabilities entry (as
// ServeLoop would) and returns the resulting CapabilitiesResponse.
func mustHandleCapabilities(t *testing.T, table DispatchTable) CapabilitiesResponse {
	t.Helper()
	entry, ok := table[OpCapabilities]
	if !ok {
		t.Fatal("capabilities entry missing from table")
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp, ok := result.(CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T, want CapabilitiesResponse", result)
	}
	return resp
}

func containsOp(ops []string, op string) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}

// TestAddCapabilities_OpsIsComputedFromTable is the mechanical proof bead
// pg2-fh2vh exists to establish: capabilities.ops is no longer a
// hand-typed literal a backend author must remember to keep in sync with
// its own dispatch table — it is a computed property of the table itself,
// so there is no second place left to drift.
//
// This simulates two backends built the exact same way real Tier-2 backend
// main.go files build theirs: one that wires both "show" and "categorize"
// into its dispatch table, and one that (representing the bug this bead
// fixes) never registers "categorize" at all — the exact mistake a new
// backend author could make. Neither backend's capabilitiesResponse-style
// call site ever writes an Ops literal; AddCapabilities computes it from
// whatever the table actually holds.
func TestAddCapabilities_OpsIsComputedFromTable(t *testing.T) {
	complete := DispatchTable{
		"show":       {Handle: noopHandle},
		"categorize": {Handle: noopHandle},
	}
	AddCapabilities(complete, 1, CapabilitiesResponse{ProtocolVersion: ProtocolVersion})
	completeResp := mustHandleCapabilities(t, complete)
	if !containsOp(completeResp.Ops, "categorize") {
		t.Fatalf("complete backend's Ops = %v, want it to include categorize (it was wired into the table)", completeResp.Ops)
	}
	if !containsOp(completeResp.Ops, "show") {
		t.Fatalf("complete backend's Ops = %v, want it to include show", completeResp.Ops)
	}
	if !containsOp(completeResp.Ops, OpCapabilities) {
		t.Fatalf("complete backend's Ops = %v, want it to include capabilities itself", completeResp.Ops)
	}

	// forgot represents a backend author who wired "show" but forgot to
	// register "categorize" in the dispatch table. Nothing about
	// AddCapabilities' call site changes between this table and complete's
	// above — the only difference is what's actually registered.
	forgot := DispatchTable{
		"show": {Handle: noopHandle},
	}
	AddCapabilities(forgot, 1, CapabilitiesResponse{ProtocolVersion: ProtocolVersion})
	forgotResp := mustHandleCapabilities(t, forgot)
	if containsOp(forgotResp.Ops, "categorize") {
		t.Fatalf("forgot backend's Ops = %v, must NOT claim categorize: it was never wired into the dispatch table", forgotResp.Ops)
	}
	if !containsOp(forgotResp.Ops, "show") {
		t.Fatalf("forgot backend's Ops = %v, want it to still include show", forgotResp.Ops)
	}
}

// TestAddCapabilities_PanicsOnHandTypedOps guards the startup-time half of
// the fix: a call site that regresses to hand-typing an Ops literal (the
// exact pattern this bead removes from all four backends) must fail loudly
// at dispatch-table-build time, not silently ship a second, driftable list.
func TestAddCapabilities_PanicsOnHandTypedOps(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected AddCapabilities to panic when resp.Ops is set, it did not")
		}
	}()
	AddCapabilities(DispatchTable{}, 1, CapabilitiesResponse{Ops: []string{"show"}})
}
