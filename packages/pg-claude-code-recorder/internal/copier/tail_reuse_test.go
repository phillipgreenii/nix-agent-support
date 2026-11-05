package copier

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cespare/xxhash/v2"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
)

// readCounter wraps a file system and counts read-only opens by path.
type readCounter struct {
	fsx.FS
	mu    sync.Mutex
	reads map[string]int
}

func (r *readCounter) OpenFile(name string, flag int, perm os.FileMode) (fsx.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		r.mu.Lock()
		if r.reads == nil {
			r.reads = map[string]int{}
		}
		r.reads[name]++
		r.mu.Unlock()
	}
	return r.FS.OpenFile(name, flag, perm)
}

func (r *readCounter) reset() {
	r.mu.Lock()
	r.reads = nil
	r.mu.Unlock()
}

func (r *readCounter) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads[name]
}

// A small append builds the new tail from the tail the check already read: the
// copy is not opened for reading again, and the stored tail still equals the
// copy's last bytes.
func TestSmallAppendDoesNotReReadTheCopyTail(t *testing.T) {
	rc := &readCounter{}
	e := newEnv(t, func(e *env) {
		e.cfgMod = func(c *Config) {
			rc.FS = e.rec
			c.FS = rc
		}
	})
	rel := "projects/example-project/s.jsonl"
	p := e.write(rel, strings.Repeat("a", 30)+"\n")
	id := e.ingest(p).File
	for i := 0; i < 3; i++ {
		e.appendTo(rel, strings.Repeat("b", 20+i)+"\n")
		rc.reset()
		e.ingest(p)
		f := e.file(id)
		cp := e.st.CopyPath(id, f.CurGen)
		if n := rc.count(cp); n != 0 {
			t.Fatalf("append %d opened the copy for reading %d times, want 0", i, n)
		}
		g := e.gen(id, f.CurGen)
		b, err := os.ReadFile(cp)
		if err != nil {
			t.Fatal(err)
		}
		want := b
		if len(want) > TailSize {
			want = want[len(want)-TailSize:]
		}
		if g.TailLen != len(want) || g.TailHash != xxhash.Sum64(want) {
			t.Fatalf("append %d: stored tail (%d bytes) differs from the copy's last bytes (%d)", i, g.TailLen, len(want))
		}
	}
}

// A copy longer than the tail window keeps the last TailSize bytes after a
// small append.
func TestSmallAppendKeepsTailWindow(t *testing.T) {
	e := newEnv(t)
	rel := "projects/example-project/s.jsonl"
	p := e.write(rel, strings.Repeat("a", 3*TailSize)+"\n")
	id := e.ingest(p).File
	e.appendTo(rel, "tail-bytes\n")
	e.ingest(p)
	f := e.file(id)
	b, _ := os.ReadFile(e.st.CopyPath(id, f.CurGen))
	want := b[len(b)-TailSize:]
	if g := e.gen(id, f.CurGen); g.TailLen != TailSize || g.TailHash != xxhash.Sum64(want) {
		t.Fatalf("tail %d bytes does not match the window", g.TailLen)
	}
}
