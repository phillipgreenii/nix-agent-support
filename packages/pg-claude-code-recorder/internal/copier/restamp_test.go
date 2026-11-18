package copier

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// movedIdentity rewrites the stored identity of file id the way a reboot or a
// remount would change it: the stored row no longer matches the live stat.
func (e *env) movedIdentity(f store.File, dev, ino uint64, birth int64) {
	e.t.Helper()
	if err := e.cp.commit(&store.Rec{T: store.TIdent, At: e.cp.now(), File: f.ID, Dev: dev, Ino: ino, Birth: birth}); err != nil {
		e.t.Fatal(err)
	}
}

func TestAppendDeviceChangeKeepsTheRowAndGeneration(t *testing.T) {
	e := newEnv(t)
	p := e.write(tx, "one\n")
	r1 := e.ingest(p)
	before := e.file(r1.File)
	e.movedIdentity(before, before.Dev+1, before.Ino, before.Birth)
	e.appendTo(tx, "two\n")

	r2 := e.ingest(p)
	if r2.File != r1.File {
		t.Fatalf("a device-only change made file %d, want the existing %d", r2.File, r1.File)
	}
	after := e.file(r1.File)
	if after.Dev != before.Dev || after.Ino != before.Ino || after.Birth != before.Birth {
		t.Fatalf("identity not re-stamped: before %+v after %+v", before, after)
	}
	if after.CurGen != before.CurGen {
		t.Fatalf("generation moved from %d to %d", before.CurGen, after.CurGen)
	}
	holders, err := e.st.FilesByCurrentPath(p)
	if err != nil || len(holders) != 1 {
		t.Fatalf("holders %v err %v, want exactly one row", holders, err)
	}
	if rows, _ := e.st.FilesByIdentity(before.Dev, before.Ino); len(rows) != 1 {
		t.Fatalf("%d rows for the live identity, want 1", len(rows))
	}
	e.mustEqual(r1.File, "one\ntwo\n")
}

func TestAppendReplacementStillGetsANewRowWhenInodeOrBirthDiffers(t *testing.T) {
	cases := []struct {
		name string
		mv   func(f store.File) (dev, ino uint64, birth int64)
	}{
		{"inode", func(f store.File) (uint64, uint64, int64) { return f.Dev + 1, f.Ino + 1, f.Birth }},
		{"birth", func(f store.File) (uint64, uint64, int64) { return f.Dev + 1, f.Ino, f.Birth + 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			p := e.write(tx, "one\n")
			r1 := e.ingest(p)
			dev, ino, birth := tc.mv(e.file(r1.File))
			e.movedIdentity(e.file(r1.File), dev, ino, birth)
			if r2 := e.ingest(p); r2.File == r1.File {
				t.Fatal("a path whose inode or birth time differs must be a new file")
			}
		})
	}
}
