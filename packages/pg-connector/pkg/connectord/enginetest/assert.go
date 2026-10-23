package enginetest

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The assertions below fail the test through t.Errorf (WantSeqs through
// t.Fatalf, because what follows indexes the rows) and report what was found.

// Seqs returns the sequence numbers of rows.
func Seqs(rows []store.ChangeRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Seq)
	}
	return out
}

// Refs returns the wire references (the row's ID) of rows.
func Refs(rows []store.ChangeRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// FieldNames returns the names of the fields of the row.
func FieldNames(t testing.TB, row store.ChangeRow) []string {
	t.Helper()
	fs, err := engine.DecodeFields(row.Fields)
	if err != nil {
		t.Fatalf("decode the fields of row %d: %v", row.Seq, err)
	}
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

// WantSeqs fails unless the rows carry exactly the sequence numbers want, in
// order.
func WantSeqs(t testing.TB, rows []store.ChangeRow, want ...int64) {
	t.Helper()
	if got := Seqs(rows); !slices.Equal(got, want) {
		t.Fatalf("seqs = %v, want %v", got, want)
	}
}

// WantKinds fails unless kinds is exactly want, in order.
func WantKinds(t testing.TB, kinds []string, want ...string) {
	t.Helper()
	if !slices.Equal(kinds, want) {
		t.Errorf("kinds = %v, want %v", kinds, want)
	}
}

// WantRowKinds fails unless the kind list of each row, in order, is the
// matching list of want.
func WantRowKinds(t testing.TB, rows []store.ChangeRow, want ...[]string) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("%d rows (%v), want %d", len(rows), Seqs(rows), len(want))
	}
	for i, row := range rows {
		if !slices.Equal(row.Kinds, want[i]) {
			t.Errorf("row %d (seq %d) kinds = %v, want %v", i, row.Seq, row.Kinds, want[i])
		}
	}
}

// WantFieldNames fails unless the row's fields are named exactly want, in order.
func WantFieldNames(t testing.TB, row store.ChangeRow, want ...string) {
	t.Helper()
	if got := FieldNames(t, row); !slices.Equal(got, want) {
		t.Errorf("fields of row %d = %v, want %v", row.Seq, got, want)
	}
}

// WantRefs fails unless the rows are for exactly the references want, in order.
func WantRefs(t testing.TB, rows []store.ChangeRow, want ...string) {
	t.Helper()
	if got := Refs(rows); !slices.Equal(got, want) {
		t.Errorf("refs = %v, want %v", got, want)
	}
}

// WantNoRows fails when rows is not empty.
func WantNoRows(t testing.TB, rows []store.ChangeRow) {
	t.Helper()
	if len(rows) != 0 {
		t.Errorf("%d rows (%v), want none", len(rows), Seqs(rows))
	}
}
