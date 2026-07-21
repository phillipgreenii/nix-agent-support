package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestPerfGuard_SteadyScanZeroFetches is the source-level near-idle proof: after
// a cold scan populates every provider, a second scan over the SAME topology
// (unchanged HEAD mtime, unchanged transcript (path,mtime), same alive pids,
// PR within its found-TTL) performs ZERO fetches and records ZERO new subprocess
// events — i.e. subprocess.spawns_total is flat across a steady tick. It also
// asserts the cold scan fires all four metered kinds at least once (pg2-sewtz
// instrument presence).
func TestPerfGuard_SteadyScanZeroFetches(t *testing.T) {
	// Real HEAD file so the git-branch UntilFileChanges cache holds across scans.
	repo := t.TempDir()
	head := filepath.Join(repo, "HEAD")
	if err := os.WriteFile(head, []byte("ref: refs/heads/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gitN, subN, termN, envN, prN int
	c := New(nil)
	fr := &fakeRec{}
	c.SetRecorder(fr)
	c.PidAlive = func(int) bool { return true }
	c.FetchGitBranch = func(string) (string, string, bool) { gitN++; return "x", head, true }
	c.FetchSubshell = func(int) (int, error) { subN++; return 1, nil }
	c.FetchTerminalHost = func(int) string { termN++; return "tmux" }
	c.FetchEnv = func(int) (map[string]string, error) { envN++; return map[string]string{"A": "1"}, nil }

	// PR backed by a real bounded PRCache so it caches across scans; its LookupFn
	// times the (fake) gh spawn and records pr_lookup only on a real fetch.
	prc := session.NewPRCache(filepath.Join(t.TempDir(), "pr.json"))
	prc.FoundTTL = 15 * time.Minute
	fixed := time.Unix(1000, 0)
	prc.Now = func() time.Time { return fixed }
	prc.LookupFn = func(_ context.Context, _, _ string) (session.PRInfo, bool, error) {
		prN++
		c.Record("pr_lookup", 0)
		return session.PRInfo{Number: 1}, true, nil
	}
	c.PRBackend = prc.Get
	c.PRPrune = prc.Prune

	sessions := []*session.Session{{SessionID: "s1", Cwd: repo, PID: 1, PidAlive: true}}
	mtime := time.Unix(2000, 0)

	scan := func() {
		c.BeginScan()
		for _, s := range sessions {
			c.Env(s.SessionID, s.PID)                         //nolint:errcheck
			c.GitBranch(s.Cwd)                                //
			c.Subshell(s.SessionID, s.PID, "/t.jsonl", mtime) //
			c.TerminalHost(s.SessionID, s.PID)                //
			c.PR(context.Background(), s.Cwd, "x")            //nolint:errcheck
		}
		c.Reconcile(sessions)
	}

	// Cold scan: exactly one fetch per distinct key.
	scan()
	if gitN != 1 || subN != 1 || termN != 1 || envN != 1 || prN != 1 {
		t.Fatalf("cold scan fetch counts: git=%d sub=%d term=%d env=%d pr=%d (want all 1)", gitN, subN, termN, envN, prN)
	}
	for _, k := range []string{"git_branch", "child_procs", "terminal_host", "pr_lookup"} {
		if countKind(fr, k) == 0 {
			t.Fatalf("cold scan: metered kind %q not recorded (presence parity)", k)
		}
	}
	spawnsAfterCold := len(fr.kinds)

	// Steady scan: unchanged inputs → ZERO fetches and ZERO new subprocess events.
	scan()
	if gitN != 1 || subN != 1 || termN != 1 || envN != 1 || prN != 1 {
		t.Fatalf("steady scan re-fetched: git=%d sub=%d term=%d env=%d pr=%d (want all still 1)", gitN, subN, termN, envN, prN)
	}
	if len(fr.kinds) != spawnsAfterCold {
		t.Fatalf("steady scan recorded new subprocess events: cold=%d now=%d (%v)", spawnsAfterCold, len(fr.kinds), fr.kinds)
	}
}
