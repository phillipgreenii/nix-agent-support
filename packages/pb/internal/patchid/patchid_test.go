package patchid

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

// Equivalent mutants in this package, and why nothing below kills them.
//
// pg-go-mutate reports five surviving mutants in patchid.go. Every one is
// EQUIVALENT to the original — behaviourally indistinguishable over every
// REACHABLE input — so it is unkillable by construction rather than a missing
// assertion. Recorded so a later reader does not contrive an assertion for it:
//
//   - L23  len(fields) == 0                          -> <= 0   (firstField)
//   - L109 len(fields) == 0                          -> <= 0   (the line loop)
//     len is never negative, so == 0 and <= 0 agree on every value it can take.
//
//   - L43  id == ""                                  -> <= ""  (Compute)
//   - L98  strings.TrimSpace(logRes.Stdout) == ""    -> <= ""  (the scan)
//     Go orders strings lexicographically by byte and "" is the least element, so
//     s <= "" holds exactly when s == "".
//
//   - L116 len(fields) > 1                           -> != 1   (the line loop)
//     L109 continues when len(fields) == 0, so L116 is reached only with
//     len(fields) >= 1; over {1,2,3,...} the two agree. The neighbouring
//     > 1 -> >= 1 mutant is NOT equivalent and IS killed: it appends fields[1]
//     on a one-field line, which panics.
//
// Related run-health note: the two branch_condition mutants at L113 are reported
// NOT VIABLE, not survived. Replacing the condition of
// `if _, seen := byID[id]; !seen` with a constant leaves seen unused, which does
// not compile — so a nonzero notViable count on this package is expected.

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir) // keep git config writes inside temp (nix sandbox is read-only HOME)
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func commit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", file)
	runGit(t, dir, "commit", "-m", msg)
}

func TestComputeAndScan_findsCommitPatchID(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "hello\n", "add a")
	commit(t, dir, "b.txt", "world\n", "add b")
	c := Client{R: run.CLIRunner{}}
	id, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil || id == "" {
		t.Fatalf("Compute: id=%q err=%v", id, err)
	}
	set, err := c.ScanPatchIDs(context.Background(), dir, "-n 10 HEAD")
	if err != nil {
		t.Fatalf("ScanPatchIDs: %v", err)
	}
	if !set[id] {
		t.Errorf("scan did not find HEAD patch-id %q in %v", id, set)
	}
}

// TestScanPatchIDCommits_mapsPatchIDToItsCommit pins the fact the gate's lock
// condition rests on: `git patch-id` prints the COMMIT sha beside each patch-id
// when fed from `git log -p`, so the gated commit is recoverable from the scan the
// gate check already runs — no extra git call and no new gate metadata. Asserted
// against real git, because it is git's output shape being relied upon.
func TestScanPatchIDCommits_mapsPatchIDToItsCommit(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "hello\n", "add a")
	base := strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))
	commit(t, dir, "b.txt", "world\n", "add b")
	head := strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))

	c := Client{R: run.CLIRunner{}}
	id, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	byID, err := c.ScanPatchIDCommits(context.Background(), dir, base+"..HEAD")
	if err != nil {
		t.Fatalf("ScanPatchIDCommits: %v", err)
	}
	shas, ok := byID[id]
	if !ok {
		t.Fatalf("scan did not find HEAD patch-id %q in %v", id, byID)
	}
	if len(shas) != 1 || shas[0] != head {
		t.Errorf("patch-id %q maps to %v, want exactly [%s]", id, shas, head)
	}
	// The key set must remain exactly what ScanPatchIDs reports.
	set, err := c.ScanPatchIDs(context.Background(), dir, base+"..HEAD")
	if err != nil {
		t.Fatalf("ScanPatchIDs: %v", err)
	}
	if len(set) != len(byID) || !set[id] {
		t.Errorf("ScanPatchIDs set %v must be the key set of %v", set, byID)
	}
}

func TestScan_emptyRangeYieldsEmptySet(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "hello\n", "c1")
	c := Client{R: run.CLIRunner{}}
	// HEAD..HEAD is an empty range.
	set, err := c.ScanPatchIDs(context.Background(), dir, "HEAD..HEAD")
	if err != nil {
		t.Fatalf("ScanPatchIDs: %v", err)
	}
	if len(set) != 0 {
		t.Errorf("empty range set = %v, want empty", set)
	}
}

