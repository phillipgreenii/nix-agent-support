package metrics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun/gitruntest"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
	"github.com/phillipgreenii/x/gitfixture"
)

var t0 = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

type fixture struct {
	t    *testing.T
	repo *gitfixture.Repo
	rp   repo.Repo
	c    Collector
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	xdgState, xdgConf := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgState)
	t.Setenv("XDG_CONFIG_HOME", xdgConf)
	r := newBaseRepo(t)
	g := gitruntest.New(t, r)
	rp, err := repo.Resolve(context.Background(), g, r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, repo: r, rp: rp}
	f.c = Collector{
		Version:      "test",
		ConfigPath:   filepath.Join(xdgConf, "pg-gwm", "config.toml"),
		RegistryPath: state.RegistryPath(),
		CountersPath: state.CountersPath(),
		ReaperPath:   state.ReaperPath(),
		Git:          g,
		Now:          func() time.Time { return t0 },
	}
	if err := (state.Registry{Path: f.c.RegistryPath}).Add(rp.Main); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) seed(fn func(*state.State)) {
	f.t.Helper()
	lk, err := state.Open(f.rp.CommonDir).Lock(context.Background(), time.Second)
	if err != nil {
		f.t.Fatal(err)
	}
	defer lk.Unlock()
	fn(lk.State())
	if err := lk.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) scrape() string {
	return Render(f.c.Collect(context.Background()))
}

func want(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l+"\n") {
			t.Errorf("missing %q in:\n%s", l, out)
		}
	}
}

func TestMetricsPerStatus(t *testing.T) {
	f := newFixture(t)
	exp := t0.Add(-90 * time.Second)
	f.seed(func(st *state.State) {
		st.Worktrees = []state.Worktree{
			{ID: "w-a", Status: state.Available},
			{ID: "w-b", Status: state.Broken},
			{ID: "w-c", Status: state.Leased, Lease: &state.Lease{ID: "l-c", Expiry: &exp, Acquired: t0.Add(-time.Hour)}},
			{ID: "w-d", Status: state.Leased, Lease: &state.Lease{ID: "l-d", Acquired: t0.Add(-10 * time.Minute)}},
		}
	})
	out := f.scrape()
	lbl := fmt.Sprintf("repo=%q", f.rp.Label)
	for st, n := range map[string]int{
		"available": 1, "broken": 1, "lease-expired": 1, "leased": 1,
		"preparing": 0, "released": 0, "creating": 0, "reclaiming": 0,
	} {
		want(t, out, fmt.Sprintf("pg_gwm_worktrees{%s,status=%q} %d", lbl, st, n))
	}
	want(
		t, out,
		fmt.Sprintf("pg_gwm_broken_worktrees{%s} 1", lbl),
		fmt.Sprintf("pg_gwm_oldest_expired_lease_age_seconds{%s} 90", lbl),
		fmt.Sprintf("pg_gwm_oldest_no_expiry_lease_age_seconds{%s} 600", lbl),
		fmt.Sprintf("pg_gwm_repo_unreachable{%s} 0", lbl),
		"pg_gwm_config_valid 1",
	)
	if strings.Contains(out, "free_space_floor_bytes") {
		t.Error("floor series exported without a configured floor")
	}
	if !strings.Contains(out, "pg_gwm_free_bytes{") {
		t.Error("no free_bytes series")
	}
}

