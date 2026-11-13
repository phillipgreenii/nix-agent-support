package locks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
)

// recorder wraps the real Runner and records the git subcommands run.
type recorder struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *recorder) run(ctx context.Context, c cmdrun.Call) cmdrun.Result {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), c.Args[len(gitexec.PinnedOptions):]...))
	r.mu.Unlock()
	return cmdrun.Exec(ctx, c)
}

// matching returns the calls whose first arg is sub (and second is arg2 when given).
func (r *recorder) matching(sub string, arg2 ...string) [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]string
	for _, c := range r.calls {
		if c[0] != sub {
			continue
		}
		if len(arg2) > 0 && (len(c) < 2 || c[1] != arg2[0]) {
			continue
		}
		out = append(out, c)
	}
	return out
}

type fx struct {
	t      *testing.T
	repo   *gitfixture.Repo
	clock  *seamstest.Clock
	rec    *recorder
	l      *Layer
	out    *bytes.Buffer
	sleeps []time.Duration
	jit    time.Duration
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	f := &fx{t: t, repo: repo, clock: seamstest.NewClock(time.Now()), rec: &recorder{}, out: &bytes.Buffer{}}
	f.l = f.layer()
	return f
}

// layer builds a Layer over the fixture (several may share one repository).
func (f *fx) layer() *Layer {
	root := f.repo.Root()
	env := append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"),
	)
	return &Layer{
		Git:    &gitexec.Git{Runner: f.rec.run, Env: seams.FilteredEnv(env)},
		Clock:  f.clock,
		Dir:    f.repo.Dir,
		Config: Config{LeaseTTL: 30 * time.Second, Wait: 60 * time.Second},
		Out:    f.out,
		Sleep: func(_ context.Context, d time.Duration) error {
			f.sleeps = append(f.sleeps, d)
			f.clock.Advance(d)
			return nil
		},
		Jitter: func(max time.Duration) time.Duration {
			if f.jit > 0 {
				return f.jit
			}
			return max / 2
		},
	}
}

