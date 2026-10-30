package settle

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/invariants"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

const sess = "0a1b2c3d-0000-4000-8000-000000000001"

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// fakeClone copies bytes (the ClonePolicy seam); ok selects clone mode.
type fakeClone struct {
	ok    bool
	calls int
}

func (f *fakeClone) Probe(string, string) (bool, string) { return f.ok, "test" }
func (f *fakeClone) Clone(src, dst string) error {
	f.calls++
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
func (f *fakeClone) PrivateSize(string) (int64, error) { return 0, nil }

type env struct {
	t       *testing.T
	claude  string
	state   string
	clone   *fakeClone
	clk     *clock.Fake
	rec     *fsx.Recorder
	obs     *obs.Obs
	st      *store.Store
	cp      *copier.Copier
	prs     *parser.Parser
	sett    *Settler
	live    map[string]bool
	free    int64
	hooks   Hooks
	before  *invariants.DirSnapshot
	skipInv bool
	policy  copier.ClonePolicy
}

func newEnv(t *testing.T, cloneMode bool) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{
		t: t, claude: filepath.Join(root, "claude"), state: filepath.Join(root, "state"), clone: &fakeClone{ok: cloneMode},
		clk: clock.NewFake(t0), live: map[string]bool{}, free: 1 << 40,
	}
	if err := os.MkdirAll(filepath.Join(e.claude, "projects", "example-project"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.open()
	t.Cleanup(func() {
		if !e.skipInv {
			invariants.Check(t, e.st, invariants.Options{ClaudeDir: e.claude, ClaudeBefore: e.before})
		}
		_ = e.st.Close()
	})
	return e
}

func (e *env) open() {
	e.t.Helper()
	e.rec = fsx.NewRecorder(nil)
	e.obs = obs.New(nil)
	st, err := store.Open(e.state, store.Options{FS: e.rec, Obs: e.obs})
	if err != nil {
		e.t.Fatal(err)
	}
	st.OnCommit(func() { e.rec.Mark("commit") })
	e.st = st
	var pol copier.ClonePolicy = e.clone
	if e.policy != nil {
		pol = e.policy
	}
	cp, err := copier.New(copier.Config{Store: st, Scope: scope.New(e.claude, ""), Clock: e.clk, Clone: pol, Obs: e.obs, JSONRetries: 1, JSONRetryDelay: time.Millisecond})
	if err != nil {
		e.t.Fatal(err)
	}
	e.cp = cp
	e.prs = parser.New(parser.Config{Store: st, Sources: sources.All(), ClaudeDir: e.claude, Obs: e.obs, Clock: e.clk})
	e.sett = New(Config{
		Store: st, Copier: cp, Parser: e.prs, Clock: e.clk, Obs: e.obs, ClaudeDir: e.claude,
		After: 24 * time.Hour, Recheck: time.Hour, MinFreeBytes: 1 << 30, MaxLineBytes: 1 << 20,
		Live:  func() (map[string]bool, error) { return e.live, nil },
		Free:  FreeFunc(func(string) (int64, error) { return e.free, nil }),
		Hooks: Hooks{BeforeClone: func(f int64) { e.hookClone(f) }, BeforeDelete: func(s string) { e.hookDelete(s) }},
	})
}

func (e *env) hookClone(f int64) {
	if e.hooks.BeforeClone != nil {
		e.hooks.BeforeClone(f)
	}
}

func (e *env) hookDelete(s string) {
	if e.hooks.BeforeDelete != nil {
		e.hooks.BeforeDelete(s)
	}
}

// crash drops the open store and reopens on the same directories, then runs the
// recovery a daemon start runs.
func (e *env) crash() {
	e.t.Helper()
	_ = e.st.Close()
	e.open()
	if err := e.cp.Recover(); err != nil {
		e.t.Fatal(err)
	}
	if err := e.sett.Recover(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) path(rel string) string { return filepath.Join(e.claude, rel) }

func line(i int) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u%d","sessionId":%q,"timestamp":"2026-10-29T20:00:%02dZ","cwd":"/example/work","message":{"role":"user","content":"example message %d"}}`+"\n", i, sess, i%60, i)
}

func (e *env) transcript() string { return "projects/example-project/" + sess + ".jsonl" }

// write creates or appends to a Claude file, sets its mtime and ingests it.
func (e *env) write(rel, content string, mtime time.Time, appendMode bool) {
	e.t.Helper()
	p := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(p, flag, 0o644)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = f.WriteString(content)
	_ = f.Close()
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) ingest(rel string) {
	e.t.Helper()
	res, err := e.cp.Ingest(e.path(rel))
	if err != nil {
		e.t.Fatal(err)
	}
	e.prs.Notify(res.File)
	if err := e.prs.Drain(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) parseAll() {
	e.t.Helper()
	if err := e.prs.ParseAll(); err != nil {
		e.t.Fatal(err)
	}
}

// seed writes n lines quiet since age before the clock and ingests them.
func (e *env) seed(n int, age time.Duration) string {
	e.t.Helper()
	var s string
	for i := 1; i <= n; i++ {
		s += line(i)
	}
	e.write(e.transcript(), s, e.clk.Now().Add(-age), false)
	e.ingest(e.transcript())
	e.parseAll()
	return s
}

func (e *env) tick() {
	e.t.Helper()
	if err := e.sett.Tick(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) stage() string {
	e.t.Helper()
	r, ok, err := e.st.Stage(sess)
	if err != nil {
		e.t.Fatal(err)
	}
	if !ok {
		return StageQuiet
	}
	return r.Stage
}

func (e *env) fileID() int64 {
	e.t.Helper()
	fl, err := e.st.FilesByCurrentPath(e.path(e.transcript()))
	if err != nil || len(fl) == 0 {
		e.t.Fatalf("no file: %v", err)
	}
	return fl[0].ID
}

// raw reads the first n bytes of the current generation through the pointer.
func (e *env) raw(file int64, n int) string {
	e.t.Helper()
	f, _ := e.st.File(file)
	b, err := e.st.ReadPointer(file, f.CurGen, 0, n)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) heldReason() string {
	e.t.Helper()
	r, ok, _ := e.st.Stage(sess)
	if !ok || r.Stage != StageHeld {
		e.t.Fatalf("stage = %q, want held", e.stage())
	}
	return r.Reason
}
