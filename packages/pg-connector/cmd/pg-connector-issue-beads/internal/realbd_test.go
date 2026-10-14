//go:build contract

package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// TestBackend_RoundTrip_RealBD is the packet's required round-trip test
// "verified against a real ... bd invocation ... not mocks alone" [bead:
// Files]: create an issue, transition it, show it, and assert the new
// state is reflected — against a REAL `bd` binary in a disposable
// workspace directory.
//
// Behind the `contract` build tag on purpose, mirroring
// packages/pg-pr/pkg/beads/contract_test.go's own resolution to the EXACT
// failure this backend hit while developing this file (bead pg2-5ek6b,
// closed): `bd init`'s database defaults to the issue prefix, but the
// *server* it talks to (`bd dolt set database`'s doc: "default: issue
// prefix or beads"; host/port "auto-detected") is this machine's shared,
// always-running per-user dolt server (org.nixos.beads-dolt-server,
// 127.0.0.1:25252) whenever one is already up — a fresh t.TempDir() does
// NOT get a truly private embedded engine. That makes two things true
// that a bare `go test ./...`-run version of this test got wrong in
// practice: (1) concurrent test binaries can trip a spurious "already
// initialized" collision against that shared server (pg2-5ek6b's own
// diagnosis), so this must stay OFF the default unit-test build (and thus
// off the pre-commit run-unit-tests hook and the plain
// checks.pg-connector-go-tests gate) exactly like pg-pr's own contract
// split; (2) a FIXED prefix persists as a same-named database on that
// shared server across separate test runs (t.TempDir()'s cleanup only
// deletes the local directory, not a server-side database), so prefix
// below is time-based per run rather than a literal constant, to avoid
// colliding with a leftover from an earlier manual `-tags contract` run.
// It never touches the real "pg2" project database (a different name), so
// this is a same-server-different-database isolation, not a
// same-directory one — consistent with pg2-5ek6b's own finding that no
// actual production data was ever at risk, only a same-machine name
// collision.
//
// Run explicitly via `go test -tags contract ./cmd/pg-connector-issue-beads/internal/...`
// (mirroring packages/pb/packages/ccpool's own contract suites); it is
// skipped when `bd` is absent from PATH.
// runDeadline is a hang guard for real `bd` runs, not a performance
// assertion: bd init and round trips are far slower under heavy host load
// (pg2-6gjki, same convention as a1be9c6), so it is deliberately generous.
const runDeadline = 2 * time.Minute

// realBDTestActor is the bd actor the runners under test claim as. It is pinned
// on the CLIRunner (never read from the ambient BEADS_ACTOR, which the hermetic
// child environment no longer carries) so the run does not depend on, or record
// anything in, the operator's identity.
const realBDTestActor = "issue-beads-realbd-test"

func TestBackend_RoundTrip_RealBD(t *testing.T) {
	roundTripRealBD(t)
}