func (f *fx) commonDir() string {
	f.t.Helper()
	d, err := f.l.common(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func branchRec(branch, target, op string, wts ...string) Record {
	r := Record{
		Schema: 1, Role: RoleBranch, State: StateRunning, OpID: op, Operation: "land", Branch: branch, Target: target,
		Owner:     Owner{Host: "laptop.local", PID: 41721, PIDStart: 1794132898, Token: "b1c4", ToolVersion: "26.11.07"},
		StartedAt: time.Now().UTC().Truncate(time.Second), Stage: "rebasing",
	}
	for _, w := range wts {
		r.Worktrees = append(r.Worktrees, WorktreeEntry{Admin: w, Path: "/wt/" + w, Roles: []string{"source"}, Start: Start{Branch: branch}})
	}
	return r
}

// plant writes rec to ref by hand (as another tool run would have).
func (f *fx) plant(ref string, rec Record) string {
	f.t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.plantRaw(ref, b)
}

func (f *fx) plantRaw(ref string, content []byte) string {
	f.t.Helper()
	r := f.l.Git.RunInput(f.t.Context(), f.repo.Dir, content, "hash-object", "-w", "--stdin")
	if !r.OK() {
		f.t.Fatalf("hash-object: %s", r.Stderr)
	}
	oid := strings.TrimSpace(string(r.Stdout))
	if u := f.l.Git.Run(f.t.Context(), f.repo.Dir, "update-ref", ref, oid); !u.OK() {
		f.t.Fatalf("update-ref: %s", u.Stderr)
	}
	return oid
}

func (f *fx) refOID(ref string) string {
	f.t.Helper()
	st, err := f.l.Read(f.t.Context(), ref)
	if err != nil {
		f.t.Fatal(err)
	}
	return st.OID
}

func (f *fx) lockFile(ref string) string {
	f.t.Helper()
	p := filepath.Join(f.commonDir(), filepath.FromSlash(ref)+".lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func TestRefsAndAdminName(t *testing.T) {
	if got := BranchRef("feature/x"); got != "refs/pg-git-lander/locks/heads/feature/x" {
		t.Error(got)
	}
	if got := WorktreeRef("feature-x"); got != "refs/pg-git-lander/locks/worktrees/feature-x" {
		t.Error(got)
	}
	if got := AdminRef(AdminEvents); got != "refs/pg-git-lander/locks/admin/events" {
		t.Error(got)
	}
	for in, want := range map[string]string{
		"/r/.git":                      "_main",
		"/r/.git/worktrees/feature-x":  "feature-x",
		"/r/.git/worktrees/feature-x/": "feature-x",
	} {
		if got := AdminName(in); got != want {
			t.Errorf("AdminName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdminNameFromRealGit(t *testing.T) {
	f := newFx(t)
	wt := filepath.Join(f.repo.Root(), "wt1")
	f.mustGit(t.Context(), f.repo.Dir, "worktree", "add", "-b", "b1", wt)
	for dir, want := range map[string]string{f.repo.Dir: "_main", wt: "wt1"} {
		r := f.l.Git.Run(t.Context(), dir, "rev-parse", "--path-format=absolute", "--git-dir")
		if got := AdminName(strings.TrimSpace(string(r.Stdout))); got != want {
			t.Errorf("%s: %q, want %q", dir, got, want)
		}
	}
}

func (f *fx) mustGit(ctx context.Context, dir string, args ...string) {
	f.t.Helper()
	if r := f.l.Git.Run(ctx, dir, args...); !r.OK() {
		f.t.Fatalf("git %v: %s", args, r.Stderr)
	}
}

func TestRecordFormsAndParse(t *testing.T) {
	b := branchRec("feature/x", "main", "op1", "feature-x")
	b.Lease = &Lease{RenewedAt: time.Unix(1, 0).UTC(), TTLSec: 30}
	keys := func(r Record) map[string]json.RawMessage {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	full := keys(b)
	for _, k := range []string{
		"schema", "role", "state", "op_id", "operation", "branch", "target", "owner", "lease", "worktrees",
		"work_branch", "placeholder_branch", "keep", "move", "started_at", "stage", "stage_started_at", "current_command", "tips",
		"attempts", "config_digest", "context", "stop",
	} {
		if _, ok := full[k]; !ok {
			t.Errorf("BRANCH record lacks %q", k)
		}
	}
	if string(full["stop"]) != "null" {
		t.Errorf("stop = %s", full["stop"])
	}
	tgt := keys(b.Short(RoleTarget))
	want := []string{"schema", "role", "state", "op_id", "branch", "owner", "lease"}
	if len(tgt) != len(want) {
		t.Errorf("TARGET keys = %v", tgt)
	}
	for _, k := range want {
		if _, ok := tgt[k]; !ok {
			t.Errorf("TARGET record lacks %q", k)
		}
	}
	adm := keys(Record{Schema: 1, Role: RoleAdmin, State: StateRunning, Lease: b.Lease})
	if len(adm) != 5 {
		t.Errorf("admin keys = %v", adm)
	}
	raw, _ := json.Marshal(b)
	back, err := Parse(raw)
	if err != nil || back.Branch != "feature/x" || back.Worktrees[0].Admin != "feature-x" || back.Lease.TTLSec != 30 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	for name, in := range map[string]string{
		"garbage": "not json", "schema0": `{"role":"branch","state":"running"}`,
		"newer": `{"schema":2,"role":"branch","state":"running"}`, "role": `{"schema":1,"role":"x","state":"running"}`,
		"state": `{"schema":1,"role":"branch","state":"x"}`,
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	if _, err := Parse([]byte(`{"schema":2,"role":"branch","state":"running"}`)); !errors.Is(err, ErrNewerSchema) {
		t.Errorf("newer schema error = %v", err)
	}
	if _, err := (Record{Role: "weird"}).MarshalJSON(); err == nil {
		t.Error("unknown role marshalled")
	}
}

func TestAcquireWritesBlobRecordWithCreateIfAbsent(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	set, err := f.l.Acquire(ctx, Spec{Ref: BranchRef("feature/x"), Role: RoleBranch, Rec: branchRec("feature/x", "main", "op1")})
	if err != nil {
		t.Fatal(err)
	}
	h := set.Locks[0]
	st, err := f.l.Read(ctx, h.Ref)
	if err != nil || st.Rec == nil || st.Rec.OpID != "op1" || st.Rec.State != StateRunning {
		t.Fatalf("read: %+v %v", st, err)
	}
	if st.Rec.Lease == nil || st.Rec.Lease.TTLSec != 30 {
		t.Fatalf("lease = %+v", st.Rec.Lease)
	}
	if h.OID() != st.OID {
		t.Errorf("OID() %s != ref %s", h.OID(), st.OID)
	}
	if typ := f.l.Git.Run(ctx, f.repo.Dir, "cat-file", "-t", st.OID); strings.TrimSpace(string(typ.Stdout)) != "blob" {
		t.Errorf("ref does not point at a blob: %s", typ.Stdout)
	}
	// the create used the zero oid as the expected old value
	ups := f.rec.matching("update-ref", h.Ref)
	if len(ups) != 1 || len(ups[0]) != 4 || strings.Trim(ups[0][3], "0") != "" {
		t.Fatalf("update-ref calls = %v", ups)
	}
	// a second creator fails
	if _, err := f.l.write(ctx, h.Ref, branchRec("feature/x", "main", "op2"), "", nil); !errors.Is(err, ErrTaken) {
		t.Fatalf("second create: %v", err)
	}
	if f.refOID(h.Ref) != st.OID {
		t.Error("second creator changed the ref")
	}
}

func TestWaiterNeverCreatesWhileARecordExists(t *testing.T) {
	f := newFx(t)
	ref := BranchRef("feature/x")
	f.plant(ref, branchRec("feature/x", "main", "other"))
	f.l.Config.Wait = 5 * time.Second
	_, err := f.l.Acquire(t.Context(), Spec{Ref: ref, Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.rec.matching("update-ref", ref)); n != 1 { // only the plant
		t.Errorf("update-ref calls on a held lock = %d, want only the planting one", n)
	}
	if len(f.sleeps) != 5 {
		t.Errorf("polled %d times, want once a second for 5 s", len(f.sleeps))
	}
	for _, d := range f.sleeps {
		if d != time.Second {
			t.Errorf("poll interval %v", d)
		}
	}
}

func TestBusyNamesHolderAndMessagesEveryTenSeconds(t *testing.T) {
	f := newFx(t)
	ref := BranchRef("feature/x")
	other := branchRec("feature/x", "main", "20261107T101500Z-3f9c2a")
	other.StartedAt = f.clock.Now().Add(-4 * time.Second).UTC().Truncate(time.Second)
	f.plant(ref, other)
	f.l.Config.Wait = 25 * time.Second
	_, err := f.l.Acquire(t.Context(), Spec{Ref: ref, Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v", err)
	}
	h := busy.Holder
	if h.OpID != "20261107T101500Z-3f9c2a" || h.Operation != "land" || h.Branch != "feature/x" || h.Stage != "rebasing" ||
		h.PID != 41721 || h.Host != "laptop.local" || h.AgeS < 4 {
		t.Errorf("holder = %+v", h)
	}
	lines := strings.Split(strings.TrimSpace(f.out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "land feature/x") || !strings.Contains(lines[0], "rebasing") {
		t.Errorf("messages = %q", lines)
	}
	o := busy.Outcome("land", "feature/x", "main")
	if o.Outcome != outcome.Busy || o.Exit != 9 || o.Holder.OpID != h.OpID {
		t.Errorf("outcome = %+v", o)
	}
}

func TestWaiterAcquiresWhenHolderReleases(t *testing.T) {
	f := newFx(t)
	ref := BranchRef("feature/x")
	oid := f.plant(ref, branchRec("feature/x", "main", "other"))
	n := 0
	inner := f.l.Sleep
	f.l.Sleep = func(ctx context.Context, d time.Duration) error {
		n++
		if n == 3 {
			f.mustGit(ctx, f.repo.Dir, "update-ref", "-d", ref, oid)
		}
		return inner(ctx, d)
	}
	set, err := f.l.Acquire(t.Context(), Spec{Ref: ref, Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	if err != nil {
		t.Fatal(err)
	}
	if set.Locks[0].Record().OpID != "me" {
		t.Error("not our record")
	}
}

func TestRenewSwapsFromCurrentRecord(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	set, err := f.l.Acquire(ctx, Spec{Ref: BranchRef("feature/x"), Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	if err != nil {
		t.Fatal(err)
	}
	h := set.Locks[0]
	old := h.OID()
	f.clock.Advance(10 * time.Second)
	if h.RenewAge() != 10*time.Second {
		t.Errorf("age = %v", h.RenewAge())
	}
	if err := h.Renew(ctx); err != nil {
		t.Fatal(err)
	}
	if h.OID() == old || f.refOID(h.Ref) != h.OID() {
		t.Error("renewal did not swap in a new record")
	}
	if h.RenewAge() != 0 {
		t.Errorf("age after renewal = %v", h.RenewAge())
	}
	// the swap names the holder's current record as the expected value
	ups := f.rec.matching("update-ref", h.Ref)
	last := ups[len(ups)-1]
	if len(last) != 4 || last[3] != old {
		t.Errorf("renewal update-ref = %v, want old %s", last, old)
	}
	// a field write between renewals is not reverted by the next renewal
	if err := h.Update(ctx, func(r *Record) { r.Stage = "landing"; r.Move.Intent = ptr("moving") }); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(10 * time.Second)
	if err := h.Renew(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := f.l.Read(ctx, h.Ref)
	if st.Rec.Stage != "landing" || st.Rec.Move.Intent == nil || *st.Rec.Move.Intent != "moving" {
		t.Errorf("renewal reverted a field write: %+v", st.Rec)
	}
	if st.Rec.Lease.RenewedAt.Before(time.Now().Add(-time.Minute)) {
		t.Error("renewed_at not refreshed")
	}
	if err := h.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.l.Read(ctx, h.Ref); !st.Absent {
		t.Error("release left the ref")
	}
	if err := h.Release(ctx); err != nil {
		t.Errorf("second release: %v", err)
	}
	if err := h.Renew(ctx); !errors.Is(err, ErrLockLost) {
		t.Errorf("renew after release: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestRenewWithChangedValueMeansLockTaken(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	set, _ := f.l.Acquire(ctx, Spec{Ref: BranchRef("feature/x"), Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	h := set.Locks[0]
	stolen := branchRec("feature/x", "main", "thief")
	stolen.State = StateStopped
	taken := f.plant(h.Ref, stolen)
	err := h.Renew(ctx)
	if !errors.Is(err, ErrLockLost) || !errors.Is(err, ErrTaken) {
		t.Fatalf("err = %v", err)
	}
	if f.refOID(h.Ref) != taken {
		t.Error("lost renewal overwrote the new holder")
	}
	if len(f.sleeps) != 0 {
		t.Error("a changed value must not be retried")
	}
}

func TestRenewWithUnchangedValueRetriesFiveTimesThenLockLost(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	f.jit = time.Millisecond
	set, _ := f.l.Acquire(ctx, Spec{Ref: BranchRef("feature/x"), Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	h := set.Locks[0]
	before := h.OID()
	f.clock.Advance(time.Second)
	lock := f.lockFile(h.Ref) // git's own ref lock held: update-ref fails, value unchanged
	n0 := len(f.rec.matching("update-ref", h.Ref))
	err := h.Renew(ctx)
	if !errors.Is(err, ErrLockLost) || !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.rec.matching("update-ref", h.Ref)) - n0; n != 1+WriteRetries {
		t.Errorf("update-ref attempts = %d, want %d", n, 1+WriteRetries)
	}
	if len(f.sleeps) != WriteRetries {
		t.Errorf("backoffs = %d", len(f.sleeps))
	}
	if f.refOID(h.Ref) != before || h.OID() != before {
		t.Error("record changed")
	}
	// the blocker goes away: the same holder can renew again
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := h.Renew(ctx); err != nil {
		t.Fatalf("renew after the ref lock cleared: %v", err)
	}
}

func TestRenewStopsBeforeTwoThirdsOfTheLease(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	f.jit = 8 * time.Second // each backoff costs 8 s of the 20 s (two thirds of 30 s) budget
	set, _ := f.l.Acquire(ctx, Spec{Ref: BranchRef("feature/x"), Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	h := set.Locks[0]
	f.lockFile(h.Ref)
	n0 := len(f.rec.matching("update-ref", h.Ref))
	err := h.Renew(ctx)
	if !errors.Is(err, ErrLockLost) || errors.Is(err, ErrWriteFailed) {
		t.Fatalf("err = %v (want lock-lost from the time budget, not the retry count)", err)
	}
	if n := len(f.rec.matching("update-ref", h.Ref)) - n0; n != 3 {
		t.Errorf("attempts = %d, want 3", n)
	}
	if age := h.RenewAge(); age >= 20*time.Second {
		t.Errorf("gave up at age %v, not before two thirds of the lease", age)
	}
}

func TestMaybeRenewOnlyWhileStepWithinTimeout(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	set, _ := f.l.Acquire(ctx, Spec{Ref: BranchRef("feature/x"), Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	h := set.Locks[0]
	deadline := f.clock.Now().Add(5 * time.Second)
	if err := h.Update(ctx, func(r *Record) {
		r.CurrentCommand = &CurrentCommand{Argv0: "git", PID: 1, StartedAt: f.clock.Now(), Deadline: deadline}
	}); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(3 * time.Second)
	if ok, err := h.MaybeRenew(ctx); !ok || err != nil {
		t.Fatalf("within timeout: %v %v", ok, err)
	}
	oid := h.OID()
	f.clock.Advance(3 * time.Second) // past the deadline
	if ok, err := h.MaybeRenew(ctx); ok || err != nil || h.OID() != oid {
		t.Fatalf("past deadline: renewed=%v err=%v", ok, err)
	}
	// no current command: renews
	if err := h.Update(ctx, func(r *Record) { r.CurrentCommand = nil }); err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.MaybeRenew(ctx); !ok {
		t.Error("no step: should renew")
	}
}

func TestRunRenewalKeepsLeaseFreshAndReportsLoss(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	f.l.Interval = 10 * time.Millisecond
	set, _ := f.l.Acquire(ctx, Spec{Ref: BranchRef("feature/x"), Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	h := set.Locks[0]
	first := h.OID()
	f.clock.Advance(time.Second) // a new renewed_at gives a new blob
	stop, lost := h.RunRenewal(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for h.OID() == first && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.OID() == first {
		t.Fatal("no background renewal happened")
	}
	if f.refOID(h.Ref) != h.OID() {
		t.Error("ref and held record differ")
	}
	// someone takes the lock: the loop reports lock-lost and ends
	f.plant(h.Ref, branchRec("feature/x", "main", "thief"))
	select {
	case err := <-lost:
		if !errors.Is(err, ErrLockLost) {
			t.Errorf("lost = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("loss not reported")
	}
	stop()
}

func TestAcquireTakesLocksInAscendingOrder(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	op := branchRec("feature/x", "main", "me", "wt-b", "_main", "wt-b")
	op.Worktrees[1].Roles = []string{"destination"}
	specs := f.l.LandLocks(op)
	var refs []string
	for _, s := range specs {
		refs = append(refs, s.Ref)
	}
	wantSet := []string{BranchRef("main"), BranchRef("feature/x"), WorktreeRef("wt-b"), WorktreeRef("_main")}
	if strings.Join(refs, " ") != strings.Join(wantSet, " ") {
		t.Fatalf("land locks = %v", refs)
	}
	roles := []Role{specs[0].Role, specs[1].Role, specs[2].Role, specs[3].Role}
	if roles[0] != RoleTarget || roles[1] != RoleBranch || roles[2] != RoleWorktree || roles[3] != RoleWorktree {
		t.Errorf("roles = %v", roles)
	}
	if specs[0].Rec.Branch != "feature/x" {
		t.Errorf("TARGET record names the operation's BRANCH, got %q", specs[0].Rec.Branch)
	}
	// request in reverse order: creation order must still be ascending
	for i, j := 0, len(specs)-1; i < j; i, j = i+1, j-1 {
		specs[i], specs[j] = specs[j], specs[i]
	}
	set, err := f.l.Acquire(ctx, specs...)
	if err != nil {
		t.Fatal(err)
	}
	var created []string
	for _, c := range f.rec.matching("update-ref") {
		if len(c) == 4 {
			created = append(created, c[1])
		}
	}
	sorted := append([]string(nil), refs...)
	sortStrings(sorted)
	if strings.Join(created, " ") != strings.Join(sorted, " ") {
		t.Errorf("creation order %v, want ascending %v", created, sorted)
	}
	// the short and full forms landed in the right refs
	for _, ref := range refs {
		st, _ := f.l.Read(ctx, ref)
		if st.Rec == nil {
			t.Fatalf("%s unreadable", ref)
		}
	}
	if st, _ := f.l.Read(ctx, BranchRef("main")); st.Rec.Role != RoleTarget || st.Rec.Branch != "feature/x" || st.Rec.Stage != "" {
		t.Errorf("TARGET record = %+v", st.Rec)
	}
	if st, _ := f.l.Read(ctx, BranchRef("feature/x")); st.Rec.Role != RoleBranch || len(st.Rec.Worktrees) != 3 {
		t.Errorf("BRANCH record = %+v", st.Rec)
	}
	if set.ByRef(BranchRef("main")) == nil || set.ByRef("nope") != nil {
		t.Error("ByRef")
	}
	if err := set.Release(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if st, _ := f.l.Read(ctx, ref); !st.Absent {
			t.Errorf("%s left behind", ref)
		}
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestRebaseLockSetHasBranchAndWorktreesOnly(t *testing.T) {
	f := newFx(t)
	op := branchRec("feature/x", "main", "me", "feature-x")
	op.Operation = "rebase"
	var refs []string
	for _, s := range f.l.RebaseLocks(op) {
		refs = append(refs, s.Ref)
	}
	if strings.Join(refs, " ") != BranchRef("feature/x")+" "+WorktreeRef("feature-x") {
		t.Errorf("rebase locks = %v", refs)
	}
}

func TestSetIsAllOrNothingWithReleaseAndRetry(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	op := branchRec("feature/x", "main", "me", "wt-b")
	blocker := branchRec("feature/y", "main", "blocker")
	blocker.Role = RoleWorktree
	blockerOID := f.plant(WorktreeRef("wt-b"), blocker.Short(RoleWorktree))
	f.l.Config.Wait = 3 * time.Second
	f.jit = 400 * time.Millisecond
	_, err := f.l.Acquire(ctx, f.l.LandLocks(op)...)
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Ref != WorktreeRef("wt-b") || busy.Holder.OpID != "blocker" {
		t.Fatalf("err = %v", err)
	}
	// the two earlier locks were taken and released on every round; none left
	for _, ref := range []string{BranchRef("main"), BranchRef("feature/x")} {
		if st, _ := f.l.Read(ctx, ref); !st.Absent {
			t.Errorf("%s left held after a failed set", ref)
		}
		if n := len(f.rec.matching("update-ref", ref)); n < 4 {
			t.Errorf("%s: %d update-refs, want create+delete per round", ref, n)
		}
	}
	retried := false
	for _, d := range f.sleeps {
		if d > 0 && d <= SetRetryMax {
			retried = true
		}
		if d > SetRetryMax {
			t.Errorf("retry wait %v exceeds 500 ms", d)
		}
	}
	if !retried {
		t.Errorf("no 0-500 ms retry wait: %v", f.sleeps)
	}
	// once the blocker goes away the whole set is taken
	f.mustGit(ctx, f.repo.Dir, "update-ref", "-d", WorktreeRef("wt-b"), blockerOID)
	set, err := f.l.Acquire(ctx, f.l.LandLocks(op)...)
	if err != nil || len(set.Locks) != 3 {
		t.Fatalf("set: %v %v", set, err)
	}
}

func TestStoppedRecordRefusals(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	stopped := branchRec("main", "", "stopper")
	stopped.State, stopped.Lease = StateStopped, nil
	stopped.Stop = &Stop{Reason: "conflict", Retryable: true}
	f.plant(BranchRef("main"), stopped)
	// BRANCH role -> stopped-pending
	_, err := f.l.Acquire(ctx, Spec{Ref: BranchRef("main"), Role: RoleBranch, Rec: branchRec("main", "x", "me")})
	var ref *RefusedError
	if !errors.As(err, &ref) || ref.Reason != outcome.RefStoppedPending {
		t.Fatalf("branch: %v", err)
	}
	if o := ref.Outcome("rebase", "main", ""); o.Exit != 22 || o.Reason != "stopped-pending" {
		t.Errorf("outcome = %+v", o)
	}
	// TARGET role -> target-stopped, and the lock set is released
	op := branchRec("feature/x", "main", "me")
	_, err = f.l.Acquire(ctx, f.l.LandLocks(op)...)
	if !errors.As(err, &ref) || ref.Reason != outcome.RefTargetStopped {
		t.Fatalf("target: %v", err)
	}
	if len(f.sleeps) != 0 {
		t.Error("a stopped record must be refused without waiting")
	}
	if st, _ := f.l.Read(ctx, BranchRef("feature/x")); !st.Absent {
		t.Error("BRANCH lock left held")
	}
}

func TestUnreadableRecordIsHeldByALiveHolder(t *testing.T) {
	cases := map[string]func(f *fx, ref string) string{
		"not a blob": func(f *fx, ref string) string {
			f.mustGit(f.t.Context(), f.repo.Dir, "update-ref", ref, "HEAD")
			return f.refOID(ref)
		},
		"unparseable": func(f *fx, ref string) string { return f.plantRaw(ref, []byte("{not json")) },
		"newer schema": func(f *fx, ref string) string {
			return f.plantRaw(ref, []byte(`{"schema":2,"role":"branch","state":"running"}`))
		},
		"stopped-looking but garbage": func(f *fx, ref string) string {
			return f.plantRaw(ref, []byte(`{"schema":1,"role":"branch","state":"stopped","x":`))
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			ctx := t.Context()
			ref := BranchRef("feature/x")
			oid := mk(f, ref)
			st, err := f.l.Read(ctx, ref)
			if err != nil || st.Unreadable == nil || st.Rec != nil || st.Absent {
				t.Fatalf("read = %+v %v", st, err)
			}
			_, err = f.l.Acquire(ctx, Spec{Ref: ref, Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
			var rf *RefusedError
			if !errors.As(err, &rf) || rf.Reason != outcome.RefUnreadableLock {
				t.Fatalf("err = %v", err)
			}
			if len(f.sleeps) != 0 || f.refOID(ref) != oid {
				t.Error("an unreadable lock is never waited out, taken over or converted")
			}
			if len(f.rec.matching("update-ref", "-d")) != 0 {
				t.Error("unreadable ref deleted")
			}
		})
	}
}

func TestWriteFailureWithUnchangedRefIsRetriedThenReported(t *testing.T) {
	f := newFx(t)
	f.jit = time.Millisecond
	ref := BranchRef("feature/x")
	f.lockFile(ref)
	_, err := f.l.Acquire(t.Context(), Spec{Ref: ref, Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")})
	if !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.rec.matching("update-ref", ref)); n != 1+WriteRetries {
		t.Errorf("attempts = %d", n)
	}
}

func TestStaleLockFiles(t *testing.T) {
	f := newFx(t)
	ctx := t.Context()
	ours := f.lockFile(BranchRef("feature/x"))
	young := f.lockFile(BranchRef("young"))
	heads := f.lockFile("refs/heads/feature/y")
	old := f.clock.Now().Add(-2 * time.Minute)
	for _, p := range []string{ours, heads} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	stale, err := f.l.StaleLockFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range stale {
		got[s.Path] = s.Ours
		if s.Age < StaleLockAge {
			t.Errorf("%s age %v", s.Path, s.Age)
		}
	}
	if len(got) != 2 || !got[ours] || got[heads] {
		t.Fatalf("stale = %v", got)
	}
	if _, ok := got[young]; ok {
		t.Error("young lock file reported")
	}
	removed, err := f.l.RemoveStaleLockFiles(ctx)
	if err != nil || len(removed) != 1 || removed[0] != ours {
		t.Fatalf("removed = %v %v", removed, err)
	}
	for p, want := range map[string]bool{ours: false, heads: true, young: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", p, err == nil, want)
		}
	}
}

func TestOnWaitReportsBusyAndAcquiredAfterWaiting(t *testing.T) {
	f := newFx(t)
	ref := BranchRef("feature/x")
	f.plant(ref, branchRec("feature/x", "main", "holder-op"))
	var got []LockWait
	f.l.OnWait = func(_ context.Context, w LockWait) { got = append(got, w) }
	f.l.Config.Wait = 3 * time.Second
	if _, err := f.l.Acquire(t.Context(), Spec{Ref: ref, Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")}); err == nil {
		t.Fatal("want busy")
	}
	if len(got) != 1 || got[0].Result != WaitBusy || got[0].Lock != ref || got[0].Holder.OpID != "holder-op" ||
		got[0].Holder.Stage != "rebasing" || got[0].Waited != 3*time.Second {
		t.Fatalf("busy wait = %+v", got)
	}

	// the holder goes away while the waiter polls: acquired after a wait
	got = nil
	f.l.Config.Wait = 10 * time.Second
	sleeps := 0
	inner := f.l.Sleep
	f.l.Sleep = func(ctx context.Context, d time.Duration) error {
		sleeps++
		if sleeps == 2 {
			if r := f.l.Git.Run(ctx, f.repo.Dir, "update-ref", "-d", ref); !r.OK() {
				t.Errorf("delete: %s", r.Stderr)
			}
		}
		return inner(ctx, d)
	}
	if _, err := f.l.Acquire(t.Context(), Spec{Ref: ref, Role: RoleBranch, Rec: branchRec("feature/x", "main", "me")}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Result != WaitAcquired || got[0].Holder.OpID != "holder-op" || got[0].Waited != 2*time.Second {
		t.Fatalf("acquired wait = %+v", got)
	}
}

func TestOnWaitSilentWithoutWaiting(t *testing.T) {
	f := newFx(t)
	called := false
	f.l.OnWait = func(context.Context, LockWait) { called = true }
	ref := BranchRef("feature/z")
	if _, err := f.l.Acquire(t.Context(), Spec{Ref: ref, Role: RoleBranch, Rec: branchRec("feature/z", "main", "me")}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("OnWait fired for an uncontended acquire")
	}
}