func TestLastReapAge(t *testing.T) {
	f := newFixture(t)
	out := f.scrape()
	if strings.Contains(out, "reaper_last_success_age") || strings.Contains(out, "last_reap_age") {
		t.Fatalf("age series without reaper.json:\n%s", out)
	}
	ok := t0.Add(-20 * time.Minute)
	err := state.WriteReaperRecord(f.c.ReaperPath, state.ReaperRecord{
		Started: ok, Ended: ok, Outcome: "completed", LastSuccess: &ok,
		Repos: map[string]state.RepoReap{f.rp.Label: {Label: f.rp.Label, At: ok, Outcome: "completed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out = f.scrape()
	want(t, out, "pg_gwm_reaper_last_success_age_seconds 1200",
		fmt.Sprintf("pg_gwm_last_reap_age_seconds{repo=%q} 1200", f.rp.Label))
}

func TestPreservedCountFromRefs(t *testing.T) {
	f := newFixture(t)
	for _, b := range []string{"pg-gwm/dirty-reaped-1", "pg-gwm/dirty-reaped-2", "pg-gwm/other", "feature"} {
		if _, err := f.repo.Client.Run(t.Context(), "branch", b); err != nil {
			t.Fatal(err)
		}
	}
	lbl := fmt.Sprintf("pg_gwm_preserved_branches{repo=%q}", f.rp.Label)
	want(t, f.scrape(), lbl+" 2")
	if _, err := f.repo.Client.Run(t.Context(), "branch", "-D", "pg-gwm/dirty-reaped-1"); err != nil {
		t.Fatal(err)
	}
	want(t, f.scrape(), lbl+" 1")
}

func TestCountersAndHistogram(t *testing.T) {
	f := newFixture(t)
	c := state.Counters{Path: f.c.CountersPath}
	c.Refusal(f.rp.Label, "busy")
	c.Refusal(f.rp.Label, "busy")
	c.Reaped(f.rp.Label, 3, 2)
	c.LeasePhase(f.rp.Label, "create", 3)
	out := f.scrape()
	l := fmt.Sprintf("repo=%q", f.rp.Label)
	want(
		t, out,
		fmt.Sprintf("pg_gwm_refusals_total{%s,error=\"busy\"} 2", l),
		fmt.Sprintf("pg_gwm_refusals_total{%s,error=\"max-reached\"} 0", l),
		fmt.Sprintf("pg_gwm_reap_removed_total{%s} 3", l),
		fmt.Sprintf("pg_gwm_reap_kept_total{%s} 2", l),
		fmt.Sprintf("pg_gwm_lease_seconds_bucket{%s,phase=\"create\",le=\"2\"} 0", l),
		fmt.Sprintf("pg_gwm_lease_seconds_bucket{%s,phase=\"create\",le=\"5\"} 1", l),
		fmt.Sprintf("pg_gwm_lease_seconds_bucket{%s,phase=\"create\",le=\"1800\"} 1", l),
		fmt.Sprintf("pg_gwm_lease_seconds_bucket{%s,phase=\"create\",le=\"+Inf\"} 1", l),
		fmt.Sprintf("pg_gwm_lease_seconds_sum{%s,phase=\"create\"} 3", l),
		fmt.Sprintf("pg_gwm_lease_seconds_count{%s,phase=\"wait\"} 0", l),
	)
}

func TestUnreadableAndUnreachable(t *testing.T) {
	f := newFixture(t)
	// A corrupt state file omits the state-derived series, not the scrape.
	if err := os.MkdirAll(filepath.Join(f.rp.CommonDir, "pg-gwm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.rp.CommonDir, "pg-gwm", "state.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A registered path that is no repository is unreachable.
	gone := filepath.Join(t.TempDir(), "gone")
	if err := (state.Registry{Path: f.c.RegistryPath}).Add(gone); err != nil {
		t.Fatal(err)
	}
	out := f.scrape()
	if strings.Contains(out, "pg_gwm_worktrees{") || strings.Contains(out, "broken_worktrees") {
		t.Errorf("state series exported for unreadable state:\n%s", out)
	}
	want(t, out, fmt.Sprintf("pg_gwm_repo_unreachable{repo=%q} 1", repo.PathLabel(gone)),
		fmt.Sprintf("pg_gwm_repo_unreachable{repo=%q} 0", f.rp.Label))
}

func TestBadConfigFallsBackToDefaults(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.c.ConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.c.ConfigPath, []byte("not = [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := f.scrape()
	want(t, out, "pg_gwm_config_valid 0")
	if !strings.Contains(out, "pg_gwm_worktrees{") {
		t.Errorf("registered repository not served with a bad config:\n%s", out)
	}
}

func TestFloorSeries(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.c.ConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[repos.%q]\nfree-space-floor = 12345\n", f.rp.Main)
	if err := os.WriteFile(f.c.ConfigPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	want(t, f.scrape(), fmt.Sprintf("pg_gwm_free_space_floor_bytes{repo=%q} 12345", f.rp.Label))
}