// roundTripRealBD is the body of TestBackend_RoundTrip_RealBD, shared with the
// foreign-git-environment guard so the same real round trip runs under both.
func roundTripRealBD(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}

	dir := t.TempDir()
	env := cleanBDEnv(t)
	prefix := "tp" + fmt.Sprintf("%x", time.Now().UnixNano())[:10]

	initCtx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	initCmd := exec.CommandContext(initCtx, "bd", "init", "--prefix", prefix,
		"--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	initCmd.Dir = dir
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}

	b := New(&CLIRunner{Dir: dir, Env: env, Actor: realBDTestActor})
	ctx, cancelCtx := context.WithTimeout(context.Background(), runDeadline)
	defer cancelCtx()

	created, err := b.Create(ctx, issue.IssueInput{
		Title:       "round-trip probe",
		Priority:    "P1",
		IssueType:   "task",
		Labels:      []string{"probe", "foo,bar"},
		Description: "a round-trip description",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create returned empty ID")
	}
	if created.State != "open" {
		t.Fatalf("Create: State = %q, want open", created.State)
	}
	// Verified live against real bd v1.2.2, not a fake: a comma-bearing
	// label must round-trip as ONE label, not split at the embedded comma
	// [review finding A-33].
	wantLabels := map[string]bool{"probe": true, "foo,bar": true}
	if len(created.Labels) != len(wantLabels) {
		t.Fatalf("Create: Labels = %v, want exactly %v", created.Labels, wantLabels)
	}
	for _, l := range created.Labels {
		if !wantLabels[l] {
			t.Fatalf("Create: unexpected label %q in %v", l, created.Labels)
		}
	}
	if created.Description != "a round-trip description" {
		t.Fatalf("Create: Description = %q, want %q (bead pg2-akfw5, finding 33)", created.Description, "a round-trip description")
	}

	if err := b.Comment(ctx, created.ID, "commenting from the round-trip test"); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	if err := b.Transition(ctx, created.ID, "in_progress"); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	shown, err := b.Show(ctx, created.ID)
	if err != nil {
		t.Fatalf("Show after Transition: %v", err)
	}
	if shown.State != "in_progress" {
		t.Fatalf("Show after Transition: State = %q, want in_progress (the new state was not reflected)", shown.State)
	}
	if shown.ID != created.ID || shown.Title != created.Title {
		t.Fatalf("Show identity mismatch: got %+v, want id/title matching %+v", shown, created)
	}
	if shown.Description != "a round-trip description" {
		t.Fatalf("Show: Description = %q, want %q (bead pg2-akfw5, finding 33: Show previously dropped this)", shown.Description, "a round-trip description")
	}

	// A genuinely unknown id must round-trip as a well-formed ErrNotFound,
	// not a generic failure — exercised here against the real binary rather
	// than a fake, to prove decodeBDEnvelope's classification actually
	// matches real bd output (backend_test.go's fake-runner tests already
	// cover the message shapes; this confirms they were not fabricated).
	if _, err := b.Show(ctx, prefix+"-doesnotexist"); !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("Show(unknown id) = %v, want wrapping ErrNotFound", err)
	}
}

// cleanBDEnv is the environment this file hands to the real bd and to the
// CLIRunner under test: hermeticBDEnv over the parent environment with a
// private HOME, plus BD_JSON_ENVELOPE=1 pinned explicitly (via
// withBDJSONEnvelope) rather than trusting the test-runner's own ambient
// environment to carry it — the exact ambient-dependence bead pg2-8o2cg reports
// for production — so this contract test stays green even when run somewhere
// that doesn't export it (e.g. a stripped CI shell), not only in an
// interactive dev shell where home-manager already exports it.
func cleanBDEnv(t *testing.T) []string {
	t.Helper()
	return withBDJSONEnvelope(hermeticBDEnv(os.Environ(), t.TempDir()))
}

