package settle

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// An append-mode file has no snapshots, and with its original gone no
// tombstone is needed to make it idle: its last copy commit (files.last_copy_ns)
// is the only signal of recent activity. An injected clock and settle.after
// decide idle; the file copied a moment ago is not idle, the one untouched for
// settle.after is.
func TestAppendFileLastCopyDecidesIdleWithoutSnapshotsOrTombstones(t *testing.T) {
	e := newCSEnv(t)
	after := time.Hour
	now := time.Unix(0, 10_000_000_000_000)
	mk := func(copyAt time.Time) store.File {
		t.Helper()
		id := e.st.NewFileID()
		e.n++
		if err := e.st.Commit(
			&store.Rec{T: store.TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: uint64(e.n), Birth: 3},
			&store.Rec{T: store.TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
			&store.Rec{T: store.TCommit, At: copyAt.UnixNano(), File: id, Gen: 1, From: 0, To: 5, Size: 5, Tail: 1, TailLen: 1},
		); err != nil {
			t.Fatal(err)
		}
		rows, err := e.st.AllFiles()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == id {
				return r
			}
		}
		t.Fatal("file not found")
		return store.File{}
	}
	recent, stale := mk(now.Add(-time.Minute)), mk(now.Add(-2*after))
	for _, cached := range []bool{false, true} {
		e.s.seen = nil
		if cached {
			groups := map[string]*conv{
				"recent": {session: "recent", files: []store.File{recent}},
				"stale":  {session: "stale", files: []store.File{stale}},
			}
			e.s.prefetchLastSeen(groups, map[string]bool{}, map[string]bool{})
		}
		if idle := now.Sub(e.s.lastSeen(recent)) >= after; idle {
			t.Errorf("cached=%v: recently copied file is idle", cached)
		}
		if idle := now.Sub(e.s.lastSeen(stale)) >= after; !idle {
			t.Errorf("cached=%v: file untouched for settle.after is not idle", cached)
		}
	}
}