func TestComputeStableAcrossRebase(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "base.txt", "base\n", "base")
	commit(t, dir, "feat.txt", "feature\n", "feat")
	c := Client{R: run.CLIRunner{}}
	before, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("Compute before: %v", err)
	}
	// Rewrite history: amend the base commit's message (changes SHAs of both).
	runGit(t, dir, "rebase", "--exec", "true", "--root") // no-op exec forces re-application
	after, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("Compute after: %v", err)
	}
	if before != after {
		t.Errorf("patch-id changed across rebase: before=%q after=%q", before, after)
	}
}

func TestIsAncestor(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "1\n", "c1")
	c := Client{R: run.CLIRunner{}}
	first := strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))
	commit(t, dir, "a.txt", "2\n", "c2")
	if !c.IsAncestor(context.Background(), dir, first, "HEAD") {
		t.Error("first commit should be ancestor of HEAD")
	}
	if c.IsAncestor(context.Background(), dir, "HEAD", first) {
		t.Error("HEAD should not be ancestor of first commit")
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// The tests below drive the Client through run.FakeRunner instead of real git. The
// real-git tests above pin what GIT does (patch-id stability, the "<id> <sha>"
// output shape); these pin what THIS PACKAGE does with git's answers — including the
// answers real git is hard to provoke into giving (a failing invocation, an empty
// or sha-less patch-id line). Both are needed: a fake cannot prove git's contract,
// and real git cannot cheaply prove the failure paths.

const fakeRepo = "/repo"

// fakeClient returns a Client over a scripted runner. An unscripted call makes the
// FakeRunner return an error, which is how the failure paths below are provoked.
func fakeClient() (Client, *run.FakeRunner) {
	f := run.NewFakeRunner()
	return Client{R: f}, f
}

func scriptShow(f *run.FakeRunner, commitish, stdout string) {
	f.AddResponse("git", []string{"-C", fakeRepo, "show", commitish}, run.Result{Stdout: stdout}, nil)
}

func scriptPatchID(f *run.FakeRunner, stdout string) {
	f.AddResponse("git", []string{"-C", fakeRepo, "patch-id", "--stable"}, run.Result{Stdout: stdout}, nil)
}

func scriptLog(f *run.FakeRunner, rangeArgs []string, stdout string) {
	args := append([]string{"-C", fakeRepo, "log", "-p", "--no-merges"}, rangeArgs...)
	f.AddResponse("git", args, run.Result{Stdout: stdout}, nil)
}

// ranPatchID reports whether the client piped anything into `git patch-id`.
func ranPatchID(f *run.FakeRunner) bool {
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) == 4 && c.Args[2] == "patch-id" {
			return true
		}
	}
	return false
}

// TestCompute_readsTheFirstFieldOfTheFirstLine pins how Compute extracts the id
// from `git patch-id` output. The patch-id is the whole identity the gate keys on —
// pb's decision to release a follow-up bead is "this id appeared in the applied
// range" — so mis-slicing the line silently changes which change a gate is about.
func TestCompute_readsTheFirstFieldOfTheFirstLine(t *testing.T) {
	tests := []struct {
		name, patchIDStdout, want string
	}{
		{"id and sha", "abc123 deadsha\n", "abc123"},
		{"id alone", "abc123\n", "abc123"},
		{"leading and trailing whitespace", "  abc123 deadsha  \n", "abc123"},
		{"only the FIRST line is the commit's own id", "abc123 deadsha\nfff444 othersha\n", "abc123"},
		{"no trailing newline", "abc123 deadsha", "abc123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, f := fakeClient()
			scriptShow(f, "HEAD", "diff --git a/a b/a\n")
			scriptPatchID(f, tc.patchIDStdout)
			id, err := c.Compute(context.Background(), fakeRepo, "HEAD")
			if err != nil {
				t.Fatalf("Compute: %v", err)
			}
			if id != tc.want {
				t.Errorf("id = %q, want %q", id, tc.want)
			}
		})
	}
}

