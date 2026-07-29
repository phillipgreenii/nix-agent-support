package proto

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// TestDirectoryBlockedCountReachesTheWire completes the evidence chain for bead
// pg2-vsrxf's `info path:` fix: a directory whose sessions are ALL blocked must
// arrive at a served surface as blocked_n == 3, and the retired dormant_n (field
// 7, ADR 0024 R8) must arrive as 0 because nothing writes it. Those two facts
// together are why "%d working, %d idle, %d dormant" rendered "0, 0, 0" for a
// directory whose three sessions were blocked on a usage limit.
func TestDirectoryBlockedCountReachesTheWire(t *testing.T) {
	in := &aggregate.Tree{
		Dirs: []*aggregate.Directory{{Path: "/repo", BlockedN: 3}},
	}
	ds := FromTree(in)

	b, err := proto.Marshal(ds)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire DaemonState
	if err := proto.Unmarshal(b, &wire); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(wire.GetDirs()) != 1 {
		t.Fatalf("want 1 dir on the wire, got %d", len(wire.GetDirs()))
	}
	d := wire.GetDirs()[0]
	if d.GetBlockedN() != 3 {
		t.Errorf("wire blocked_n = %d, want 3 — blocked sessions must not vanish from the rollup", d.GetBlockedN())
	}
	if d.GetWorkingN() != 0 || d.GetIdleN() != 0 {
		t.Errorf("wire working_n/idle_n = %d/%d, want 0/0", d.GetWorkingN(), d.GetIdleN())
	}
	if d.GetDormantN() != 0 {
		t.Errorf("wire dormant_n = %d, want 0 — the field is retired and has no writer (ADR 0024 R8)", d.GetDormantN())
	}

	got := ToTree(&wire)
	if got.Dirs[0].BlockedN != 3 {
		t.Errorf("ToTree BlockedN = %d, want 3", got.Dirs[0].BlockedN)
	}
	if got.Dirs[0].IdleN != 0 {
		t.Errorf("ToTree IdleN = %d, want 0 (nothing to fold in)", got.Dirs[0].IdleN)
	}
}

// TestDirectoryLegacyDormantNFoldsIntoIdle covers the other half of the version
// skew: an OLDER daemon still sends dormant_n, and dirFromProto must ADD it to
// idle (ADR 0024 R8) rather than expose it as its own count.
func TestDirectoryLegacyDormantNFoldsIntoIdle(t *testing.T) {
	wire := &DaemonState{Dirs: []*Directory{{
		Path:     "/repo",
		WorkingN: 1,
		BlockedN: 2,
		IdleN:    3,
		DormantN: 4, // only an old daemon ever sets this
	}}}
	got := ToTree(wire)
	d := got.Dirs[0]
	if d.WorkingN != 1 || d.BlockedN != 2 || d.IdleN != 7 {
		t.Errorf("counts = (working %d, blocked %d, idle %d), want (1, 2, 7) — legacy dormant_n folds into idle",
			d.WorkingN, d.BlockedN, d.IdleN)
	}
}
