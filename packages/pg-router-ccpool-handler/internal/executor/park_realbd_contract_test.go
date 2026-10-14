//go:build contract

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/gitenv"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/parknote"
)

// Real-bd regression guard for the park note (bead pg2-78mxs).
//
// WHY THIS FILE EXISTS (global rule V-3): the handler parks a bead as human and
// releases it in ONE `bd update`, and what a bead looks like afterwards (the
// label, the cleared assignee, the comment a human-queue reader sees) is a
// property of the REAL tool, which the fake runners in parknote_test.go only
// impersonate. These tests drive the two real paths that park a bead as human
// (waitDone's death branch, and the second consecutive needs_input stall)
// against a REAL `bd` in a throwaway store and a REAL git repository holding
// the bead's drain worktree, then read the bead back: it carries `human`, is
// open and unassigned, and has exactly one park note with every required
// element. A third test fails the comment write and asserts the label and the
// release (the pg2-0fsuu invariant) still land.
//
// Hermetic store: see dtest.NewRealBD (embedded dolt private to a t.TempDir(),
// every BEADS_*/BD_*/WORKSPACE_ROOT variable dropped, BEADS_DOLT_AUTO_START=0;
// no dolt server is started or stopped and nothing shared is touched). The git
// repository is private to the test too, and runs under gitenv's hermetic
// environment.
//
// Behind the `contract` build tag; the
// `pg-router-ccpool-handler-bd-contract-test` hook in flake.nix runs it. Run it
// directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run RealBD ./internal/executor/

// recordingBD wraps the real runner, records the verb of every call in order
// and fails every call whose verb is failVerb (a bd hiccup on that write).
type recordingBD struct {
	inner    beads.Runner
	failVerb string
	mu       sync.Mutex
	verbs    []string
}

func (b *recordingBD) Run(ctx context.Context, args ...string) (string, error) {
	b.mu.Lock()
	b.verbs = append(b.verbs, args[0])
	b.mu.Unlock()
	if b.failVerb != "" && args[0] == b.failVerb {
		return "", errors.New("injected bd hiccup on " + args[0])
	}
	return b.inner.Run(ctx, args...)
}

func (b *recordingBD) writes() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var w []string
	for _, v := range b.verbs {
		if v == "comment" || v == "update" {
			w = append(w, v)
		}
	}
	return w
}

func rbGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitenv.Command(context.Background(), dir, args...)
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// drainRepo creates a private git repository with the bead's drain worktree
// (branch drain/<id>) holding two commits, and returns the repo root and the
// worktree path.
func drainRepo(t *testing.T, id string) (repo, wt string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	rbGit(t, repo, "init", "-q", "-b", "main")
	rbGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	wt = filepath.Join(repo, ".worktrees", id)
	rbGit(t, repo, "worktree", "add", "-q", "-b", "drain/"+id, wt)
	rbGit(t, wt, "commit", "-q", "--allow-empty", "-m", "one")
	rbGit(t, wt, "commit", "-q", "--allow-empty", "-m", "two")
	return repo, wt
}

// claimed creates a bead, claims it as the runner's actor and optionally adds a
// label, as a dispatch (and a prior stall) leave it.
func claimed(ctx context.Context, t *testing.T, r *beads.CLIRunner, labels ...string) string {
	t.Helper()
	out, err := r.Run(ctx, "q", "park note subject")
	if err != nil {
		t.Fatalf("bd q: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		t.Fatalf("bd q returned no id; stdout=%q", out)
	}
	if out, err := r.Run(ctx, "update", id, "--claim"); err != nil {
		t.Fatalf("claim %s: %v\n%s", id, err, out)
	}
	for _, l := range labels {
		if out, err := r.Run(ctx, "update", id, "--add-label", l); err != nil {
			t.Fatalf("label %s: %v\n%s", l, err, out)
		}
	}
	return id
}

// commentTexts reads the bead's comments back through the real bd.
func commentTexts(ctx context.Context, t *testing.T, r *beads.CLIRunner, id string) []string {
	t.Helper()
	out, err := r.Run(ctx, "comments", id, "--json")
	if err != nil {
		t.Fatalf("bd comments: %v\n%s", err, out)
	}
	var env struct {
		Data []struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode comments: %v\n%s", err, out)
	}
	var texts []string
	for _, c := range env.Data {
		texts = append(texts, c.Text)
	}
	return texts
}

func parkNotes(texts []string) []string {
	var notes []string
	for _, c := range texts {
		if strings.Contains(c, parknote.Marker) {
			notes = append(notes, c)
		}
	}
	return notes
}

// assertParked reads the bead back and asserts the human label and the release.
func assertParked(ctx context.Context, t *testing.T, r *beads.CLIRunner, id string) {
	t.Helper()
	iss, err := beads.ShowObj(ctx, r, id)
	if err != nil {
		t.Fatalf("ShowObj: %v", err)
	}
	if !iss.HasLabel("human") {
		t.Errorf("the bead must carry human; labels=%v", iss.Labels)
	}
	if iss.Status != "open" || iss.Assignee != "" {
		t.Errorf("the bead must be released (open, no assignee): status=%q assignee=%q", iss.Status, iss.Assignee)
	}
}

// assertOnePark asserts exactly one park note carrying every required element.
func assertOnePark(t *testing.T, texts []string, trigger string, wt string, extra ...string) {
	t.Helper()
	notes := parkNotes(texts)
	if len(notes) != 1 {
		t.Fatalf("want exactly one park note, got %d in comments %q", len(notes), texts)
	}
	for _, want := range append([]string{
		trigger,                     // the trigger
		"role ",                     // the role
		"Session: ",                 // the session
		wt,                          // the drain worktree path
		"(branch drain/",            // and branch
		"2 commit(s) ahead of main", // with its commit state
		"No person-question was identified",
		"resume-or-land",
	}, extra...) {
		if !strings.Contains(notes[0], want) {
			t.Errorf("the park note must contain %q; got:\n%s", want, notes[0])
		}
	}
}

// deathRun drives waitDone's death branch for a worker dying under a real bd
// store and a real drain worktree. failVerb injects a bd failure on that verb.
func deathRun(t *testing.T, failVerb string) (ctx context.Context, r *beads.CLIRunner, id, wt string, rec *recordingBD, werr error) {
	t.Helper()
	const actor = "pgii-pool__worker"
	ctx, r = dtest.NewRealBD(t, actor)
	id = claimed(ctx, t, r)
	var repo string
	repo, wt = drainRepo(t, id)

	cfg := fastCfg()
	cfg.RepoRoot = repo
	role := workerRole(cfg) // add-human on_failure
	role.CCPool.Actor = actor
	name := "pg-router-worker-" + id
	dead := ccpool.Session{ExternalID: name, Live: false, State: ccpool.StateErrored, CloseReason: "handler"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{dead}, {dead}, {dead}}}

	rec = &recordingBD{inner: r, failVerb: failVerb}
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	e := &ccpoolRun{deps: Deps{CC: cc, BD: rec, Cfg: cfg, Now: clk.Now, Tick: clk.TickAdvancing()}}
	werr = e.waitDone(ctx, nil, DispatchContext{Role: role, Item: item.Item{ID: id}}, name)
	return ctx, r, id, wt, rec, werr
}