// TestCompute_reportsWhichGitCallFailed covers the two failure wraps in Compute.
// `pb gate create` turns this error into a refusal to create the gate, so the
// operator's next move depends on knowing WHICH call failed — a bad commitish (git
// show) is a different fix from git patch-id being unavailable.
//
// The substrings are chosen to be discriminating: asserting merely "an error" would
// pass with either wrap deleted, since the very next unscripted call errors too.
// "git patch-id: " (with the colon) is likewise distinct from the no-id message
// below, which also begins "git patch-id".
func TestCompute_reportsWhichGitCallFailed(t *testing.T) {
	tests := []struct {
		name    string
		script  func(*run.FakeRunner)
		wantSub string
	}{
		{
			name:    "git show fails (unknown commitish)",
			script:  func(*run.FakeRunner) {}, // nothing scripted → git show errors
			wantSub: "git show HEAD:",
		},
		{
			name:    "git patch-id fails",
			script:  func(f *run.FakeRunner) { scriptShow(f, "HEAD", "diff\n") },
			wantSub: "git patch-id: ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, f := fakeClient()
			tc.script(f)
			id, err := c.Compute(context.Background(), fakeRepo, "HEAD")
			if err == nil {
				t.Fatalf("Compute returned id %q and a nil error; a gate must never be created from an "+
					"unproven patch-id", id)
			}
			if id != "" {
				t.Errorf("id = %q on the error path, want empty", id)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want it to name the failing call (%q)", err, tc.wantSub)
			}
		})
	}
}

// TestCompute_emptyPatchIDOutputIsAnError covers the case where git exits 0 having
// said nothing — the shape a `git show` of an empty commit produces. An empty id
// must NOT be returned as an id: the gate's await id would become "wsid:repo:" and
// then match on the empty patch-id of any other such commit.
//
// It is also the only route to firstField's empty-fields guard; without the guard
// the indexing panics instead of returning this error.
func TestCompute_emptyPatchIDOutputIsAnError(t *testing.T) {
	for _, stdout := range []string{"", "\n", "   \n\t\n"} {
		t.Run(fmt.Sprintf("stdout=%q", stdout), func(t *testing.T) {
			c, f := fakeClient()
			scriptShow(f, "HEAD", "")
			scriptPatchID(f, stdout)
			id, err := c.Compute(context.Background(), fakeRepo, "HEAD")
			if err == nil || id != "" {
				t.Fatalf("Compute = (%q, %v); git saying nothing is not an id", id, err)
			}
			if !strings.Contains(err.Error(), "produced no id") {
				t.Errorf("error = %q, want it to say no id was produced", err)
			}
		})
	}
}

