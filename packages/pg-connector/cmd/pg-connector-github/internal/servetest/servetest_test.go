package servetest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// awaitLockFree is what Daemon.Kill waits on: a lock still held (here by a
// holder that lets go after a while, as a forked child does when its exec
// closes the inherited descriptor) is waited out, not reported as free.
func TestAwaitLockFree_WaitsForTheHolderToLetGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	holder := flock.New(path)
	if locked, err := holder.TryLock(); err != nil || !locked {
		t.Fatalf("fixture: take the lock: %v, %v", locked, err)
	}
	const hold = 300 * time.Millisecond
	start := time.Now()
	go func() {
		time.Sleep(hold)
		_ = holder.Unlock()
	}()
	if !awaitLockFree(path, 10*time.Second) {
		t.Fatal("the lock was never reported free")
	}
	if got := time.Since(start); got < hold {
		t.Errorf("reported free after %s, while the holder still had it (%s)", got, hold)
	}
}

func TestAwaitLockFree_GivesUpWhileTheLockIsStillHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	holder := flock.New(path)
	if locked, err := holder.TryLock(); err != nil || !locked {
		t.Fatalf("fixture: take the lock: %v, %v", locked, err)
	}
	defer func() { _ = holder.Unlock() }()
	if awaitLockFree(path, 150*time.Millisecond) {
		t.Fatal("a held lock was reported free")
	}
}

func TestAwaitLockFree_AFreeOrAbsentLockIsFree(t *testing.T) {
	if !awaitLockFree(filepath.Join(t.TempDir(), "never-created.lock"), time.Second) {
		t.Fatal("an absent lock file was reported held")
	}
}

// A killed daemon that was busy forking gh calls leaves its lock held for a
// moment after the process is reaped (the forked child holds the descriptor
// until its exec). Kill must not return before the lock is free, so the start
// that follows never meets "another instance holds the lock". Measured on an
// unfixed Kill: about one kill in ten left the lock held, for up to tens of
// milliseconds, so 25 cycles catch an unfixed Kill with near certainty.
func TestKill_ReturnsOnlyOnceTheLockIsFree(t *testing.T) {
	d := NewDaemon(t, Options{
		Hooks: "poll_interval=2ms,viewer_interval=2ms",
		Before: func(d *Daemon) {
			d.Shim.SetPRs(MineExpr, PR{NodeID: "PR_a", Repo: "o/w", Number: 1})
			d.Shim.SetPRs(TeamExpr, PR{NodeID: "PR_a", Repo: "o/w", Number: 1})
		},
	})
	for i := 0; i < 25; i++ {
		d.Start(t)
		time.Sleep(time.Duration(i%7) * 20 * time.Millisecond)
		d.Kill(t)
		lk := flock.New(filepath.Join(d.StateDir, "daemon.lock"))
		locked, err := lk.TryLock()
		if err != nil || !locked {
			t.Fatalf("cycle %d: Kill returned with the lock still held (%v, %v)", i, locked, err)
		}
		_ = lk.Unlock()
	}
}

// runShim runs the fake gh as a summary call for PR_a (and, with viewer, as the
// viewer query) under a stand-in cat that, once it has read a node fixture, holds
// the call until a "go" file exists. It returns the call's answer once the test's
// during function (run while the call is held) has returned.
func runShim(t *testing.T, s *Shim, query string, hold bool, during func()) string {
	t.Helper()
	realCat, err := exec.LookPath("cat")
	if err != nil {
		t.Fatal(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	d := shortDir(t)
	bin := filepath.Join(d, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeCat := "#!" + bash + "\n" +
		"\"" + realCat + "\" \"$@\"\n" +
		"rc=$?\n" +
		"case \"${1-}\" in\n" +
		"*/node.PR_a.json)\n" +
		"  : >\"" + d + "/reached\"\n" +
		"  while [ ! -f \"" + d + "/go\" ]; do sleep 0.01; done\n" +
		"  ;;\n" +
		"esac\n" +
		"exit $rc\n"
	if err := os.WriteFile(filepath.Join(bin, "cat"), []byte(fakeCat), 0o700); err != nil {
		t.Fatal(err)
	}
	if !hold {
		if err := os.WriteFile(filepath.Join(d, "go"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Path, "api", "graphql", "-F", "query="+query, "-f", "ids[]=PR_a")
	cmd.Env = append(os.Environ(), "FAKE_GH_DIR="+s.Dir, "GH_TOKEN=fixture-token", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if hold {
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(d, "reached")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the shim never reached the read of the node fixture")
			}
		}
		if during != nil {
			during()
		}
		if err := os.WriteFile(filepath.Join(d, "go"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the shim failed: %v (%s)", err, out.String())
	}
	return out.String()
}

const (
	shimSummaryDoc = "query($ids: [ID!]!) { rateLimit { cost remaining resetAt } nodes(ids: $ids) { id } }"
	shimViewerDoc  = "query { viewer { login } rateLimit { cost remaining resetAt } }"
)

// A summary answer is one consistent snapshot of the node fixtures and the
// remaining count. A test that changes a pull request and lowers the count in
// one Shim.Update relies on every answer that shows the new head also showing
// the lowered count, and the reverse; a call descheduled between its reads
// (seconds, on a loaded host) used to answer one half of it. The call is held
// after its read of the node fixture, the Update runs, the call is released, and
// the answer must show both halves, not the old head under the new count.
func TestShim_ASummaryAnswerNeverShowsHalfOfAnUpdate(t *testing.T) {
	s := newShim(t, filepath.Join(shortDir(t), "gh"), "fixture-token")
	pr := PR{NodeID: "PR_a", Repo: "o/w", Number: 1}
	s.SetPRs("", pr)
	s.Remaining(4900)
	got := runShim(t, s, shimSummaryDoc, true, func() {
		moved := pr
		moved.Head = "head-2"
		s.Update(func() {
			s.RemainingSummary(500)
			s.SetPRs("", moved)
		})
	})
	if !strings.Contains(got, `"remaining":500`) || !strings.Contains(got, `"headRefOid":"head-2"`) {
		t.Errorf("the answer = %s, want the new head and the lowered count together", got)
	}
}

// The count lowered for the summary answers reaches the daemon in no other
// answer: a search or viewer answer that comes first must not close the reserve
// before the summary answer that carries the change.
func TestShim_RemainingSummaryLowersTheSummaryAnswerOnly(t *testing.T) {
	s := newShim(t, filepath.Join(shortDir(t), "gh"), "fixture-token")
	s.SetPRs("", PR{NodeID: "PR_a", Repo: "o/w", Number: 1})
	s.Remaining(4900)
	s.RemainingSummary(500)
	if got := runShim(t, s, shimSummaryDoc, false, nil); !strings.Contains(got, `"remaining":500`) {
		t.Errorf("the summary answer = %s, want 500", got)
	}
	if got := runShim(t, s, shimViewerDoc, false, nil); !strings.Contains(got, `"remaining":4900`) {
		t.Errorf("the viewer answer = %s, want 4900", got)
	}
}