func TestPark_RealBD_DeathParksWithOneNote(t *testing.T) {
	ctx, r, id, wt, rec, werr := deathRun(t, "")
	if !errors.Is(werr, ErrSessionDied) {
		t.Fatalf("want ErrSessionDied, got %v", werr)
	}
	assertParked(ctx, t, r, id)
	assertOnePark(t, commentTexts(ctx, t, r, id), "session died before completing", wt, "ccpool close reason: handler")
	// The note is written BEFORE the update that makes the bead visible as human.
	if w := rec.writes(); !slices.Equal(w, []string{"comment", "update"}) {
		t.Errorf("writes = %v, want the comment then the label-and-release update", w)
	}
}

// The pg2-0fsuu invariant: when the comment write fails the label and the
// release still land together, and the bead is never left in_progress.
func TestPark_RealBD_DeathCommentFailureStillParksAndReleases(t *testing.T) {
	ctx, r, id, _, rec, werr := deathRun(t, "comment")
	if !errors.Is(werr, ErrSessionDied) {
		t.Fatalf("want ErrSessionDied, got %v", werr)
	}
	assertParked(ctx, t, r, id)
	if notes := parkNotes(commentTexts(ctx, t, r, id)); len(notes) != 0 {
		t.Errorf("the comment write was failed, so no note may exist: %q", notes)
	}
	if w := rec.writes(); !slices.Equal(w, []string{"comment", "update"}) {
		t.Errorf("writes = %v, want the failed comment then the update", w)
	}
}

// stallRun drives the second consecutive needs_input stall of a bead (it already
// carries the strike label) against a real bd store and a real drain worktree.
func stallRun(t *testing.T, failVerb string) (ctx context.Context, r *beads.CLIRunner, id, wt string, rec *recordingBD, werr error) {
	t.Helper()
	ctx, r = dtest.NewRealBD(t, relActor)
	id = claimed(ctx, t, r, drainStallLabel)
	var repo string
	repo, wt = drainRepo(t, id)

	rig := newStallRig(t, releaseRole(config.Default()), func(c *config.Config) {
		c.RepoRoot = repo
		// A coarse injected poll keeps the 17 simulated minutes to ~20 real bd reads.
		c.PollInterval = time.Minute
	})
	rec = &recordingBD{inner: r, failVerb: failVerb}
	rig.e.deps.BD = rec
	rig.d.Item.ID = id
	in := stallT0.Add(time.Minute)
	rig.lastWrite = in.Add(-5 * time.Second)
	rig.cc.script = needsInputBetween(in, time.Time{})
	werr = rig.run(stallT0.Add(3 * time.Hour))
	return ctx, r, id, wt, rec, werr
}

func TestPark_RealBD_StallParksWithOneNote(t *testing.T) {
	ctx, r, id, wt, rec, werr := stallRun(t, "")
	if !errors.Is(werr, ErrNeedsInputStalled) {
		t.Fatalf("want ErrNeedsInputStalled, got %v", werr)
	}
	assertParked(ctx, t, r, id)
	assertOnePark(t, commentTexts(ctx, t, r, id), "needs_input stall bound hit twice in a row", wt, "bound 15m0s")
	if w := rec.writes(); !slices.Equal(w, []string{"comment", "update"}) {
		t.Errorf("writes = %v, want the comment then the label-and-release update", w)
	}
}

func TestPark_RealBD_StallCommentFailureStillParksAndReleases(t *testing.T) {
	ctx, r, id, _, _, werr := stallRun(t, "comment")
	if !errors.Is(werr, ErrNeedsInputStalled) {
		t.Fatalf("want ErrNeedsInputStalled, got %v", werr)
	}
	assertParked(ctx, t, r, id)
	if notes := parkNotes(commentTexts(ctx, t, r, id)); len(notes) != 0 {
		t.Errorf("the comment write was failed, so no note may exist: %q", notes)
	}
}