// TestScanPatchIDCommits_parsesPatchIDOutput pins the scan's parse, which is the
// identity half of gate resolution: the key set answers "did this change land?" and
// the slices answer "in which commit?", which the lock condition then tests for
// ancestry.
//
// The sha-less rows are the documented behaviour of ScanPatchIDCommits — a patch
// whose sha could not be read must "register as found rather than silently
// vanishing from the scan", because the key set is exactly ScanPatchIDs' answer and
// dropping a key would turn a landed change into a never-resolving gate.
func TestScanPatchIDCommits_parsesPatchIDOutput(t *testing.T) {
	tests := []struct {
		name          string
		patchIDStdout string
		want          map[string][]string
	}{
		{"one patch, one commit", "abc123 deadsha\n", map[string][]string{"abc123": {"deadsha"}}},
		{
			// A cherry-pick: diff-identical copies differing in ancestry.
			name:          "one patch, two commits",
			patchIDStdout: "abc123 shipped\nabc123 localonly\n",
			want:          map[string][]string{"abc123": {"shipped", "localonly"}},
		},
		{
			name:          "two patches",
			patchIDStdout: "abc123 sha1\nfff444 sha2\n",
			want:          map[string][]string{"abc123": {"sha1"}, "fff444": {"sha2"}},
		},
		{
			// The key MUST still be present: found, with no commit to test ancestry on.
			name:          "a patch with no sha still registers",
			patchIDStdout: "abc123\n",
			want:          map[string][]string{"abc123": nil},
		},
		{
			name:          "a sha-less line does not suppress a later sha for the same patch",
			patchIDStdout: "abc123\nabc123 late\n",
			want:          map[string][]string{"abc123": {"late"}},
		},
		{
			name:          "a sha-less patch does not shadow a sha-bearing one",
			patchIDStdout: "abc123\nfff444 sha2\n",
			want:          map[string][]string{"abc123": nil, "fff444": {"sha2"}},
		},
		{
			name:          "blank lines are skipped, not keyed as an empty patch-id",
			patchIDStdout: "\n\nabc123 deadsha\n\n   \n",
			want:          map[string][]string{"abc123": {"deadsha"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, f := fakeClient()
			scriptLog(f, []string{"base..tip"}, "diff --git a/a b/a\n")
			scriptPatchID(f, tc.patchIDStdout)
			byID, err := c.ScanPatchIDCommits(context.Background(), fakeRepo, "base..tip")
			if err != nil {
				t.Fatalf("ScanPatchIDCommits: %v", err)
			}
			if len(byID) != len(tc.want) {
				t.Fatalf("byID = %v, want %v", byID, tc.want)
			}
			for id, wantShas := range tc.want {
				gotShas, ok := byID[id]
				if !ok {
					t.Fatalf("patch-id %q missing from %v; a found patch must never vanish from the scan",
						id, byID)
				}
				if len(gotShas) != len(wantShas) {
					t.Fatalf("byID[%q] = %v, want %v", id, gotShas, wantShas)
				}
				for i := range wantShas {
					if gotShas[i] != wantShas[i] {
						t.Fatalf("byID[%q] = %v, want %v", id, gotShas, wantShas)
					}
				}
			}
		})
	}
}

// TestScanPatchIDs_isTheKeySetOfScanPatchIDCommits pins the documented relationship
// between the two scans, on the case that can break it: a patch with no sha. Its
// slice is empty, so any "drop the empty ones" simplification would silently shrink
// the key set — and the key set is what condition 1 of gate resolution tests.
func TestScanPatchIDs_isTheKeySetOfScanPatchIDCommits(t *testing.T) {
	c, f := fakeClient()
	scriptLog(f, []string{"base..tip"}, "diff\n")
	scriptPatchID(f, "abc123\nfff444 sha2\n")
	set, err := c.ScanPatchIDs(context.Background(), fakeRepo, "base..tip")
	if err != nil {
		t.Fatalf("ScanPatchIDs: %v", err)
	}
	if len(set) != 2 || !set["abc123"] || !set["fff444"] {
		t.Fatalf("set = %v; the key set must hold every patch found, sha or no sha", set)
	}
}

// TestScanPatchIDCommits_splitsTheRevRangeIntoGitArgs pins the range plumbing. The
// gate check passes either "base..tip" or "-n 100 tip"; the latter is only a valid
// git invocation if it is split into separate argv entries, and a single glued arg
// makes git scan nothing while still exiting 0 — a silent false miss.
func TestScanPatchIDCommits_splitsTheRevRangeIntoGitArgs(t *testing.T) {
	c, f := fakeClient()
	scriptLog(f, []string{"-n", "100", "tip"}, "diff\n")
	scriptPatchID(f, "abc123 deadsha\n")
	if _, err := c.ScanPatchIDCommits(context.Background(), fakeRepo, "-n 100 tip"); err != nil {
		t.Fatalf("ScanPatchIDCommits: %v (the range must be split into separate git args)", err)
	}
}

// TestScanPatchIDCommits_emptyRangeIsAnEmptyMapNotAnError pins the documented
// distinction the gate check depends on: an EMPTY RANGE is an answer ("this change
// is not in the applied range" → stay gated), whereas an ERROR is the absence of
// one (→ Skipped, which drives `pb gate check`'s non-zero exit and pn's warning).
// Collapsing them either way misroutes the gate.
//
// Asserting that `git patch-id` is never invoked pins the SHORT-CIRCUIT rather than
// a lucky verdict: real `git patch-id` on empty stdin also prints nothing, so
// without this the behaviour would look identical while spawning a pointless
// process on every unresolved gate.
func TestScanPatchIDCommits_emptyRangeIsAnEmptyMapNotAnError(t *testing.T) {
	for _, stdout := range []string{"", "\n", "  \n\t"} {
		t.Run(fmt.Sprintf("stdout=%q", stdout), func(t *testing.T) {
			c, f := fakeClient()
			scriptLog(f, []string{"tip..tip"}, stdout)
			byID, err := c.ScanPatchIDCommits(context.Background(), fakeRepo, "tip..tip")
			if err != nil {
				t.Fatalf("ScanPatchIDCommits: %v; an empty range is a determinate answer, not a failure", err)
			}
			if byID == nil || len(byID) != 0 {
				t.Fatalf("byID = %v, want an empty (non-nil) map", byID)
			}
			if ranPatchID(f) {
				t.Error("git patch-id was invoked on an empty log; the empty range must short-circuit")
			}
		})
	}
}

// TestScanPatchIDCommits_reportsWhichGitCallFailed covers the two failure wraps in
// the scan. A swallowed failure here is the expensive direction: an empty map means
// "the change is not in the applied range", so a scan that failed but reported
// success turns into an indefinitely blocked gate with no reason recorded — or,
// with the range mis-scanned, an unresolvable one.
func TestScanPatchIDCommits_reportsWhichGitCallFailed(t *testing.T) {
	tests := []struct {
		name    string
		script  func(*run.FakeRunner)
		wantSub string
	}{
		{
			name:    "git log -p fails",
			script:  func(*run.FakeRunner) {}, // nothing scripted → git log errors
			wantSub: "git log -p base..tip:",
		},
		{
			name:    "git patch-id fails",
			script:  func(f *run.FakeRunner) { scriptLog(f, []string{"base..tip"}, "diff\n") },
			wantSub: "git patch-id (scan): ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, f := fakeClient()
			tc.script(f)
			byID, err := c.ScanPatchIDCommits(context.Background(), fakeRepo, "base..tip")
			if err == nil {
				t.Fatalf("ScanPatchIDCommits returned %v and a nil error; a failed scan must not read as "+
					"'the change is not there'", byID)
			}
			if byID != nil {
				t.Errorf("byID = %v on the error path, want nil", byID)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want it to name the failing call (%q)", err, tc.wantSub)
			}
		})
	}
}

