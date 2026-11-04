package parser_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func oracleKinds(t *testing.T, k interface {
	UpdateGauges()
}, gauge func() float64,
) int {
	t.Helper()
	k.UpdateGauges()
	return int(gauge())
}

func unknownEqual(t *testing.T, kit interface {
	UpdateGauges()
}, gauge func() float64, u *parser.Unknown, want int, label string,
) {
	t.Helper()
	if got := u.Count(); got != want {
		t.Fatalf("%s: maintained count %d, want %d", label, got, want)
	}
	if o := oracleKinds(t, kit, gauge); o != want {
		t.Fatalf("%s: oracle %d, want %d", label, o, want)
	}
}

func addUnknown(t *testing.T, st *store.Store, src string, at int64) int64 {
	t.Helper()
	id := st.NewFileID()
	if err := st.Commit(&store.Rec{T: store.TFile, At: at, File: id, Source: src, Mode: "append", Dev: 1, Ino: uint64(id)}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUnknownKindsMaintained(t *testing.T) {
	k := newLagKit(t)
	u, err := k.Parser.StartUnknown()
	if err != nil {
		t.Fatal(err)
	}
	gauge := func() float64 { v, _ := k.Obs.Reg.Gauge(parser.MUnknownKinds, nil); return v }
	unknownEqual(t, k.Parser, gauge, u, 0, "empty")

	k.Add("example-proj", "example-session-1.jsonl", "")
	unknownEqual(t, k.Parser, gauge, u, 0, "known source only")

	a := addUnknown(t, k.Store, "no-such-source", 10)
	unknownEqual(t, k.Parser, gauge, u, 1, "one unknown file")
	b := addUnknown(t, k.Store, "no-such-source", 11)
	c := addUnknown(t, k.Store, "other-unknown", 12)
	unknownEqual(t, k.Parser, gauge, u, 2, "distinct sources")

	// A duplicate file record is ignored by the store and changes nothing.
	if err := k.Store.Commit(&store.Rec{T: store.TFile, At: 13, File: a, Source: "no-such-source", Mode: "append", Dev: 1, Ino: uint64(a)}); err != nil {
		t.Fatal(err)
	}
	unknownEqual(t, k.Parser, gauge, u, 2, "duplicate record")

	// Deleting one of two files of a source keeps the source; deleting the
	// last one removes it.
	if err := k.Store.Commit(&store.Rec{T: store.TState, At: 14, File: a, State: "deleted"}); err != nil {
		t.Fatal(err)
	}
	unknownEqual(t, k.Parser, gauge, u, 2, "one of two deleted")
	if err := k.Store.Commit(&store.Rec{T: store.TState, At: 15, File: b, State: "deleted"}); err != nil {
		t.Fatal(err)
	}
	unknownEqual(t, k.Parser, gauge, u, 1, "source's files all deleted")
	// A tombstone deletes too, and a state change back revives.
	if err := k.Store.Commit(&store.Rec{T: store.TTomb, At: 16, File: c, Path: "/x"}); err != nil {
		t.Fatal(err)
	}
	unknownEqual(t, k.Parser, gauge, u, 0, "tombstoned")
	if err := k.Store.Commit(&store.Rec{T: store.TState, At: 17, File: c, State: "ok"}); err != nil {
		t.Fatal(err)
	}
	unknownEqual(t, k.Parser, gauge, u, 1, "revived")
}

// A restart: files of an unknown source that exist before the set is started
// are counted by the seed, before any new file is created.
func TestUnknownKindsSeededAtStart(t *testing.T) {
	k := newLagKit(t)
	addUnknown(t, k.Store, "no-such-source", 10)
	dead := addUnknown(t, k.Store, "gone-source", 11)
	if err := k.Store.Commit(&store.Rec{T: store.TState, At: 12, File: dead, State: "deleted"}); err != nil {
		t.Fatal(err)
	}
	u, err := k.Parser.StartUnknown()
	if err != nil {
		t.Fatal(err)
	}
	gauge := func() float64 { v, _ := k.Obs.Reg.Gauge(parser.MUnknownKinds, nil); return v }
	unknownEqual(t, k.Parser, gauge, u, 1, "after seed")
	addUnknown(t, k.Store, "third", 13)
	unknownEqual(t, k.Parser, gauge, u, 2, "after new file")
}