// hermeticBDEnv builds the environment a test hands to the real bd (bead
// pg2-rxs4j, same class as pg2-g3204).
//
// It is an ALLOWLIST, never a scrub. The parent environment contributes only
// the names in hermeticBDPassthrough (and the LC_* locale family); everything
// else, including every variable of an outer git hook (GIT_DIR,
// GIT_INDEX_FILE, GIT_WORK_TREE, GIT_COMMON_DIR, ...) and every workspace
// binding (BEADS_*, BD_*, WORKSPACE_ROOT, ...), is dropped by construction. The
// previous helper stripped only BEADS_DIR, WORKSPACE_ROOT and
// ZR_MACHINE_SUPPORT_WORKSPACE_ROOT, so under a commit hook bd in the
// throwaway workspace followed GIT_DIR/GIT_WORK_TREE to the OUTER repository's
// beads workspace and `bd init` refused with "already initialized".
//
// HOME is not passed through: the caller names a private home directory, which
// also keeps ~/.gitconfig and user-level bd/dolt configuration out of the run.
// BEADS_DOLT_AUTO_START=0 is pinned so bd never spawns its own dolt server.
func hermeticBDEnv(parent []string, home string) []string {
	var out []string
	for _, kv := range parent {
		k, _, _ := strings.Cut(kv, "=")
		if hermeticBDPassthrough[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return append(out, "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "BEADS_DOLT_AUTO_START=0")
}

// hermeticBDPassthrough is the complete list of parent-environment variables a
// bd child may inherit. Adding a name is a deliberate decision; it MUST NOT be
// a name that selects a git repository or a beads workspace.
var hermeticBDPassthrough = map[string]bool{
	"PATH": true, "TMPDIR": true, "USER": true, "LOGNAME": true,
	"LANG": true, "TZ": true, "TERM": true,
}

// TestBackend_Update_ReopenClearDefer_RealBD pins pg2-vhs3e against a REAL bd
// (v1.2.2): a reopen that does not clear the deferral keeps the issue out of
// `bd ready`, and Update with ClearDefer ("--defer=") makes it ready again in
// the same call. This is the evidence behind pg-desk's review-request reopen.
func TestBackend_Update_ReopenClearDefer_RealBD(t *testing.T) {
	reopenClearDeferRealBD(t)
}

// reopenClearDeferRealBD is the body of the test above, shared with the
// foreign-git-environment guard.
func reopenClearDeferRealBD(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}
	dir := t.TempDir()
	env := cleanBDEnv(t)
	prefix := "tp" + fmt.Sprintf("%x", time.Now().UnixNano())[:10]

	bd := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bd", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	bd("init", "--prefix", prefix, "--non-interactive", "-q", "--skip-agents", "--skip-hooks")

	b := New(&CLIRunner{Dir: dir, Env: env, Actor: realBDTestActor})
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	created, err := b.Create(ctx, issue.IssueInput{Title: "defer probe", IssueType: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ready := func() bool {
		return strings.Contains(bd("ready", "--json"), created.ID)
	}

	// The review worker's release: open, unclaimed, deferred.
	bd("update", "--status=open", "--assignee=", "--defer=+12h", "--json", "--", created.ID)
	if ready() {
		t.Fatal("a deferred issue must not be ready")
	}
	// A reopen WITHOUT ClearDefer keeps the deferral: still not ready.
	if _, err := b.Update(ctx, created.ID, issue.IssueUpdateFields{Status: "open", ClearAssignee: true}); err != nil {
		t.Fatalf("Update (no ClearDefer): %v", err)
	}
	if ready() {
		t.Fatal("reopen without ClearDefer unexpectedly cleared the deferral; the pg2-vhs3e premise no longer holds")
	}
	// A reopen WITH ClearDefer makes it ready in the same call.
	if _, err := b.Update(ctx, created.ID, issue.IssueUpdateFields{Status: "open", ClearAssignee: true, ClearDefer: true}); err != nil {
		t.Fatalf("Update (ClearDefer): %v", err)
	}
	if !ready() {
		t.Fatal("reopen with ClearDefer must leave the issue ready")
	}
}

// hookLeakEnv is a git-hook-shaped environment plus every workspace binding a
// test process could have inherited, all pointing at "another repository".
var hookLeakEnv = []string{
	"GIT_DIR=/outer/repo/.git",
	"GIT_WORK_TREE=/outer/repo",
	"GIT_INDEX_FILE=/outer/repo/.git/index",
	"GIT_COMMON_DIR=/outer/repo/.git",
	"GIT_OBJECT_DIRECTORY=/outer/repo/.git/objects",
	"GIT_PREFIX=packages/pg-connector/",
	"GIT_CEILING_DIRECTORIES=/outer",
	"GIT_ANYTHING_FUTURE=1",
	"BEADS_DIR=/outer/repo/.beads",
	"BEADS_DOLT_SERVER_PORT=25252",
	"BD_ACTOR=outer",
	"WORKSPACE_ROOT=/outer",
	"ZR_MACHINE_SUPPORT_WORKSPACE_ROOT=/outer",
	"PG_CONNECTOR_ISSUE_BEADS_DIR=/outer/repo/.beads",
}

// TestHermeticBDEnv_DropsEverythingOffTheAllowlist is the unit-tier regression
// guard for pg2-rxs4j: a git-hook environment (and any other workspace
// binding) in the parent MUST NOT reach the child, and the names the child does
// need MUST still arrive. Reintroducing a deny-list or a plain pass-through of
// os.Environ fails it at the first GIT_* name.
func TestHermeticBDEnv_DropsEverythingOffTheAllowlist(t *testing.T) {
	parent := append([]string{
		"PATH=/usr/bin", "HOME=/outer/home", "TMPDIR=/tmp/x", "LANG=en_US.UTF-8",
		"LC_ALL=C", "SOMETHING_ELSE=1",
	}, hookLeakEnv...)
	have := map[string]string{}
	for _, kv := range hermeticBDEnv(parent, "/private/home") {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := have[k]; dup {
			t.Errorf("%s appears twice in the child environment", k)
		}
		have[k] = v
	}
	for _, kv := range hookLeakEnv {
		k, _, _ := strings.Cut(kv, "=")
		if v, leaked := have[k]; leaked {
			t.Errorf("%s=%s leaked into the child environment", k, v)
		}
	}
	for k := range have {
		if strings.HasPrefix(k, "GIT_") && k != "GIT_CONFIG_NOSYSTEM" {
			t.Errorf("unexpected GIT_ variable %s in the child environment", k)
		}
	}
	if _, leaked := have["SOMETHING_ELSE"]; leaked {
		t.Error("a name off the allowlist leaked")
	}
	want := map[string]string{
		"HOME": "/private/home", "PATH": "/usr/bin", "TMPDIR": "/tmp/x",
		"LANG": "en_US.UTF-8", "LC_ALL": "C", "GIT_CONFIG_NOSYSTEM": "1",
		"BEADS_DOLT_AUTO_START": "0",
	}
	for k, v := range want {
		if have[k] != v {
			t.Errorf("%s = %q, want %q", k, have[k], v)
		}
	}
}

// foreignRepoWithBeads is another repository with a beads workspace in it.
func foreignRepoWithBeads(t *testing.T) string {
	t.Helper()
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd not on PATH")
	}
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "issue-beads-foreign"})
	if out, err := runRealBD(bdPath, repo.Dir, withBDJSONEnvelope(hermeticBDEnv(os.Environ(), t.TempDir())),
		"init", "--prefix", "foreign", "--non-interactive", "-q", "--skip-agents", "--skip-hooks"); err != nil {
		t.Fatalf("bd init in the foreign repository: %v\n%s", err, out)
	}
	return repo.Dir
}

// foreignGitEnv is what a git hook run inside repo exports.
func foreignGitEnv(repo string) []string {
	return []string{
		"GIT_DIR=" + filepath.Join(repo, ".git"),
		"GIT_WORK_TREE=" + repo,
		"GIT_INDEX_FILE=" + filepath.Join(repo, ".git", "index"),
	}
}

func runRealBD(bdPath, dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, bdPath, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestBackend_RealBD_ForeignGitEnv_StillPasses runs both real-bd tests of this
// file with GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE exported to ANOTHER
// repository that holds a beads workspace. Restoring any inheritance of those
// names makes the first `bd init` fail here, as it did under the commit hook.
func TestBackend_RealBD_ForeignGitEnv_StillPasses(t *testing.T) {
	foreign := foreignRepoWithBeads(t)
	for _, kv := range foreignGitEnv(foreign) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Run("RoundTrip", roundTripRealBD)
	t.Run("ReopenClearDefer", reopenClearDeferRealBD)
}

// TestBackend_RealBD_ForeignGitEnv_StopsABdThatInheritsIt is the negative
// control: the SAME environment, handed to a bd that inherits it (the old
// cleanBDEnv shape: os.Environ() minus three BEADS/WORKSPACE names), makes
// `bd init` in a fresh directory fail. If bd ever stops following GIT_DIR this
// test fails and says so, instead of the guard above passing for no reason.
func TestBackend_RealBD_ForeignGitEnv_StopsABdThatInheritsIt(t *testing.T) {
	foreign := foreignRepoWithBeads(t)
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd not on PATH")
	}
	leaky := append(
		hermeticBDEnv(os.Environ(), t.TempDir()),
		foreignGitEnv(foreign)...,
	)
	out, err := runRealBD(bdPath, t.TempDir(), leaky, "init", "--prefix", "leak", "--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	if err == nil {
		t.Fatalf("bd init succeeded under a foreign GIT_DIR/GIT_WORK_TREE: the hook environment no longer reaches bd, so the guard above proves nothing (revisit pg2-rxs4j)\n%s", out)
	}
	if !strings.Contains(out, "already initialized") {
		t.Fatalf("bd init failed under the foreign git environment, but not with the 'already initialized' refusal pg2-g3204 recorded:\n%s", out)
	}
}
