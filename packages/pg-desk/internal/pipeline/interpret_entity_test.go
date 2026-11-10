package pipeline

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/corpus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// An entity type with no registered interpreter is an error and builds no row.
func TestInterpretEntity_UnknownTypeErrors(t *testing.T) {
	row, _, err := interpretEntity(corpus.Repo, corpus.Config(), corpus.Clock(), "bogus", "1", gather.GatherResult{})
	if err == nil || !strings.Contains(err.Error(), `no interpret adapter registered for entity type "bogus"`) {
		t.Fatalf("err = %v, want a no-adapter error", err)
	}
	if row != (store.Interpretation{}) {
		t.Fatalf("row = %+v, want zero", row)
	}
}

// An interpreter failure (an undecodable pr payload) is returned unwrapped and
// builds no row.
func TestInterpretEntity_InterpreterErrorBuildsNoRow(t *testing.T) {
	row, _, err := interpretEntity(corpus.Repo, corpus.Config(), corpus.Clock(), "pr", "1", gather.GatherResult{Payload: []byte("not json")})
	if err == nil || !strings.Contains(err.Error(), "decode pr facts") {
		t.Fatalf("err = %v, want a decode error", err)
	}
	if row != (store.Interpretation{}) {
		t.Fatalf("row = %+v, want zero", row)
	}
}

// The row carries the caller's repo, type and id, and the interpreter result is
// returned alongside it.
func TestInterpretEntity_RowIdentityAndInterpretation(t *testing.T) {
	row, interp, err := interpretEntity("acme/widgets", corpus.Config(), corpus.Clock(), "thread", "t-1", gather.GatherResult{Degraded: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Repo != "acme/widgets" || row.EntityType != "thread" || row.EntityID != "t-1" {
		t.Fatalf("row identity = %q %q %q", row.Repo, row.EntityType, row.EntityID)
	}
	if !row.Degraded || interp.Degraded != "stale" || row.AsOf != interp.AsOf {
		t.Fatalf("row = %+v interp = %+v", row, interp)
	}
}
