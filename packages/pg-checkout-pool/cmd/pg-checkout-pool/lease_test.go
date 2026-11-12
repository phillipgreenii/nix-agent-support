package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
	"github.com/phillipgreenii/pg-checkout-pool/internal/provision"
)

var hermeticEnv = []string{
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=Pool Test", "GIT_AUTHOR_EMAIL=pool-test@example.invalid",
	"GIT_COMMITTER_NAME=Pool Test", "GIT_COMMITTER_EMAIL=pool-test@example.invalid",
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), hermeticEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type mutClock struct{ t time.Time }

func (c *mutClock) Now() time.Time          { return c.t }
func (c *mutClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

type env struct {
	t      *testing.T
	root   string
	repo   string
	c1, c2 string
	clk    *mutClock
	wrap   func(provision.Provisioner) provision.Provisioner
	free   uint64
	sleeps []time.Duration
	onSlp  func()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	git(t, base, "init", "-q", "-b", "main", repo)
	writeFile(t, filepath.Join(repo, ".gitignore"), "*.o\n.flox/\n.envrc.local\n.claude/settings.local.json\nCLAUDE.local.md\n.vscode/settings.json\n")
	writeFile(t, filepath.Join(repo, "a.txt"), "one\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "c1")
	c1 := git(t, repo, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(repo, "a.txt"), "two\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "c2")
	c2 := git(t, repo, "rev-parse", "HEAD")
	return &env{
		t: t, root: filepath.Join(base, "pool"), repo: repo, c1: c1, c2: c2,
		clk: &mutClock{time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}, free: 1 << 40,
	}
}

// do runs one CLI invocation against the environment's pool root.
func (e *env) do(args ...string) (int, string, string) {
	e.t.Helper()
	var out, errb bytes.Buffer
	a := newApp(&out, &errb, e.clk)
	a.newProv = func(repo string, keep []string) (provision.Provisioner, error) {
		p, err := provision.NewWorktree(provision.WorktreeConfig{Repo: repo, Keep: keep, Env: hermeticEnv, Timeout: 30 * time.Second})
		if err != nil {
			return nil, err
		}
		if e.wrap != nil {
			return e.wrap(p), nil
		}
		return p, nil
	}
	a.freeBytes = func(string) (uint64, error) { return e.free, nil }
	a.sleep = func(d time.Duration) {
		e.sleeps = append(e.sleeps, d)
		e.clk.Advance(d)
		if e.onSlp != nil {
			e.onSlp()
		}
	}
	code := a.run(append([]string{args[0], "--root", e.root}, args[1:]...))
	return code, out.String(), errb.String()
}

func (e *env) lease(owner, bead string, extra ...string) (leaseOut, int, string) {
	e.t.Helper()
	args := []string{"lease", "--repo", e.repo, "--ref", "main", "--owner", owner, "--wait", "0"}
	if bead != "" {
		args = append(args, "--bead", bead)
	}
	code, out, errs := e.do(append(args, extra...)...)
	var l leaseOut
	if code == 0 {
		if err := json.Unmarshal([]byte(out), &l); err != nil {
			e.t.Fatalf("lease json: %v\n%s", err, out)
		}
	}
	return l, code, errs
}

func (e *env) mustLease(owner, bead string, extra ...string) leaseOut {
	e.t.Helper()
	l, code, errs := e.lease(owner, bead, extra...)
	if code != 0 {
		e.t.Fatalf("lease %s: exit %d: %s", owner, code, errs)
	}
	return l
}

func (e *env) release(id string, extra ...string) (int, map[string]any) {
	e.t.Helper()
	code, out, errs := e.do(append([]string{"release", "--lease", id}, extra...)...)
	m := map[string]any{}
	if code == 0 {
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			e.t.Fatalf("release json: %v\n%s", err, out)
		}
	} else {
		e.t.Logf("release stderr: %s", errs)
	}
	return code, m
}

func (e *env) record(slot string) pool.Record {
	e.t.Helper()
	r, err := pool.NewStore(e.root, e.clk).ReadRecord(slot)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func TestLeaseRenewReleaseCycle(t *testing.T) {
	e := newEnv(t)
	l := e.mustLease("sess-1", "bead-a", "--ttl", "10m")
	if l.Head != e.c2 || l.Owner != "sess-1" || l.Branch != "pg-checkout-pool/sess-1" || l.Daemon != "cold" || l.LeaseID == "" {
		t.Fatalf("unexpected lease: %+v", l)
	}
	if got := git(t, l.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != l.Branch {
		t.Errorf("slot branch %q, want %q", got, l.Branch)
	}
	if want := e.clk.Now().Add(10 * time.Minute); !l.ExpiresAt.Equal(want) {
		t.Errorf("expiry %v, want %v", l.ExpiresAt, want)
	}

	e.clk.Advance(5 * time.Minute)
	code, out, errs := e.do("renew", "--lease", l.LeaseID, "--ttl", "20m")
	if code != 0 {
		t.Fatalf("renew exit %d: %s", code, errs)
	}
	var rn struct{ ExpiresAt time.Time }
	if err := json.Unmarshal([]byte(out), &rn); err != nil || !rn.ExpiresAt.Equal(e.clk.Now().Add(20*time.Minute)) {
		t.Fatalf("renew out %s (%v)", out, err)
	}

	// A commit made in the lease must survive the release.
	writeFile(t, filepath.Join(l.Path, "work.txt"), "w\n")
	git(t, l.Path, "add", "work.txt")
	git(t, l.Path, "commit", "-q", "-m", "work")
	work := git(t, l.Path, "rev-parse", "HEAD")

	rcode, rout := e.release(l.LeaseID)
	if rcode != 0 || rout["disposition"] != "reset" || rout["rescueRef"] != nil {
		t.Fatalf("release: %d %v", rcode, rout)
	}
	rec := e.record(l.Slot)
	if rec.State != pool.WarmFree || rec.OwnerSessionID != "" {
		t.Errorf("after release: %+v", rec)
	}
	if got := git(t, l.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Errorf("slot not detached: %q", got)
	}
	if got := git(t, l.Path, "rev-parse", "HEAD"); got != e.c2 {
		t.Errorf("slot HEAD %s, want %s", got, e.c2)
	}
	if got := git(t, e.repo, "rev-parse", l.Branch); got != work {
		t.Errorf("lease branch lost its commit: %s vs %s", got, work)
	}
	if _, err := os.Stat(filepath.Join(l.Path, "work.txt")); err == nil {
		// work.txt is tracked only on the lease branch; detached at c2 it is gone.
		t.Error("work.txt still present after reset to the lease commit")
	}

	// The lease id is dead now, for renew and release alike.
	if code, _, _ := e.do("renew", "--lease", l.LeaseID); code != exitNoLease {
		t.Errorf("renew after release exit %d", code)
	}
	if code, _ := e.release(l.LeaseID); code != exitNoLease {
		t.Errorf("second release exit %d", code)
	}
	// And the slot is leasable again, warm this time.
	if l2 := e.mustLease("sess-2", ""); l2.Slot != l.Slot || l2.Daemon != "warm" {
		t.Errorf("second lease %+v", l2)
	}
}

func TestLeaseKeyIsSessionNotBead(t *testing.T) {
	e := newEnv(t)
	a := e.mustLease("sess-a", "same-bead")
	b := e.mustLease("sess-b", "same-bead")
	if a.Slot == b.Slot {
		t.Fatalf("two sessions on one bead share slot %s", a.Slot)
	}
	if e.record(a.Slot).OwnerSessionID != "sess-a" || e.record(a.Slot).BeadHint != "same-bead" {
		t.Errorf("record: %+v", e.record(a.Slot))
	}
	// One lease per session id, whatever the bead.
	if _, code, _ := e.lease("sess-a", "other-bead"); code != exitOwnerHasLease {
		t.Errorf("second lease for one session: exit %d", code)
	}
	// A lease id found by a bead id must not resolve.
	if code, _, _ := e.do("renew", "--lease", "same-bead"); code != exitNoLease {
		t.Errorf("renew by bead id exit %d", code)
	}
}

func TestAffinityPrefersSlotLastUsedForBead(t *testing.T) {
	e := newEnv(t)
	a := e.mustLease("s1", "bead-a")
	b := e.mustLease("s2", "bead-b")
	if a.Slot == b.Slot {
		t.Fatal("expected two slots")
	}
	for _, id := range []string{a.LeaseID, b.LeaseID} {
		if code, _ := e.release(id); code != 0 {
			t.Fatalf("release %s: %d", id, code)
		}
	}
	// Without affinity the lowest slot id wins; bead-b last used the second.
	if got := e.mustLease("s3", "bead-b"); got.Slot != b.Slot {
		t.Errorf("bead-b redispatch got %s, want %s", got.Slot, b.Slot)
	}
	// The hint is only a preference: a different bead takes whatever is free.
	if got := e.mustLease("s4", "bead-b"); got.Slot != a.Slot {
		t.Errorf("preferred slot busy, got %s, want fallback %s", got.Slot, a.Slot)
	}
}

func TestReleaseDirtyCapturesRescueRef(t *testing.T) {
	e := newEnv(t)
	l := e.mustLease("sess-1", "")
	writeFile(t, filepath.Join(l.Path, "a.txt"), "edited by owner\n")
	writeFile(t, filepath.Join(l.Path, "junk.o"), "ignored\n")
	code, out := e.release(l.LeaseID)
	if code != 0 {
		t.Fatalf("release exit %d", code)
	}
	ref, _ := out["rescueRef"].(string)
	if !strings.HasPrefix(ref, rescuePrefix) {
		t.Fatalf("rescueRef %q", ref)
	}
	if got := git(t, e.repo, "show", ref+":a.txt"); got != "edited by owner" {
		t.Errorf("rescue ref content %q", got)
	}
	if got := git(t, l.Path, "status", "--porcelain", "--ignored"); got != "" {
		t.Errorf("slot not clean after release: %q", got)
	}
	if got := git(t, l.Path, "show", "HEAD:a.txt"); got != "two" {
		t.Errorf("a.txt not reset: %q", got)
	}
}

type wrapped struct {
	provision.Provisioner
	rescue func() (string, error)
	reset  func() (string, error)
	// resetHook runs before every real reset (a test advances its clock in it).
	resetHook func()
}

func (w wrapped) Rescue(ctx context.Context, dir, ref string) (string, error) {
	if w.rescue != nil {
		return w.rescue()
	}
	return w.Provisioner.Rescue(ctx, dir, ref)
}

func (w wrapped) Reset(ctx context.Context, dir, commit, branch string) (string, error) {
	if w.reset != nil {
		return w.reset()
	}
	if w.resetHook != nil {
		w.resetHook()
	}
	return w.Provisioner.Reset(ctx, dir, commit, branch)
}

func TestFailedRescueQuarantinesAndNeverResets(t *testing.T) {
	e := newEnv(t)
	l := e.mustLease("sess-1", "")
	writeFile(t, filepath.Join(l.Path, "a.txt"), "precious\n")
	e.wrap = func(p provision.Provisioner) provision.Provisioner {
		return wrapped{Provisioner: p, rescue: func() (string, error) { return "", errors.New("disk full") }}
	}
	if code, _ := e.release(l.LeaseID); code != exitInternal {
		t.Fatalf("exit %d", code)
	}
	rec := e.record(l.Slot)
	if rec.State != pool.Quarantined || !strings.Contains(rec.Reason, "rescue capture") {
		t.Errorf("record: %+v", rec)
	}
	b, _ := os.ReadFile(filepath.Join(l.Path, "a.txt"))
	if string(b) != "precious\n" {
		t.Errorf("edits destroyed: %q", b)
	}
}

func TestReleaseKeepAndQuarantineDispositions(t *testing.T) {
	e := newEnv(t)
	k := e.mustLease("s-keep", "")
	writeFile(t, filepath.Join(k.Path, "a.txt"), "keep me\n")
	if code, _ := e.release(k.LeaseID, "--disposition", "keep"); code != 0 {
		t.Fatalf("keep exit %d", code)
	}
	if r := e.record(k.Slot); r.State != pool.Quarantined || !strings.Contains(r.Reason, "operator") {
		t.Errorf("keep record: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(k.Path, "a.txt")); string(b) != "keep me\n" {
		t.Errorf("keep touched the tree: %q", b)
	}
	q := e.mustLease("s-q", "")
	if q.Slot == k.Slot {
		t.Fatal("quarantined slot was leased")
	}
	if code, _ := e.release(q.LeaseID, "--disposition", "quarantine"); code != 0 {
		t.Fatalf("quarantine exit %d", code)
	}
	if e.record(q.Slot).State != pool.Quarantined {
		t.Errorf("record: %+v", e.record(q.Slot))
	}
}

func TestReap(t *testing.T) {
	e := newEnv(t)
	dead := e.mustLease("dead-owner", "", "--ttl", "1m")
	live := e.mustLease("live-owner", "", "--ttl", "1h")
	writeFile(t, filepath.Join(dead.Path, "a.txt"), "dead owner edit\n")

	// Nothing is expired yet.
	if code, out, _ := e.do("reap"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("early reap: %d %s", code, out)
	}
	e.clk.Advance(2 * time.Minute)

	code, out, _ := e.do("reap", "--dry-run")
	var es []reapEntry
	if err := json.Unmarshal([]byte(out), &es); err != nil || code != 0 || len(es) != 1 || es[0].Action != "would-reap" || es[0].Slot != dead.Slot {
		t.Fatalf("dry-run: %d %s", code, out)
	}
	if e.record(dead.Slot).State != pool.Leased {
		t.Fatal("dry-run changed state")
	}
	// A grace window keeps it.
	if _, out, _ := e.do("reap", "--grace", "10m"); strings.TrimSpace(out) != "[]" {
		t.Errorf("grace ignored: %s", out)
	}

	code, out, _ = e.do("reap")
	es = nil
	if err := json.Unmarshal([]byte(out), &es); err != nil || code != 0 || len(es) != 1 || es[0].Action != "reaped" || es[0].Owner != "dead-owner" {
		t.Fatalf("reap: %d %s", code, out)
	}
	if got := git(t, e.repo, "show", es[0].RescueRef+":a.txt"); got != "dead owner edit" {
		t.Errorf("rescue content %q", got)
	}
	if r := e.record(dead.Slot); r.State != pool.WarmFree || r.OwnerSessionID != "" {
		t.Errorf("reaped record: %+v", r)
	}
	if e.record(live.Slot).State != pool.Leased {
		t.Error("live lease was reaped")
	}
}

func TestReapQuarantinesWhenRescueFails(t *testing.T) {
	e := newEnv(t)
	l := e.mustLease("dead", "", "--ttl", "1m")
	e.clk.Advance(time.Hour)
	e.wrap = func(p provision.Provisioner) provision.Provisioner {
		return wrapped{Provisioner: p, rescue: func() (string, error) { return "", errors.New("boom") }}
	}
	code, out, _ := e.do("reap")
	if code != exitInternal || !strings.Contains(out, `"quarantined"`) {
		t.Fatalf("reap: %d %s", code, out)
	}
	if e.record(l.Slot).State != pool.Quarantined {
		t.Errorf("record: %+v", e.record(l.Slot))
	}
}

func TestReapSkipsHeldSlot(t *testing.T) {
	e := newEnv(t)
	l := e.mustLease("owner", "", "--ttl", "1m")
	e.clk.Advance(time.Hour)
	h, err := pool.NewStore(e.root, e.clk).Lock(l.Slot)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Unlock()
	if _, out, _ := e.do("reap"); strings.TrimSpace(out) != "[]" {
		t.Errorf("reaped a held slot: %s", out)
	}
}

func TestExitCodes(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"no owner", []string{"lease", "--repo", e.repo, "--ref", "main"}, exitUsage},
		{"relative repo", []string{"lease", "--repo", "repo", "--ref", "main", "--owner", "s"}, exitUsage},
		{"no ref", []string{"lease", "--repo", e.repo, "--owner", "s"}, exitUsage},
		{"bad branch", []string{"lease", "--repo", e.repo, "--ref", "main", "--owner", "s", "--branch", "a..b"}, exitUsage},
		{"bad ref", []string{"lease", "--repo", e.repo, "--ref", "no-such-ref", "--owner", "s"}, exitRefUnresolved},
		{"renew no id", []string{"renew"}, exitUsage},
		{"renew unknown", []string{"renew", "--lease", "nope"}, exitNoLease},
		{"release unknown", []string{"release", "--lease", "nope"}, exitNoLease},
		{"release bad disposition", []string{"release", "--lease", "x", "--disposition", "bin"}, exitUsage},
		{"unknown flag", []string{"reap", "--bogus"}, exitUsage},
	} {
		if code, _, _ := e.do(tc.args...); code != tc.want || code < 2 {
			t.Errorf("%s: exit %d, want %d", tc.name, code, tc.want)
		}
	}
}

func TestPoolExhaustedAndWait(t *testing.T) {
	e := newEnv(t)
	first := e.mustLease("s1", "", "--max-slots", "1")
	if _, code, _ := e.lease("s2", "", "--max-slots", "1"); code != exitExhausted {
		t.Fatalf("exit %d, want %d", code, exitExhausted)
	}
	// A bounded wait that is rewarded: the slot frees while the requester sleeps.
	e.onSlp = func() {
		if code, _ := e.release(first.LeaseID); code != 0 {
			t.Errorf("release in wait: %d", code)
		}
		e.onSlp = nil
	}
	l, code, errs := e.lease("s2", "", "--max-slots", "1", "--wait", "30s")
	if code != 0 || l.Slot != first.Slot {
		t.Fatalf("waited lease: %d %s %+v", code, errs, l)
	}
	if len(e.sleeps) != 1 || e.sleeps[0] != time.Second {
		t.Errorf("sleeps %v", e.sleeps)
	}
	// A wait that is not rewarded still ends, with the same code.
	e.sleeps = nil
	if _, code, _ := e.lease("s3", "", "--max-slots", "1", "--wait", "3s"); code != exitExhausted || len(e.sleeps) != 3 {
		t.Errorf("unrewarded wait: exit %d sleeps %v", code, e.sleeps)
	}
}

func TestLowDiskRefusesToGrow(t *testing.T) {
	e := newEnv(t)
	e.free = 1 << 20
	if _, code, _ := e.lease("s1", ""); code != exitLowDisk {
		t.Fatalf("exit %d, want %d", code, exitLowDisk)
	}
	if ids, _ := pool.NewStore(e.root, e.clk).Slots(); len(ids) != 0 {
		t.Errorf("slots created despite low disk: %v", ids)
	}
	// Reusing an existing slot needs no free space.
	e.free = 1 << 40
	l := e.mustLease("s1", "")
	e.release(l.LeaseID)
	e.free = 0
	e.mustLease("s2", "")
}

func TestQuarantineOnly(t *testing.T) {
	e := newEnv(t)
	l := e.mustLease("s1", "", "--max-slots", "1")
	if code, _ := e.release(l.LeaseID, "--disposition", "quarantine"); code != 0 {
		t.Fatal("release")
	}
	if _, code, _ := e.lease("s2", "", "--max-slots", "1"); code != exitQuarantineOnly {
		t.Errorf("exit %d, want %d", code, exitQuarantineOnly)
	}
}

func TestWedgedResetQuarantinesAndExits7(t *testing.T) {
	e := newEnv(t)
	e.wrap = func(p provision.Provisioner) provision.Provisioner {
		return wrapped{Provisioner: p, reset: func() (string, error) { return "", provision.ErrWedged }}
	}
	// The first lease creates a slot (create is real), then reset wedges.
	if _, code, _ := e.lease("s1", ""); code != exitWedged {
		t.Fatalf("exit %d, want %d", code, exitWedged)
	}
	if r := e.record("slot-01"); r.State != pool.Quarantined {
		t.Errorf("record: %+v", r)
	}
}

func TestResetFailureRetriesOnceThenMovesToAnotherSlot(t *testing.T) {
	e := newEnv(t)
	first := e.mustLease("s1", "")
	e.release(first.LeaseID)
	calls := 0
	e.wrap = func(p provision.Provisioner) provision.Provisioner {
		return wrapped{Provisioner: p, reset: func() (string, error) {
			calls++
			if calls <= 2 {
				return "", errors.New("transient")
			}
			return p.Reset(context.Background(), treePath(e.root, "slot-02"), e.c2, "pg-checkout-pool/s2")
		}}
	}
	l, code, errs := e.lease("s2", "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if e.record("slot-01").State != pool.Quarantined {
		t.Errorf("slot-01 should be quarantined after two failed resets: %+v", e.record("slot-01"))
	}
	if l.Slot != "slot-02" {
		t.Errorf("slot %s", l.Slot)
	}
}

func TestHelpListsLeaseCommands(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--help"}, &out, &errb, pool.SystemClock{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, w := range []string{"lease", "renew", "release", "reap", "exit codes"} {
		if !strings.Contains(errb.String(), w) {
			t.Errorf("help missing %q", w)
		}
	}
}

func TestLeaseReuseReturnsOwnersLiveLease(t *testing.T) {
	e := newEnv(t)
	a := e.mustLease("sess-a", "bead-1")
	other := e.mustLease("sess-b", "bead-2")
	e.clk.Advance(10 * time.Minute)

	// Without the flag a second lease keeps failing with exit 11.
	if _, code, _ := e.lease("sess-a", "bead-1"); code != exitOwnerHasLease {
		t.Fatalf("second lease without --reuse: exit %d", code)
	}
	again := e.mustLease("sess-a", "bead-1", "--reuse")
	if again.LeaseID != a.LeaseID || again.Slot != a.Slot || again.Path != a.Path || again.Branch != a.Branch {
		t.Fatalf("reuse = %+v, want the original %+v", again, a)
	}
	if !again.ExpiresAt.After(a.ExpiresAt) {
		t.Errorf("reuse did not extend the lease: %v vs %v", again.ExpiresAt, a.ExpiresAt)
	}
	if rec := e.record(a.Slot); rec.State != pool.Leased || rec.LeaseID != a.LeaseID {
		t.Errorf("record after reuse: %+v", rec)
	}
	// A different owner is unaffected: --reuse with no lease of its own leases normally.
	c := e.mustLease("sess-c", "bead-3", "--reuse")
	if c.LeaseID == a.LeaseID || c.LeaseID == other.LeaseID || c.Slot == a.Slot || c.Slot == other.Slot {
		t.Errorf("new owner with --reuse got %+v", c)
	}
	// After release the owner leases afresh, with a new lease id.
	if code, _ := e.release(a.LeaseID); code != 0 {
		t.Fatalf("release exit %d", code)
	}
	fresh := e.mustLease("sess-a", "bead-1", "--reuse")
	if fresh.LeaseID == a.LeaseID {
		t.Errorf("lease id reused after release: %s", fresh.LeaseID)
	}
}
