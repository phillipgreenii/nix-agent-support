package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

func dur(t *testing.T, s string) config.Duration {
	t.Helper()
	d, err := config.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *env) renew(k LeaseKey, ttl *config.Duration, md map[string]string) (Record, error) {
	e.t.Helper()
	return e.m.Renew(e.t.Context(), k, ttl, md)
}

// seed edits the state under the lock, as another command would have.
func (e *env) seed(f func(st *state.State)) {
	e.t.Helper()
	lk, err := e.m.Store.Lock(e.t.Context(), time.Second)
	if err != nil {
		e.t.Fatal(err)
	}
	defer lk.Unlock()
	f(lk.State())
	if err := lk.Commit(); err != nil {
		e.t.Fatal(err)
	}
}

func str(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestTTLMatrix(t *testing.T) {
	e := newFakeEnv(t)
	rec := e.mustLease(LeaseRequest{Name: "a", Detach: true})
	if rec.LeaseExpiration != nil {
		t.Fatalf("expiry = %v", rec.LeaseExpiration)
	}
	got, err := e.renew(LeaseKey{Name: "a"}, nil, nil)
	if err != nil || got.LeaseExpiration != nil {
		t.Errorf("renew without ttl on none: %v %v", got.LeaseExpiration, err)
	}
	one := dur(t, "1h")
	got, err = e.renew(LeaseKey{Name: "a"}, &one, nil)
	if err != nil || str(got.LeaseExpiration) != "2026-11-14T11:00:00Z" {
		t.Errorf("renew 1h on none: %v %v", got.LeaseExpiration, err)
	}

	half := dur(t, "30m")
	b := e.mustLease(LeaseRequest{Name: "b", Detach: true, TTL: &half})
	e.now = e.now.Add(10 * time.Minute)
	got, err = e.renew(LeaseKey{LeaseID: *b.LeaseID}, nil, nil)
	if err != nil || str(got.LeaseExpiration) != "2026-11-14T10:40:00Z" {
		t.Errorf("renew recorded 30m: %v %v", got.LeaseExpiration, err)
	}

	_, err = e.renew(LeaseKey{Name: "b"}, &config.Duration{}, nil)
	wantErr(t, err, "usage", 2)
	none := config.NoDuration
	_, err = e.renew(LeaseKey{Name: "b"}, &none, nil)
	wantErr(t, err, "usage", 2)
	if got, _ := e.renew(LeaseKey{Name: "b"}, nil, nil); str(got.LeaseExpiration) != "2026-11-14T10:40:00Z" {
		t.Errorf("a refused renew moved the expiry: %v", got.LeaseExpiration)
	}
	_, err = e.renew(LeaseKey{}, nil, nil)
	wantErr(t, err, "usage", 2)
	_, err = e.renew(LeaseKey{Name: "b", LeaseID: "l-x"}, nil, nil)
	wantErr(t, err, "usage", 2)
}

func TestRenewStates(t *testing.T) {
	e := newFakeEnv(t)
	h := dur(t, "1h")
	a := e.mustLease(LeaseRequest{Name: "a", Branch: "ab", Create: true, TTL: &h})
	b := e.mustLease(LeaseRequest{Name: "b", Detach: true, TTL: &h})

	// By name and by id.
	for _, k := range []LeaseKey{{Name: "A"}, {LeaseID: *a.LeaseID}} {
		if got, err := e.renew(k, nil, nil); err != nil || *got.LeaseID != *a.LeaseID || got.WorktreePath != a.WorktreePath {
			t.Errorf("renew %+v: %+v %v", k, got, err)
		}
	}

	// Expired but not reclaimed: renewable.
	e.now = e.now.Add(2 * time.Hour)
	if got, err := e.renew(LeaseKey{Name: "a"}, nil, nil); err != nil || got.Status != state.Leased {
		t.Errorf("renew of an expired lease: %v %v", got.Status, err)
	}

	// Reclaiming: lease-lost, naming the preserved branch.
	e.seed(func(st *state.State) {
		for i := range st.Worktrees {
			if st.Worktrees[i].ID == b.ID {
				st.Worktrees[i].Status = state.Reclaiming
			}
		}
		st.Preserved = append(st.Preserved, state.Preserved{Branch: "pg-gwm/dirty-reaped-x", LeaseID: *b.LeaseID, Name: "b", At: e.now})
	})
	_, err := e.renew(LeaseKey{LeaseID: *b.LeaseID}, nil, nil)
	if ee := wantErr(t, err, "lease-lost", 25); ee.Details["preserved_branch"] != "pg-gwm/dirty-reaped-x" {
		t.Errorf("details = %v", ee.Details)
	}

	// A reclaimed tombstone, by name.
	e.seed(func(st *state.State) {
		st.Tombstones = append(st.Tombstones, state.Tombstone{Kind: "reclaimed", LeaseID: "l-gone000000", Name: "gone", PreservedBranch: "pg-gwm/dirty-reaped-g", At: e.now})
	})
	_, err = e.renew(LeaseKey{Name: "gone"}, nil, nil)
	if ee := wantErr(t, err, "lease-lost", 25); ee.Details["preserved_branch"] != "pg-gwm/dirty-reaped-g" {
		t.Errorf("details = %v", ee.Details)
	}

	// Released, also after its worktree was leased again.
	if _, err := e.m.Release(t.Context(), LeaseKey{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err = e.renew(LeaseKey{Name: "a"}, nil, nil)
	wantErr(t, err, "lease-released", 24)
	e.mustLease(LeaseRequest{Name: "c", Branch: "ab", Reset: true})
	_, err = e.renew(LeaseKey{LeaseID: *a.LeaseID}, nil, nil)
	wantErr(t, err, "lease-released", 24)

	// Broken.
	e.seed(func(st *state.State) {
		st.Worktrees = append(st.Worktrees, state.Worktree{
			ID: "w-brokenbrok", Slot: 99, Path: filepath.Join(e.m.Cfg.WorktreeRoot, "x"), Status: state.Broken, Reason: "boom",
			Lease: &state.Lease{ID: "l-brokenbrok", Name: "bk"}, CreatedAt: e.now,
		})
	})
	_, err = e.renew(LeaseKey{LeaseID: "l-brokenbrok"}, nil, nil)
	wantErr(t, err, "broken", 28)

	// Nothing.
	_, err = e.renew(LeaseKey{LeaseID: "l-nope000000"}, nil, nil)
	wantErr(t, err, "not-found", 4)
	_, err = e.renew(LeaseKey{Name: "nope"}, nil, nil)
	if ee := wantErr(t, err, "not-found", 4); !strings.Contains(ee.Hint, "held names:") {
		t.Errorf("hint = %q", ee.Hint)
	}
}

func TestNotFoundHintCapsHeldNames(t *testing.T) {
	e := newFakeEnv(t, func(c *config.RepoConfig) { c.Max = nil })
	// The hint reads only the state: one real lease, eleven seeded records.
	e.mustLease(LeaseRequest{Name: "na", Detach: true})
	e.seed(func(st *state.State) {
		for i := 1; i < 12; i++ {
			name := "n" + string(rune('a'+i))
			st.Worktrees = append(st.Worktrees, state.Worktree{
				ID: "w-seed" + name, Slot: i + 1, Status: state.Leased,
				Lease: &state.Lease{ID: "l-seed" + name, Name: name, Acquired: epoch},
			})
		}
	})
	_, err := e.renew(LeaseKey{Name: "zzz"}, nil, nil)
	ee := wantErr(t, err, "not-found", 4)
	if names, _ := ee.Details["held_names"].([]string); len(names) != 10 {
		t.Errorf("held names = %v", ee.Details["held_names"])
	}
}

func TestOrRenew(t *testing.T) {
	e := newEnv(t)
	h := dur(t, "1h")
	first := e.mustLease(LeaseRequest{Name: "w", Branch: "feat", Create: true, TTL: &h})
	e.now = e.now.Add(20 * time.Minute)
	rec, err := e.lease(LeaseRequest{Name: "w", OrRenew: true, Branch: "other", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Renewed == nil || !*rec.Renewed || *rec.LeaseID != *first.LeaseID || rec.WorktreePath != first.WorktreePath || str(rec.Branch) != "feat" {
		t.Errorf("or-renew record = %+v", rec)
	}
	if str(rec.LeaseExpiration) != "2026-11-14T11:20:00Z" {
		t.Errorf("expiry = %v", rec.LeaseExpiration)
	}
	if len(rec.Warnings) != 1 || !strings.Contains(rec.Warnings[0], "feat") {
		t.Errorf("warnings = %v", rec.Warnings)
	}
	if out := e.git(e.repo.Dir, "branch", "--list", "other"); out != "" {
		t.Errorf("the ignored branch was created: %q", out)
	}
	// Expired and not reclaimed: renewed with the same id.
	e.now = e.now.Add(3 * time.Hour)
	rec, err = e.lease(LeaseRequest{Name: "w", OrRenew: true})
	if err != nil || !*rec.Renewed || *rec.LeaseID != *first.LeaseID || len(rec.Warnings) != 0 {
		t.Errorf("expired or-renew: %+v %v", rec, err)
	}
	// No lease: a new one, renewed false.
	rec, err = e.lease(LeaseRequest{Name: "fresh", OrRenew: true, Detach: true})
	if err != nil || rec.Renewed == nil || *rec.Renewed {
		t.Errorf("new lease: %+v %v", rec, err)
	}
}

func TestRenewMetadata(t *testing.T) {
	e := newFakeEnv(t)
	first := e.mustLease(LeaseRequest{Name: "m", Owner: "me", Branch: "mb", Create: true, Metadata: map[string]string{"old": "1", "keep": "x"}})
	got, err := e.renew(LeaseKey{Name: "m"}, nil, nil)
	if err != nil || got.Metadata["old"] != "1" || got.Metadata["keep"] != "x" {
		t.Errorf("absent metadata changed: %v %v", got.Metadata, err)
	}
	got, err = e.renew(LeaseKey{Name: "m"}, nil, map[string]string{"new": "2"})
	if err != nil || len(got.Metadata) != 1 || got.Metadata["new"] != "2" {
		t.Errorf("metadata not replaced: %v %v", got.Metadata, err)
	}
	if str(got.Name) != "m" || str(got.Owner) != "me" || got.WorktreePath != first.WorktreePath || str(got.Branch) != "mb" {
		t.Errorf("identity changed: %+v", got)
	}
}

func TestReleaseDirty(t *testing.T) {
	e := newEnv(t)
	check := func(name string, mutate func(path string), wantFile string) {
		t.Helper()
		rec := e.mustLease(LeaseRequest{Name: name, Branch: name, Create: true})
		mutate(rec.WorktreePath)
		before := e.state()
		_, err := e.m.Release(t.Context(), LeaseKey{Name: name})
		ee := wantErr(t, err, "dirty", 23)
		if cmds, _ := ee.Details["commands"].([]string); len(cmds) != 3 || !strings.Contains(cmds[0], "add -A") || !strings.Contains(cmds[1], "stash push -u") || !strings.Contains(cmds[2], "branch <name> HEAD") {
			t.Errorf("%s: commands = %v", name, ee.Details["commands"])
		}
		files, _ := ee.Details["files"].([]string)
		commits, _ := ee.Details["commits"].([]string)
		if wantFile != "" && !strings.Contains(strings.Join(files, " "), wantFile) {
			t.Errorf("%s: files = %v", name, files)
		}
		if wantFile == "" && len(commits) == 0 {
			t.Errorf("%s: no unreachable commits listed", name)
		}
		after := e.state()
		if len(after.Worktrees) != len(before.Worktrees) || after.Worktrees[len(after.Worktrees)-1].Status != state.Leased || len(after.Tombstones) != 0 {
			t.Errorf("%s: the refusal changed the state: %+v", name, after)
		}
	}
	check("tracked", func(p string) {
		if err := os.WriteFile(filepath.Join(p, "tracked.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.git(p, "add", "tracked.txt")
		e.git(p, "commit", "-m", "add tracked")
		if err := os.WriteFile(filepath.Join(p, "tracked.txt"), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
	}, "tracked.txt")
	check("untracked", func(p string) {
		if err := os.WriteFile(filepath.Join(p, "new.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}, "new.txt")
	check("detached", func(p string) {
		e.git(p, "checkout", "--detach")
		e.git(p, "commit", "--allow-empty", "-m", "orphan")
	}, "")

	// Ignored-only: released, and the output names the owner.
	rec := e.mustLease(LeaseRequest{Name: "ign", Owner: "ivy", Branch: "ign", Create: true})
	if err := os.WriteFile(filepath.Join(rec.WorktreePath, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.git(rec.WorktreePath, "add", ".gitignore")
	e.git(rec.WorktreePath, "commit", "-m", "ignore logs")
	if err := os.WriteFile(filepath.Join(rec.WorktreePath, "x.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Release(t.Context(), LeaseKey{LeaseID: *rec.LeaseID})
	if err != nil || str(got.Owner) != "ivy" || got.Status != state.Released {
		t.Fatalf("release: %+v %v", got, err)
	}
	_, err = e.renew(LeaseKey{LeaseID: *rec.LeaseID}, nil, nil)
	wantErr(t, err, "lease-released", 24)
	_, err = e.m.Release(t.Context(), LeaseKey{LeaseID: *rec.LeaseID})
	wantErr(t, err, "lease-released", 24)
	_, err = e.m.Release(t.Context(), LeaseKey{Name: "ign"})
	wantErr(t, err, "lease-released", 24)

	// A reclaimed lease.
	e.seed(func(st *state.State) {
		st.Tombstones = append(st.Tombstones, state.Tombstone{Kind: "reclaimed", LeaseID: "l-reclaimed0", Name: "rc", At: e.now})
	})
	_, err = e.m.Release(t.Context(), LeaseKey{LeaseID: "l-reclaimed0"})
	wantErr(t, err, "lease-lost", 25)
}

func TestReleaseThenLeaseSameName(t *testing.T) {
	e := newFakeEnv(t)
	a := e.mustLease(LeaseRequest{Name: "same", Branch: "sb", Create: true})
	if _, err := e.m.Release(t.Context(), LeaseKey{Name: "same"}); err != nil {
		t.Fatal(err)
	}
	b := e.mustLease(LeaseRequest{Name: "same", CommitIsh: "sb"})
	if *a.LeaseID == *b.LeaseID {
		t.Errorf("lease id reused: %s", *a.LeaseID)
	}
}

func TestLeaseIDNeverReused(t *testing.T) {
	e := newFakeEnv(t)
	seen := map[string]bool{}
	const cycles = 4 // reuse of an id shows by the second cycle; more adds only spawns
	for i := 0; i < cycles; i++ {
		req := LeaseRequest{Name: "cycle", Branch: "cyc", Create: true}
		if i > 0 {
			req = LeaseRequest{Name: "cycle", CommitIsh: "cyc"}
		}
		rec := e.mustLease(req)
		if seen[*rec.LeaseID] {
			t.Fatalf("lease id %s reused", *rec.LeaseID)
		}
		seen[*rec.LeaseID] = true
		if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: *rec.LeaseID}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != cycles || len(e.state().Worktrees) != 1 {
		t.Errorf("ids %d, worktrees %d", len(seen), len(e.state().Worktrees))
	}
}

func TestExpire(t *testing.T) {
	e := newFakeEnv(t)
	h := dur(t, "1h")
	rec := e.mustLease(LeaseRequest{Name: "x", Owner: "olga", Metadata: map[string]string{"k": "v"}, Branch: "xb", Create: true, TTL: &h})
	got, err := e.m.Expire(t.Context(), LeaseKey{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if str(got.Owner) != "olga" || got.Status != state.LeaseExpired || str(got.LeaseExpiration) != "2026-11-14T10:00:00Z" {
		t.Errorf("expire record = %+v", got)
	}
	if str(got.LeaseTTL) != "1h" || got.Metadata["k"] != "v" || str(got.Branch) != "xb" || got.WorktreePath != rec.WorktreePath {
		t.Errorf("expire changed more than the expiry: %+v", got)
	}
	if r, err := e.renew(LeaseKey{Name: "x"}, nil, nil); err != nil || r.Status != state.Leased {
		t.Errorf("holder renew after expire: %v %v", r.Status, err)
	}
	_, err = e.m.Expire(t.Context(), LeaseKey{Name: "nope"})
	wantErr(t, err, "not-found", 4)
	_, err = e.m.Expire(t.Context(), LeaseKey{LeaseID: "l-nope000000"})
	wantErr(t, err, "not-found", 4)
}

func TestRenewDuringSlowCreation(t *testing.T) {
	e := newEnv(t, func(c *config.RepoConfig) { c.LockWait = dur(t, "1s") })
	other := e.mustLease(LeaseRequest{Name: "other", Detach: true})
	slow := e.manager(func(c *config.RepoConfig) { c.LockWait = dur(t, "1s") })
	bp := newBlocking(slow.Prov)
	slow.Prov = bp
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := slow.Lease(ctx, LeaseRequest{Name: "slow", Detach: true, BaseDir: e.repo.Dir})
		done <- err
	}()
	<-bp.started

	start := time.Now()
	if _, err := e.renew(LeaseKey{LeaseID: *other.LeaseID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("renew took %v while another lease was being created", d)
	}
	if got, err := e.renew(LeaseKey{Name: "slow"}, nil, nil); err != nil || got.Status != state.Preparing {
		t.Errorf("renew of the preparing lease: %v %v", got.Status, err)
	}
	_, err := e.m.Release(t.Context(), LeaseKey{Name: "slow"})
	wantErr(t, err, "busy", 9)
	_, err = e.m.Expire(t.Context(), LeaseKey{Name: "slow"})
	wantErr(t, err, "busy", 9)
	cancel()
	<-done

	// A lock held past lock-wait is busy.
	short := e.manager(func(c *config.RepoConfig) { c.LockWait = dur(t, "1s") })
	lk, err := short.Store.Lock(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Unlock()
	_, err = short.Renew(t.Context(), LeaseKey{LeaseID: *other.LeaseID}, nil, nil)
	wantErr(t, err, "busy", 9)
}

func TestInfo(t *testing.T) {
	e := newEnv(t)
	rec := e.mustLease(LeaseRequest{Name: "inf", Owner: "me", Branch: "ib", Create: true})
	sub := filepath.Join(rec.WorktreePath, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	same := func(k InfoKey) {
		t.Helper()
		got, err := e.m.Info(t.Context(), k)
		if err != nil || got.ID != rec.ID || !got.Managed || got.Head != rec.Head || str(got.Name) != "inf" {
			t.Errorf("info %+v: %+v %v", k, got, err)
		}
	}
	for _, k := range []InfoKey{
		{Raw: rec.WorktreePath},
		{Raw: sub},
		{Raw: rec.ID},
		{Raw: *rec.LeaseID},
		{Raw: "inf"},
		{Raw: "INF"},
		{Path: sub},
		{ID: rec.ID},
		{LeaseID: *rec.LeaseID},
		{Name: "inf"},
	} {
		same(k)
	}
	// A forcing flag does not fall back to the other kinds.
	for _, k := range []InfoKey{{ID: "inf"}, {Name: rec.ID}, {LeaseID: rec.ID}, {Path: "inf"}} {
		_, err := e.m.Info(t.Context(), k)
		wantErr(t, err, "not-found", 4)
	}
	_, err := e.m.Info(t.Context(), InfoKey{})
	wantErr(t, err, "usage", 2)
	_, err = e.m.Info(t.Context(), InfoKey{Raw: "a", Name: "b"})
	wantErr(t, err, "usage", 2)
	_, err = e.m.Info(t.Context(), InfoKey{Raw: "no-such"})
	wantErr(t, err, "not-found", 4)

	// Unmanaged: a worktree of the repository's own, and the main checkout.
	own := filepath.Join(e.repo.Root(), "manual-wt")
	e.git(e.repo.Dir, "worktree", "add", "-b", "manual", own)
	for _, p := range []string{own, e.repo.Dir} {
		got, err := e.m.Info(t.Context(), InfoKey{Raw: p})
		if err != nil || got.Managed || got.Status != state.Unmanaged || len(got.Warnings) != 1 || got.Head == "" || got.Metadata == nil {
			t.Errorf("unmanaged %s: %+v %v", p, got, err)
		}
	}
	if got, _ := e.m.Info(t.Context(), InfoKey{Raw: own}); str(got.Branch) != "manual" {
		t.Errorf("unmanaged branch = %v", got.Branch)
	}

	// A broken worktree still has a record.
	e.seed(func(st *state.State) {
		st.Worktrees = append(st.Worktrees, state.Worktree{
			ID: "w-brokenbrok", Slot: 90, Path: filepath.Join(e.m.Cfg.WorktreeRoot, "gone"), Status: state.Broken, Reason: "boom", CreatedAt: e.now,
		})
	})
	got, err := e.m.Info(t.Context(), InfoKey{ID: "w-brokenbrok"})
	if err != nil || got.Status != state.Broken || got.LeaseID != nil {
		t.Errorf("broken: %+v %v", got, err)
	}
}

func TestList(t *testing.T) {
	e := newEnv(t)
	a := e.mustLease(LeaseRequest{Name: "first", Detach: true})
	e.now = e.now.Add(time.Minute)
	b := e.mustLease(LeaseRequest{Name: "second", Detach: true})
	e.now = e.now.Add(time.Minute)
	c := e.mustLease(LeaseRequest{Name: "third", Detach: true})
	if _, err := e.m.Release(t.Context(), LeaseKey{Name: "second"}); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	if _, err := e.m.Expire(t.Context(), LeaseKey{Name: "third"}); err != nil {
		t.Fatal(err)
	}

	recs, err := e.m.List(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || recs[0].ID != a.ID || recs[1].ID != b.ID || recs[2].ID != c.ID {
		t.Fatalf("order = %+v", recs)
	}
	if recs[1].Status != state.Released || recs[2].Status != state.LeaseExpired || recs[0].Status != state.Leased {
		t.Errorf("statuses = %v %v %v", recs[0].Status, recs[1].Status, recs[2].Status)
	}
	for _, r := range recs {
		if !r.Managed {
			t.Errorf("unmanaged row without the flag: %+v", r)
		}
	}
	empty := newEnv(t)
	if recs, err := empty.m.List(t.Context(), false); err != nil || recs == nil || len(recs) != 0 {
		t.Errorf("empty list = %#v %v", recs, err)
	}

	own := filepath.Join(e.repo.Root(), "manual-wt")
	e.git(e.repo.Dir, "worktree", "add", "-b", "manual", own)
	recs, err = e.m.List(t.Context(), true)
	if err != nil || len(recs) != 5 {
		t.Fatalf("with unmanaged: %d %v", len(recs), err)
	}
	un := 0
	for _, r := range recs[3:] {
		if r.Managed || r.Status != state.Unmanaged {
			t.Errorf("row %+v is not marked unmanaged", r)
		}
		un++
	}
	if un != 2 {
		t.Errorf("unmanaged rows = %d", un)
	}
}
