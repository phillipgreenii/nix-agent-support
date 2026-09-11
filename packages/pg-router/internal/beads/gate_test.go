package beads

import (
	"context"
	"strings"
	"testing"
)

// scriptRunner scripts bd responses keyed by the first one or two args (e.g.
// "gate create", "create", "dep add"), recording every invocation.
type scriptRunner struct {
	responses map[string]string
	errOn     map[string]error
	calls     [][]string
}

func (s *scriptRunner) Run(_ context.Context, args ...string) (string, error) {
	s.calls = append(s.calls, args)
	for _, key := range scriptKeys(args) {
		if s.errOn != nil {
			if e, ok := s.errOn[key]; ok {
				return "", e
			}
		}
		if s.responses != nil {
			if out, ok := s.responses[key]; ok {
				return out, nil
			}
		}
	}
	return "", nil
}

// scriptKeys returns the two-arg key first (more specific) then the one-arg key.
func scriptKeys(args []string) []string {
	var keys []string
	if len(args) >= 2 {
		keys = append(keys, args[0]+" "+args[1])
	}
	if len(args) >= 1 {
		keys = append(keys, args[0])
	}
	return keys
}

var _ Runner = (*scriptRunner)(nil)

// findCall returns the first recorded call whose args contain all of want (in
// order as a contiguous check is not required — each want token must appear).
func findCall(calls [][]string, head ...string) []string {
	for _, c := range calls {
		joined := strings.Join(c, " ")
		ok := true
		for _, w := range head {
			if !strings.Contains(joined, w) {
				ok = false
				break
			}
		}
		if ok {
			return c
		}
	}
	return nil
}

func TestCreateGate(t *testing.T) {
	fr := &scriptRunner{responses: map[string]string{
		"gate create": `{"data":{"id":"gate-1"}}`,
	}}
	id, err := CreateGate(context.Background(), fr, "review-1", "pg-pr:active-pr", "o/r#7", "PR active")
	if err != nil {
		t.Fatalf("CreateGate: %v", err)
	}
	if id != "gate-1" {
		t.Errorf("gate id = %q, want gate-1", id)
	}
	call := findCall(fr.calls, "gate", "create", "--type=pg-pr:active-pr", "--blocks", "review-1", "--await-id", "o/r#7", "--json")
	if call == nil {
		t.Errorf("expected gate create with type/blocks/await-id/json flags; calls=%v", fr.calls)
	}
}

func TestListGates(t *testing.T) {
	fr := &scriptRunner{responses: map[string]string{
		"gate list": `{"data":[{"id":"g1","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#7"}]}`,
	}}
	gs, err := ListGates(context.Background(), fr)
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	if len(gs) != 1 {
		t.Fatalf("expected 1 gate, got %d", len(gs))
	}
	if gs[0].ID != "g1" || gs[0].AwaitType != "pg-pr:active-pr" || gs[0].AwaitID != "o/r#7" {
		t.Errorf("gate decoded wrong: %+v", gs[0])
	}
}

func TestResolveGate(t *testing.T) {
	fr := &scriptRunner{}
	if err := ResolveGate(context.Background(), fr, "g1", "PR active"); err != nil {
		t.Fatalf("ResolveGate: %v", err)
	}
	if findCall(fr.calls, "gate", "resolve", "g1") == nil {
		t.Errorf("expected gate resolve g1; calls=%v", fr.calls)
	}
}

// TestListGates_EmptyTolerated: an empty or null payload yields no gates and no
// error (no open gates), matching the store-decode tolerance elsewhere.
func TestListGates_EmptyTolerated(t *testing.T) {
	for _, out := range []string{"", "null", `{"data":[]}`, `{"data":null}`} {
		fr := &scriptRunner{responses: map[string]string{"gate list": out}}
		gs, err := ListGates(context.Background(), fr)
		if err != nil {
			t.Errorf("ListGates(%q) errored: %v", out, err)
		}
		if len(gs) != 0 {
			t.Errorf("ListGates(%q) = %d gates, want 0", out, len(gs))
		}
	}
}

// OpenActiveGate finds an open pg-pr:active-pr gate for a given await id.
func TestFindOpenGate(t *testing.T) {
	gs := []Gate{
		{ID: "g1", AwaitType: "pg-pr:merge", AwaitID: "o/r#7"},
		{ID: "g2", AwaitType: "pg-pr:active-pr", AwaitID: "o/r#7"},
		{ID: "g3", AwaitType: "pg-pr:active-pr", AwaitID: "o/r#9"},
	}
	g := FindOpenGate(gs, "pg-pr:active-pr", "o/r#7")
	if g == nil || g.ID != "g2" {
		t.Errorf("FindOpenGate should return g2, got %+v", g)
	}
	if FindOpenGate(gs, "pg-pr:active-pr", "o/r#404") != nil {
		t.Errorf("FindOpenGate should return nil for a missing await id")
	}
}
