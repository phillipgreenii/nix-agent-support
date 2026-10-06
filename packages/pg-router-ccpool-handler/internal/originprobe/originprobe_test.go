package originprobe

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// clock is a settable fake time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)} }

// fakeRepo makes a directory that passes probe step 1 (it holds a .git entry).
func fakeRepo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Mkdir(filepath.Join(d, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

type script struct {
	mu    sync.Mutex
	out   string
	err   error
	calls int
}

func (s *script) run(_ context.Context, _ string, _ ...string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.out, s.err
}

func (s *script) set(out string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out, s.err = out, err
}

func (s *script) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newProber(t *testing.T, origins ...config.WatchedOrigin) (*Prober, *script, *clock) {
	t.Helper()
	cfg := config.DefaultOriginProbe()
	cfg.Origins = origins
	cfg.StateDir = t.TempDir()
	p := New(cfg)
	s := &script{}
	c := newClock()
	p.Run = s.run
	p.Now = c.now
	return p, s, c
}

func TestCheck_classes(t *testing.T) {
	repo := fakeRepo(t)
	missing := filepath.Join(t.TempDir(), "gone")
	cases := []struct {
		name string
		root string
		out  string
		err  error
		want Class
	}{
		{"ok", repo, "abc\tHEAD", nil, OK},
		{"mount-missing: repo root absent", missing, "", nil, MountMissing},
		{"mount-missing: mkdir text", repo, "mkdir /Volumes/gitrepos: permission denied", errors.New("exit status 1"), MountMissing},
		{"auth: oauth command timed out", repo, "fatal: oauth command timed out", errors.New("exit status 128"), AuthUnavailable},
		{"auth: generic read failure", repo, "fatal: Could not read from remote repository.", errors.New("exit status 128"), AuthUnavailable},
		{"network", repo, "ssh: Could not resolve hostname git.example.test", errors.New("exit status 128"), Network},
		{"unknown", repo, "something unexpected happened", errors.New("exit status 1"), Unknown},
		{"timeout: deadline with no other signal", repo, "", context.DeadlineExceeded, Timeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := config.WatchedOrigin{Key: "git.example.test/o/r", RepoRoot: tc.root}
			p, s, _ := newProber(t, w)
			p.cfg.FailureThreshold = 1
			s.set(tc.out, tc.err)
			p.Run = func(ctx context.Context, dir string, a ...string) (string, error) {
				if errors.Is(tc.err, context.DeadlineExceeded) {
					<-ctx.Done()
				}
				return s.run(ctx, dir, a...)
			}
			// Only the timeout case needs a short deadline. Every other case
			// returns immediately, so a generous deadline keeps a loaded
			// machine from expiring pctx before probe() classifies the result
			// (which would turn "unknown" into "timeout"; pg2-ithab).
			p.cfg.Timeout = time.Minute
			if errors.Is(tc.err, context.DeadlineExceeded) {
				p.cfg.Timeout = 20 * time.Millisecond
			}
			d := p.Check(context.Background(), w)
			if d.State.Class != tc.want {
				t.Fatalf("class = %q, want %q (tail %q)", d.State.Class, tc.want, d.State.LastError)
			}
			if wantGated := tc.want != OK; d.Gated != wantGated {
				t.Fatalf("gated = %v, want %v at K=1", d.Gated, wantGated)
			}
		})
	}
}

func TestCheck_flapK2(t *testing.T) {
	w := config.WatchedOrigin{Key: "git.example.test/o/r", RepoRoot: fakeRepo(t)}
	p, s, c := newProber(t, w)
	s.set("fatal: Could not read from remote repository.", errors.New("exit status 128"))

	if d := p.Check(context.Background(), w); d.Gated {
		t.Fatal("one failure must not gate")
	}
	// Confirming probe is NOT delayed by the TTL.
	if d := p.Check(context.Background(), w); !d.Gated {
		t.Fatal("second consecutive failure must gate")
	}
	if s.count() != 2 {
		t.Fatalf("probes = %d, want 2", s.count())
	}
	// Gated, within TTL: cached, no new probe.
	if d := p.Check(context.Background(), w); !d.Gated || s.count() != 2 {
		t.Fatalf("within TTL must reuse cache: gated=%v probes=%d", d.Gated, s.count())
	}
	// Past TTL with the origin recovered: first ok clears.
	s.set("abc\tHEAD", nil)
	c.advance(p.cfg.TTL + time.Second)
	if d := p.Check(context.Background(), w); d.Gated || d.State.Class != OK || d.State.ConsecutiveFailures != 0 {
		t.Fatalf("one ok probe must clear: %+v", d)
	}
	if s.count() != 3 {
		t.Fatalf("probes = %d, want 3 (TTL re-probe)", s.count())
	}
}

func TestCheck_okResetsBetweenFailures(t *testing.T) {
	w := config.WatchedOrigin{Key: "git.example.test/o/r", RepoRoot: fakeRepo(t)}
	p, s, c := newProber(t, w)
	fail := func() { s.set("fatal: Could not read from remote repository.", errors.New("x")) }
	fail()
	p.Check(context.Background(), w) // 1 failure
	s.set("ok", nil)
	c.advance(time.Second)
	p.Check(context.Background(), w) // ok resets (failures>0 bypasses TTL)
	fail()
	c.advance(p.cfg.TTL + time.Second)
	if d := p.Check(context.Background(), w); d.Gated {
		t.Fatal("failures are consecutive: fail, ok, fail must not gate at K=2")
	}
}

func TestCheck_isolatedPerOriginAndUnresolved(t *testing.T) {
	a := config.WatchedOrigin{Key: "git.example.test/o/a", RepoRoot: fakeRepo(t)}
	b := config.WatchedOrigin{Key: "git.example.test/o/b", RepoRoot: fakeRepo(t)}
	p, s, _ := newProber(t, a, b)
	s.set("fatal: Could not read from remote repository.", errors.New("x"))
	p.Run = func(ctx context.Context, dir string, args ...string) (string, error) {
		if dir == a.RepoRoot {
			return s.run(ctx, dir, args...)
		}
		return "ok", nil
	}
	p.Check(context.Background(), a)
	if !p.Check(context.Background(), a).Gated {
		t.Fatal("origin A must be gated")
	}
	if p.Check(context.Background(), b).Gated {
		t.Fatal("gated origin A must not gate origin B")
	}
	// A dispatch with no resolvable origin is never gated.
	if _, ok := p.Resolve(""); ok {
		t.Fatal("empty repo root must not resolve")
	}
	if _, ok := p.Resolve(filepath.Join(a.RepoRoot, "..", "elsewhere")); ok {
		t.Fatal("unwatched repo root must not resolve")
	}
	if w, ok := p.Resolve(a.RepoRoot + "/"); !ok || w.Key != a.Key {
		t.Fatalf("cleaned repo root must resolve to A, got %+v ok=%v", w, ok)
	}
}

func TestCheck_killSwitch(t *testing.T) {
	w := config.WatchedOrigin{Key: "git.example.test/o/r", RepoRoot: fakeRepo(t)}
	p, s, _ := newProber(t, w)
	s.set("fatal: Could not read from remote repository.", errors.New("x"))
	p.Check(context.Background(), w)
	if !p.Check(context.Background(), w).Gated {
		t.Fatal("precondition: gated")
	}
	if err := p.Ignore(w.Key); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(p.Dir(), "git.example.test__o__r.disable")
	if p.DisablePath(w.Key) != want {
		t.Fatalf("disable path = %s, want %s", p.DisablePath(w.Key), want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("kill-switch file not written: %v", err)
	}
	before := s.count()
	if p.Check(context.Background(), w).Gated {
		t.Fatal("kill-switch must never gate")
	}
	if s.count() != before {
		t.Fatal("kill-switch must make the probe a no-op")
	}
	if err := p.Unignore(w.Key); err != nil {
		t.Fatal(err)
	}
	if !p.Check(context.Background(), w).Gated {
		t.Fatal("removing the kill-switch restores gating from the persisted state")
	}
	if err := p.Unignore(w.Key); err != nil {
		t.Fatalf("unignore of an absent file must not error: %v", err)
	}
}

func TestCheck_redactsUserinfoAndEmitsTransitions(t *testing.T) {
	w := config.WatchedOrigin{Key: "git.example.test/o/r", RepoRoot: fakeRepo(t)}
	p, s, c := newProber(t, w)
	var mu sync.Mutex
	var kinds []string
	var blob strings.Builder
	p.Emit = func(level, kind, msg string, f map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		kinds = append(kinds, kind)
		blob.WriteString(msg)
		for k, v := range f {
			blob.WriteString(k)
			blob.WriteString(toString(v))
		}
		return nil
	}
	secret := "s3cr3tpassw0rd"
	s.set("fatal: unable to access 'https://deploy:"+secret+"@git.example.test/o/r.git/': Could not resolve host: git.example.test", errors.New("exit status 128"))
	p.Check(context.Background(), w)
	d := p.Check(context.Background(), w)
	if !d.Gated || d.State.Class != Network {
		t.Fatalf("want gated network, got %+v", d)
	}
	s.set("ok", nil)
	c.advance(p.cfg.TTL + time.Second)
	p.Check(context.Background(), w)

	if strings.Join(kinds, ",") != "origin_gated,origin_cleared" {
		t.Fatalf("events = %v", kinds)
	}
	raw, err := os.ReadFile(p.statePath(w.Key))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"state file": string(raw), "events": blob.String(), "tail": d.State.LastError} {
		if strings.Contains(text, secret) {
			t.Errorf("%s leaks userinfo credential: %s", name, text)
		}
	}
	if !strings.Contains(d.State.LastError, "git.example.test") {
		t.Errorf("tail lost the non-secret context: %q", d.State.LastError)
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func TestWriteStatus_golden(t *testing.T) {
	a := config.WatchedOrigin{Key: "git.example.test/o/a", RepoRoot: fakeRepo(t)}
	b := config.WatchedOrigin{Key: "git.example.test/o/b", RepoRoot: fakeRepo(t)}
	c2 := config.WatchedOrigin{Key: "git.example.test/o/c", RepoRoot: fakeRepo(t)}
	p, s, _ := newProber(t, a, b, c2)
	s.set("fatal: Could not read from remote repository.", errors.New("exit status 128"))
	p.Run = func(ctx context.Context, dir string, args ...string) (string, error) {
		if dir == a.RepoRoot {
			return s.run(ctx, dir, args...)
		}
		return "ok", nil
	}
	p.Check(context.Background(), a)
	p.Check(context.Background(), a)
	p.Check(context.Background(), b)
	if err := p.Ignore(b.Key); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := p.WriteStatus(&buf); err != nil {
		t.Fatal(err)
	}
	want := "" +
		"KEY                   CLASS             GATED  IGNORED  SINCE                 FAILURES  LAST_ERROR\n" +
		"git.example.test/o/a  auth-unavailable  true   false    2026-09-30T12:00:00Z  2         fatal: Could not read from remote repository. exit status 128\n" +
		"git.example.test/o/b  ok                false  true     2026-09-30T12:00:00Z  0         -\n" +
		"git.example.test/o/c  unchecked         false  false    -                     0         -\n"
	if got := buf.String(); got != want {
		t.Fatalf("status mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/leaked/.git")
	t.Setenv("GIT_INDEX_FILE", "/leaked/index")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("GIT_SSH_COMMAND", "ssh -F /tmp/cfg")
	env := Env()
	has := func(kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_INDEX_FILE=") {
			t.Errorf("repository-naming variable leaked into probe env: %s", e)
		}
	}
	if !has("GIT_TERMINAL_PROMPT=0") {
		t.Error("probe env must force GIT_TERMINAL_PROMPT=0")
	}
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_TERMINAL_PROMPT=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("GIT_TERMINAL_PROMPT appears %d times, want 1", n)
	}
	if !has("SSH_AUTH_SOCK=/tmp/agent.sock") || !has("GIT_SSH_COMMAND=ssh -F /tmp/cfg") {
		t.Error("credential-delivery variables must reach the probe like they reach any git child")
	}
}

// TestGitRunner_realLocalRemote drives the production runner against local
// fixtures only (no network): a second local repo as the remote, and a missing path.
func TestGitRunner_realLocalRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmp, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// Both repositories come from x/gittest: hermetic by construction (temp
	// root, fixture HOME, allowlisted environment), so a leaked GIT_DIR from a
	// commit hook cannot redirect fixture setup.
	workFx := gittest.New(t, gitfixture.RepoOptions{Suite: "originprobe-work"})
	srcFx := gittest.New(t, gitfixture.RepoOptions{Suite: "originprobe-src"})
	if _, err := srcFx.Commit(context.Background(), "init", nil); err != nil {
		t.Fatal(err)
	}
	work, src := workFx.Dir, srcFx.Dir

	// The remote is a second local repository, so no network is involved.
	w := config.WatchedOrigin{Key: "git.example.test/o/r", RepoRoot: work, Remote: src}
	cfg := config.DefaultOriginProbe()
	cfg.Origins = []config.WatchedOrigin{w}
	cfg.StateDir = t.TempDir()
	cfg.FailureThreshold = 1
	p := New(cfg)

	if d := p.Check(context.Background(), w); d.State.Class != OK {
		t.Fatalf("reachable local remote: class = %q tail %q", d.State.Class, d.State.LastError)
	}

	bad := config.WatchedOrigin{Key: "git.example.test/o/bad", RepoRoot: work, Remote: filepath.Join(tmp, "does-not-exist.git")}
	p.cfg.Origins = append(p.cfg.Origins, bad)
	d := p.Check(context.Background(), bad)
	if d.State.Class == OK || !d.Gated {
		t.Fatalf("unreachable remote must gate at K=1: %+v", d)
	}
}