// TestScanPatchIDs_propagatesScanFailure is the same soundness point one layer up.
// ScanPatchIDs is the thin key-set wrapper, so a dropped error there is invisible at
// the call site: callers get an empty set and cannot tell "not found" from
// "never looked".
func TestScanPatchIDs_propagatesScanFailure(t *testing.T) {
	c, _ := fakeClient() // nothing scripted → git log errors
	set, err := c.ScanPatchIDs(context.Background(), fakeRepo, "base..tip")
	if err == nil {
		t.Fatalf("ScanPatchIDs returned %v and a nil error; an empty set means 'not landed', which is a "+
			"claim a failed scan cannot make", set)
	}
	if set != nil {
		t.Errorf("set = %v on the error path, want nil", set)
	}
	if !strings.Contains(err.Error(), "git log -p") {
		t.Errorf("error = %q, want the underlying failure preserved", err)
	}
}

// TestIsAncestor_reportsGitsVerdictFromTheExitStatus pins IsAncestor's mapping from
// `git merge-base --is-ancestor` (which answers by exit status, printing nothing)
// to a bool. It is the primitive BOTH gate conditions are decided by, and it is a
// bool with no error channel, so an inverted mapping cannot surface any other way.
func TestIsAncestor_reportsGitsVerdictFromTheExitStatus(t *testing.T) {
	t.Run("exit 0 means yes", func(t *testing.T) {
		c, f := fakeClient()
		f.AddResponse("git", []string{"-C", fakeRepo, "merge-base", "--is-ancestor", "a", "b"},
			run.Result{}, nil)
		if !c.IsAncestor(context.Background(), fakeRepo, "a", "b") {
			t.Error("exit 0 must be reported as 'is an ancestor'")
		}
	})
	t.Run("non-zero means no", func(t *testing.T) {
		c, f := fakeClient()
		f.AddResponse("git", []string{"-C", fakeRepo, "merge-base", "--is-ancestor", "a", "b"},
			run.Result{ExitCode: 1}, fmt.Errorf("not an ancestor"))
		if c.IsAncestor(context.Background(), fakeRepo, "a", "b") {
			t.Error("a non-zero exit must be reported as 'is NOT an ancestor'")
		}
	})
	t.Run("an unknown rev is reported as no, never as yes", func(t *testing.T) {
		c, _ := fakeClient() // unscripted → the runner errors, as git does on a bad rev
		if c.IsAncestor(context.Background(), fakeRepo, "nope", "b") {
			t.Error("a failed probe must never be read as 'the commit is in there'")
		}
	})
}
