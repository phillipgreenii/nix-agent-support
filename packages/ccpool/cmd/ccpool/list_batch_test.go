//go:build !contract

package main

// Real-tmux tests for the batched list read and `list --no-facets`. They run in
// the hermetic sandbox hermetic_test.go creates (TMUX_TMPDIR points inside it) on
// a private -L socket, against the real tmux and the real git: tmux's target
// resolution and pane semantics are exactly what the batch must reproduce, so
// nothing here stands in for either tool. The launch counters are PATH shims
// that log argv and then exec the real binary.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/gitfacet"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
)

// shimOnPath puts a logging wrapper for the named tool first on PATH and returns
// the log file: one line per launch holding its argv. The wrapper execs the real
// tool, so behaviour is unchanged.
func shimOnPath(t *testing.T, tool string) string {
	t.Helper()
	real, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("%s not installed", tool)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, tool+".log")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexec %q \"$@\"\n", log, real)
	if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil { //nolint:gosec // a test wrapper must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func logLines(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' })
}

func resetLog(t *testing.T, log string) {
	t.Helper()
	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// tmuxSubcommands names the tmux subcommand of each logged launch
// ("-L <socket> <subcommand> ...").
func tmuxSubcommands(lines []string) []string {
	var out []string
	for _, l := range lines {
		if f := strings.Fields(l); len(f) >= 3 {
			out = append(out, f[2])
		}
	}
	return out
}

type batchFixture struct {
	socket string
	client *tmux.Client
	rows   []store.Session
	repoA  string
	repoB  string
	spaced string // a non-repo directory whose name contains a space
}

// newBatchFixture builds a default-pool sandbox with a private tmux socket, two
// git repos, nLive live rows spread over them, one crashed row (remain-on-exit
// failed, dead pane), one absent row (a state row with no tmux session) and one
// row whose session has a split pane, then writes the rows to the pool's store.
func newBatchFixture(t *testing.T, nLive int) *batchFixture {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := isolateLabelerEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	registerTmuxCleanup(t, cfg.Tmux.Socket)
	f := &batchFixture{socket: cfg.Tmux.Socket, client: tmux.NewClient(cfg.Tmux.Socket)}

	for _, p := range []*string{&f.repoA, &f.repoB} {
		repo := gittest.New(t, gitfixture.RepoOptions{InitialBranch: "main"})
		if _, err := repo.Commit(t.Context(), "init", nil); err != nil {
			t.Fatalf("commit: %v", err)
		}
		*p = repo.Dir
	}
	f.spaced = filepath.Join(base, "with space")
	if err := os.MkdirAll(f.spaced, 0o700); err != nil {
		t.Fatal(err)
	}

	add := func(id, cwd string) store.Session {
		s := store.Session{
			ExternalID: id, ClaudeSessionID: "csid-" + id, Name: id, CWD: cwd,
			State: store.Working, TmuxSession: "cc-" + id, LastActivityAt: time.Now().Unix(),
		}
		f.rows = append(f.rows, s)
		return s
	}
	start := func(s store.Session, argv ...string) {
		if err := f.client.NewSession(s.TmuxSession, s.CWD, nil, argv); err != nil {
			t.Fatalf("NewSession %s: %v", s.TmuxSession, err)
		}
	}
	sleep := []string{"sleep", "300"}
	for i := 0; i < nLive; i++ {
		cwd := f.repoA
		if i%2 == 1 {
			cwd = f.repoB
		}
		start(add(fmt.Sprintf("live%d", i), cwd), sleep...)
	}
	crashed := add("crashed", f.repoA)
	start(crashed, "sh", "-c", "exit 3")
	waitPaneDead(t, cfg.Tmux.Socket, crashed.TmuxSession)
	add("absent", f.repoA)
	split := add("split", f.repoB)
	start(split, sleep...)
	// The split makes the new pane (in a different directory) the active one.
	if out, err := exec.CommandContext(t.Context(), "tmux", "-L", f.socket, "split-window", "-t", split.TmuxSession, "-c", f.spaced, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("split-window: %v\n%s", err, out)
	}

	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	for _, r := range f.rows {
		if err := st.Insert(context.Background(), r); err != nil {
			t.Fatalf("Insert %s: %v", r.ExternalID, err)
		}
	}
	return f
}

func waitPaneDead(t *testing.T, socket, session string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(t.Context(), "tmux", "-L", socket, "display-message", "-p", "-t", session, "#{pane_dead}").Output()
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("pane of %s never died", session)
}

// The batch must equal what the per-row calls report, for a live row, a crashed
// row (dead pane kept by remain-on-exit), an absent row and a session whose
// active pane is a split in another directory (a path with a space in it).
func TestListPanes_equalsPerRowCalls(t *testing.T) {
	f := newBatchFixture(t, 2)
	panes, err := f.client.ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	for _, r := range f.rows {
		name := r.TmuxSession
		wantLive := f.client.HasSession(name)
		got, present := panes[name]
		if got.Live != wantLive {
			t.Errorf("%s: batch live = %v (present=%v), per-row HasSession = %v", name, got.Live, present, wantLive)
		}
		if wantLive {
			wantPath, err := f.client.PaneCurrentPath(name)
			if err != nil {
				t.Fatalf("PaneCurrentPath(%s): %v", name, err)
			}
			if got.Path != wantPath {
				t.Errorf("%s: batch path = %q, per-row PaneCurrentPath = %q", name, got.Path, wantPath)
			}
		}
	}
	// Spot-check the fixture really covers each case it claims to.
	if !panes["cc-live0"].Live || panes["cc-crashed"].Live {
		t.Errorf("fixture: live0 live=%v crashed live=%v, want true and false", panes["cc-live0"].Live, panes["cc-crashed"].Live)
	}
	if _, ok := panes["cc-absent"]; ok {
		t.Error("fixture: the absent row has a tmux session")
	}
	want, _ := filepath.EvalSymlinks(f.spaced)
	if got, _ := filepath.EvalSymlinks(panes["cc-split"].Path); got != want {
		t.Errorf("split session path = %q, want the split pane's own directory %q (spaces intact)", panes["cc-split"].Path, want)
	}
	// Exact name equality: a prefix of a live session's name is not that session.
	if _, ok := panes["cc-live"]; ok {
		t.Error("a name that is only a prefix of cc-live0 must not match")
	}
}

// A socket with no server is an empty answer, not an error.
func TestListPanes_noServerIsEmpty(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateLabelerEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	registerTmuxCleanup(t, cfg.Tmux.Socket)
	panes, err := tmux.NewClient(cfg.Tmux.Socket).ListPanes()
	if err != nil || len(panes) != 0 {
		t.Errorf("ListPanes with no server = (%v, %v), want an empty map and no error", panes, err)
	}
}

func runListJSON(t *testing.T, args ...string) string {
	t.Helper()
	out, code := captureStdout(t, func() int { return runList(append([]string{"--all", "--json"}, args...)) })
	if code != 0 {
		t.Fatalf("runList = %d (%s)", code, out)
	}
	return strings.TrimSpace(out)
}

// ccpool list launches ONE tmux process however many live rows there are; the
// per-row path (the negative control) launches three per live row.
func TestList_tmuxLaunchesConstantWithBatch(t *testing.T) {
	for _, n := range []int{2, 6} {
		t.Run(fmt.Sprintf("%d-live-rows", n), func(t *testing.T) {
			f := newBatchFixture(t, n)
			log := shimOnPath(t, "tmux")

			runListJSON(t)
			if got := tmuxSubcommands(logLines(t, log)); !reflect.DeepEqual(got, []string{"list-panes"}) {
				t.Errorf("batched list tmux launches = %v, want exactly [list-panes]", got)
			}
			resetLog(t, log)
			runListJSON(t, "--no-facets")
			if got := tmuxSubcommands(logLines(t, log)); !reflect.DeepEqual(got, []string{"list-panes"}) {
				t.Errorf("--no-facets tmux launches = %v, want exactly [list-panes]", got)
			}

			// Negative control: the old per-row reads over the same live rows.
			resetLog(t, log)
			live := 0
			for _, r := range f.rows {
				if tmux.HasSession(f.socket, r.TmuxSession) {
					live++
					_, _ = tmux.PaneCurrentPath(f.socket, r.TmuxSession)
				}
			}
			subs := tmuxSubcommands(logLines(t, log))
			// Per live row has-session, pane_dead and the path (3); the crashed
			// row has-session and pane_dead (2); the absent row has-session (1).
			want := 3*live + 2 + 1
			if len(subs) != want || live != n+1 {
				t.Errorf("per-row tmux launches = %d over %d live rows (%v), want %d: the control must show the old cost", len(subs), live, subs, want)
			}
		})
	}
}

// --no-facets launches no git at all; without it git runs at most four times per
// distinct effective cwd, however many rows share a cwd.
func TestList_gitLaunches(t *testing.T) {
	f := newBatchFixture(t, 6) // six live rows over two repos
	log := shimOnPath(t, "git")

	runListJSON(t, "--no-facets")
	if got := logLines(t, log); len(got) != 0 {
		t.Errorf("--no-facets launched git %d times: %v", len(got), got)
	}

	out := runListJSON(t)
	var rows []struct {
		CWD  string `json:"cwd"`
		Live bool   `json:"live"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	distinct := map[string]bool{}
	for _, r := range rows {
		if r.Live {
			distinct[r.CWD] = true
		}
	}
	got := len(logLines(t, log))
	if got == 0 || got > 4*len(distinct) {
		t.Errorf("git launches = %d for %d distinct live cwds, want 1..%d", got, len(distinct), 4*len(distinct))
	}
	if len(distinct) >= len(f.rows) {
		t.Fatalf("fixture: %d distinct cwds over %d rows, rows must share cwds for this to prove memoization", len(distinct), len(f.rows))
	}
}

// Facet contract: --no-facets reports the launch dir as cwd and no git facets;
// without the flag the output is byte-identical to the old per-row pipeline.
func TestList_facetContract(t *testing.T) {
	f := newBatchFixture(t, 3)

	var light []map[string]any
	if err := json.Unmarshal([]byte(runListJSON(t, "--no-facets")), &light); err != nil {
		t.Fatal(err)
	}
	if len(light) != len(f.rows) {
		t.Fatalf("--no-facets rows = %d, want %d", len(light), len(f.rows))
	}
	byID := map[string]store.Session{}
	for _, r := range f.rows {
		byID[r.ExternalID] = r
	}
	for _, row := range light {
		id, _ := row["external_id"].(string)
		src, ok := byID[id]
		if !ok {
			t.Fatalf("unexpected row %v", row)
		}
		if row["cwd"] != row["launch_dir"] || row["launch_dir"] != src.CWD {
			t.Errorf("%s: cwd=%v launch_dir=%v, want both the launch dir %q", id, row["cwd"], row["launch_dir"], src.CWD)
		}
		for _, k := range []string{"git_repo_root", "worktree", "branch"} {
			if _, ok := row[k]; ok {
				t.Errorf("%s: --no-facets must not report %s (got %v)", id, k, row[k])
			}
		}
		if wantLive := f.client.HasSession(src.TmuxSession); row["live"] != wantLive {
			t.Errorf("%s: live = %v, want %v even with --no-facets", id, row["live"], wantLive)
		}
	}

	// The old pipeline: per-row HasSession and PaneCurrentPath, an unmemoized
	// git resolve, the same renderer.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	stored, err := st.List(context.Background()) // the same rows in the order list reads them
	if err != nil {
		t.Fatal(err)
	}
	want, err := renderListJSON(stored, true, "", tmux.HasSession, tmux.PaneCurrentPath, gitfacet.Resolve,
		nil, cfg.Tmux.Socket, time.Now(), time.Duration(cfg.List.DoneTTL), time.Duration(cfg.List.FailedTTL))
	if err != nil {
		t.Fatal(err)
	}
	if got := runListJSON(t); got != want {
		t.Errorf("facet-resolving output differs from the per-row pipeline\n got: %s\nwant: %s", got, want)
	}
	if !strings.Contains(want, `"branch":"main"`) {
		t.Errorf("fixture: the baseline carries no git facets, so the comparison proves nothing: %s", want)
	}
}
