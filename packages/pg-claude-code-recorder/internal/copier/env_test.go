package copier

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/invariants"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// fakeClone is the ClonePolicy seam: a probe result, an errno and a byte-copying
// clone, so the unit tier covers clone mode, byte mode and EXDEV on any volume.
type fakeClone struct {
	ok      bool
	reason  string
	err     error // returned by Clone when set
	private int64
	calls   int
}

func (f *fakeClone) Probe(string, string) (bool, string) { return f.ok, f.reason }
func (f *fakeClone) Clone(src, dst string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
func (f *fakeClone) PrivateSize(string) (int64, error) { return f.private, nil }

type env struct {
	t      *testing.T
	claude string
	ends   string
	state  string
	st     *store.Store
	cp     *Copier
	obs    *obs.Obs
	clk    *clock.Fake
	rec    *fsx.Recorder
	clone  *fakeClone
	before *invariants.DirSnapshot
	cfgMod func(*Config)
	log    *slog.Logger
}

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// newEnv builds a fixture Claude directory and a state directory in temporary
// directories (real files, real inodes, real SQLite) and runs the invariants
// helper when the test ends.
func newEnv(t *testing.T, mod ...func(*env)) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{
		t: t, claude: filepath.Join(root, "claude"), ends: filepath.Join(root, "control", "ends"),
		state: filepath.Join(root, "state"), clk: clock.NewFake(t0), clone: &fakeClone{reason: "test: byte mode"},
	}
	for _, d := range []string{e.claude, e.ends, filepath.Join(e.claude, "projects", "example-project"), filepath.Join(e.claude, "sessions")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range mod {
		m(e)
	}
	e.open()
	t.Cleanup(func() {
		invariants.Check(t, e.st, invariants.Options{ClaudeDir: e.claude, ClaudeBefore: e.before})
		_ = e.st.Close()
	})
	return e
}

func (e *env) open() {
	e.t.Helper()
	e.rec = fsx.NewRecorder(nil)
	e.obs = obs.New(e.log)
	st, err := store.Open(e.state, store.Options{FS: e.rec, Obs: e.obs})
	if err != nil {
		e.t.Fatal(err)
	}
	st.OnCommit(func() { e.rec.Mark("commit") })
	e.st = st
	cfg := Config{
		Store: st, Scope: scope.New(e.claude, e.ends), Clock: e.clk, Clone: e.clone, Obs: e.obs,
		JSONRetries: 3, JSONRetryDelay: time.Millisecond,
	}
	if e.cfgMod != nil {
		e.cfgMod(&cfg)
	}
	cp, err := New(cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.cp = cp
}

// crash drops the open store without a clean shutdown of the recorder state
// (nothing is flushed that was not already fsynced) and reopens on the same
// state and Claude directories.
func (e *env) reopen() {
	e.t.Helper()
	_ = e.st.Close()
	e.open()
}

func (e *env) abs(rel string) string { return filepath.Join(e.claude, rel) }

func (e *env) write(rel, content string) string {
	e.t.Helper()
	p := e.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) appendTo(rel, content string) {
	e.t.Helper()
	f, err := os.OpenFile(e.abs(rel), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) ingest(path string) Result {
	e.t.Helper()
	r, err := e.cp.Ingest(path)
	if err != nil {
		e.t.Fatalf("Ingest(%s): %v", path, err)
	}
	return r
}

func (e *env) file(id int64) store.File {
	e.t.Helper()
	f, err := e.st.File(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

func (e *env) gen(id int64, gen int) store.Generation {
	e.t.Helper()
	g, err := e.st.Generation(id, gen)
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}

// copyOf returns the bytes of the file's current generation.
func (e *env) copyOf(id int64) string {
	e.t.Helper()
	f := e.file(id)
	b, err := os.ReadFile(e.st.CopyPath(id, f.CurGen))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) mustEqual(id int64, want string) {
	e.t.Helper()
	if got := e.copyOf(id); got != want {
		e.t.Fatalf("copy of %d = %q, want %q", id, got, want)
	}
	f := e.file(id)
	if g := e.gen(id, f.CurGen); g.CopyOffset != int64(len(want)) {
		e.t.Fatalf("committed offset = %d, want %d", g.CopyOffset, len(want))
	}
}

func (e *env) eventAttr(name, attr string) []string {
	var out []string
	for _, ev := range e.obs.EventsNamed(name) {
		out = append(out, ev.Attrs[attr])
	}
	return out
}

var _ = syscall.ENOSPC

// hookClock runs onSleep whenever the copier sleeps (a writer finishing a
// rewrite while the copier waits to re-read).
type hookClock struct {
	*clock.Fake
	onSleep func()
}

func (h *hookClock) Sleep(d time.Duration) {
	h.onSleep()
	h.Fake.Sleep(d)
}
