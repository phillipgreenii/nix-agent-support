//go:build contract

// e2e_contract_test.go: pg-connector's real end-to-end/contract suite
// (bead pg2-qp50z). Every other test in this module's Go suite drives a
// hand-mocked fake Runner/gh client with hand-typed JSON fixtures — the
// exact shape that let the sibling ListReviews id int64-vs-string bug
// escape (real gh returns "id":"PRR_kwDO..." as a string; the fixture typed
// "id":12345). This file instead builds the REAL five binaries this module
// produces (pg-connector plus its four Tier-2 backends) via `go build`,
// execs the REAL pg-connector binary as a genuinely separate OS process
// (mirroring packages/pr-pool/cmd/pr-pool/e2e_test.go's own
// build-the-real-binary-and-exec-it convention), and lets IT exec the real
// backend binaries in turn exactly as it does in production — never
// calling any backend's internal.Backend/Provider Go type directly.
//
// Behind the `contract` build tag on purpose, the same tag
// packages/pb/pkg/beads, packages/pg-pr/pkg/beads and this module's own
// cmd/pg-connector-issue-beads/internal/realbd_test.go already use for a
// real-external-system suite that must stay out of the default
// `go test ./...` run (and therefore out of checks.pg-connector-go-tests
// and the pre-commit run-unit-tests hook) — invoked instead via
// `nix run .#pg-connector-contract` (flake.nix), a deliberate, manual,
// on-demand action, never part of `nix flake check`/CI.
//
// Credential/tool availability, per case:
//   - bd (issue-beads) and git (scm-git) are expected to already be on this
//     machine's PATH in the ordinary case; a case that needs one skips
//     gracefully (t.Skip, never a failure) when the tool is genuinely
//     absent, mirroring realbd_test.go's own convention for `bd`.
//   - gh (pr-github, ci-github-actions) is the same: absent from PATH skips
//     gracefully. But unlike a missing TOOL, a missing real TARGET is a
//     configuration mistake, not an environment limitation: per the
//     2026-09-07 operator ruling on this bead (bd comment,
//     fcb71be0-848b-467d-851b-5f61429ce92e-unblock), this suite has no
//     fixed PR/repo — the caller supplies one they have real access to via
//     $PG_CONNECTOR_E2E_REPO ("owner/repo") and $PG_CONNECTOR_E2E_PR (a
//     plain PR number), with NO default and NO real value ever committed
//     here (docs/tests/fixtures alike; see requireGHRepoAndPR below and
//     this repo's own CLAUDE.md disclosure rule). When gh IS on PATH but
//     either var is unset, this errors loudly (t.Fatal) rather than
//     skipping quietly — an unset required var is this suite's own
//     configuration failure, not an unavailable environment.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// contractBackendBinaries is every binary this suite builds and runs for
// real: the Tier-1 umbrella plus all four Tier-2 backends this bead's scope
// names (pr-github, ci-github-actions, issue-beads, scm-git).
var contractBackendBinaries = []string{
	"pg-connector",
	"pg-connector-pr-github",
	"pg-connector-ci-github-actions",
	"pg-connector-issue-beads",
	"pg-connector-scm-git",
}

// contractBinDir holds the directory TestMain builds every real binary
// into, shared read-only across every test in this file — built exactly
// once per `go test` process, since a full `go build` per binary per test
// would multiply this suite's already-real-process cost for no benefit
// (nothing here mutates the built binaries themselves).
var contractBinDir string

// TestMain builds the real binaries once, before any Test* in this file
// runs, and cleans up the temp build directory afterward. This is the one
// TestMain in this package's test binary — only ever compiled in under
// -tags contract, so it never collides with a plain `go test ./...` run,
// which never sees this file at all.
func TestMain(m *testing.M) {
	dir, err := buildContractBinaries()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pg-connector contract suite: build real binaries:", err)
		os.Exit(1)
	}
	contractBinDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// buildContractBinaries compiles every binary in contractBackendBinaries
// via `go build -o <dir>/<name> ./cmd/<name>`, run with the MODULE ROOT
// (two directories up from this file, packages/pg-connector) as the
// build's working directory — the actual artifacts operators run, not a
// package-level fake, mirroring packages/pr-pool/cmd/pr-pool/e2e_test.go's
// own buildPrPoolBinary.
func buildContractBinaries() (string, error) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		return "", fmt.Errorf("resolve module root: %w", err)
	}
	dir, err := os.MkdirTemp("", "pg-connector-contract-bin-")
	if err != nil {
		return "", fmt.Errorf("mkdir temp build dir: %w", err)
	}
	for _, name := range contractBackendBinaries {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(dir, name), "./cmd/"+name)
		cmd.Dir = moduleRoot
		out, buildErr := cmd.CombinedOutput()
		cancel()
		if buildErr != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("go build ./cmd/%s: %w\n%s", name, buildErr, out)
		}
	}
	return dir, nil
}

// contractRegistryYAML is the connector.<type> registry every case in this
// file registers all four real backends under, by their bare binary names
// (registry.go's own PATH-lookup convention) — issue/ci/pr are list-valued,
// scm is single-valued (scm.go's own doc comment: "connector.scm is a
// SINGLE-VALUED registry entry").
const contractRegistryYAML = `connector:
  pr:
    - pg-connector-pr-github
  ci:
    - pg-connector-ci-github-actions
  issue:
    - pg-connector-issue-beads
  scm: pg-connector-scm-git
`

// writeContractRegistry writes contractRegistryYAML to a fresh temp file
// and returns its path, for setting $PG_PR_CONFIG (registry.go's own
// resolution order checks that env var first).
func writeContractRegistry(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contractRegistryYAML), 0o644); err != nil {
		t.Fatalf("write registry config: %v", err)
	}
	return path
}

// contractEnvKeysToStrip are env vars that must never leak from this test
// process's own ambient environment into a real pg-connector subprocess:
// bd's own workspace-selection vars (mirroring
// cmd/pg-connector-issue-beads/internal/realbd_test.go's cleanBDEnv, so a
// disposable per-case workspace can never accidentally bind to this
// machine's real beads workspace or shared dolt server) plus the three
// vars every case below sets explicitly itself, so a stale value from the
// real ambient environment can never masquerade as this case's own
// deliberate override.
var contractEnvKeysToStrip = map[string]bool{
	"BEADS_DIR":                         true,
	"WORKSPACE_ROOT":                    true,
	"ZR_MACHINE_SUPPORT_WORKSPACE_ROOT": true,
	"PG_PR_CONFIG":                      true,
	"PG_CONNECTOR_ISSUE_BEADS_DIR":      true,
	"XDG_STATE_HOME":                    true,
}

// contractEnv builds the full env slice a real pg-connector subprocess
// runs under: this test process's own ambient environment, minus
// contractEnvKeysToStrip, with contractBinDir prepended to PATH (so the
// registry's bare binary names resolve to THESE freshly built binaries,
// never a stale installed copy elsewhere on PATH) and overrides applied
// last.
func contractEnv(overrides map[string]string) []string {
	merged := make(map[string]string)
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		k := kv[:i]
		if contractEnvKeysToStrip[k] {
			continue
		}
		merged[k] = kv[i+1:]
	}
	merged["PATH"] = contractBinDir + string(os.PathListSeparator) + merged["PATH"]
	for k, v := range overrides {
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}

// contractResult is one real pg-connector invocation's captured outcome.
type contractResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

// runPGConnector execs the REAL, freshly built pg-connector binary
// (contractBinDir) with args, cwd (empty means inherit this test process's
// own cwd) and env (see contractEnv), and captures its outcome. It never
// treats a non-zero exit as a Go test failure itself — pg-connector's own
// targeted/fan-out exit codes (outcome.go) are meaningful data every case
// below asserts on explicitly, not a Go-level error.
func runPGConnector(t *testing.T, cwd string, env []string, args ...string) contractResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(contractBinDir, "pg-connector"), args...)
	cmd.Dir = cwd
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec real pg-connector %v: %v\nstderr=%s", args, err, stderr.String())
		}
	}
	return contractResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

// decodeContractJSON decodes raw into a T, failing the test with the raw
// bytes on any parse error — every case below invokes this on a real
// subprocess's real stdout, never a fixture.
func decodeContractJSON[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode JSON: %v\nraw=%s", err, raw)
	}
	return v
}

// requireTool skips the calling test (never fails it) when name is not on
// this machine's real PATH — the "tool genuinely unavailable in this
// environment" case, mirroring realbd_test.go's identical `bd`-absent
// skip.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH; skipping real-%s contract case", name, name)
	}
}

// newDisposableBDWorkspace bd-inits a fresh, disposable workspace directory
// for the issue-beads cases, mirroring realbd_test.go's own
// TestBackend_RoundTrip_RealBD setup (a time-based prefix, since a fixed
// prefix can collide with a leftover database on this machine's shared
// dolt server across separate contract runs — see that file's own doc
// comment for the full mechanism). Caller must already have called
// requireTool(t, "bd").
func newDisposableBDWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prefix := "tc" + fmt.Sprintf("%x", time.Now().UnixNano())[:10]
	env := contractEnv(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bd", "init", "--prefix", prefix,
		"--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}
	return dir
}

// newDisposableGitRepo creates a fresh, disposable one-commit git repo with
// one extra unchecked-out branch ("feature/contract-probe") for the
// scm-git cases to exercise `worktree add` against. Caller must already
// have called requireTool(t, "git").
func newDisposableGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "pg-connector-contract-suite@example.invalid")
	run("config", "user.name", "pg-connector contract suite")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("pg-connector contract suite fixture\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial commit")
	run("branch", "feature/contract-probe")
	return dir
}

// requireGHRepoAndPR is this suite's env-var gate for every real-GitHub
// case (pr-github, ci-github-actions): see this file's own header comment
// for the full rationale. It reports the operator-chosen repo ("owner/repo")
// and PR number to target.
func requireGHRepoAndPR(t *testing.T) (repo string, prNumber int) {
	t.Helper()
	requireTool(t, "gh")
	repo = strings.TrimSpace(os.Getenv("PG_CONNECTOR_E2E_REPO"))
	prRaw := strings.TrimSpace(os.Getenv("PG_CONNECTOR_E2E_PR"))
	if repo == "" || prRaw == "" {
		t.Fatalf("pg-connector contract suite: gh is on PATH but $PG_CONNECTOR_E2E_REPO and/or " +
			"$PG_CONNECTOR_E2E_PR is unset. This suite has no fixed PR/repo of its own (2026-09-07 " +
			"operator ruling on bead pg2-qp50z) — set PG_CONNECTOR_E2E_REPO=owner/repo and " +
			"PG_CONNECTOR_E2E_PR=<number> to a real PR you have real access to before running the " +
			"real-GitHub contract cases; never hardcode or commit a real value here.")
	}
	n, err := strconv.Atoi(prRaw)
	if err != nil || n <= 0 {
		t.Fatalf("pg-connector contract suite: $PG_CONNECTOR_E2E_PR=%q must be a positive integer PR number", prRaw)
	}
	return repo, n
}

// ---------------------------------------------------------------------
// issue (pg-connector-issue-beads): show, create, comment, transition —
// every op this backend's own capabilities response declares
// (cmd/pg-connector-issue-beads/main.go's newDispatchTable).
// ---------------------------------------------------------------------

func TestContract_IssueBeads_RealBD_RoundTrip(t *testing.T) {
	requireTool(t, "bd")
	bdDir := newDisposableBDWorkspace(t)
	env := contractEnv(map[string]string{
		"PG_PR_CONFIG":                 writeContractRegistry(t),
		"PG_CONNECTOR_ISSUE_BEADS_DIR": bdDir,
	})

	created := runPGConnector(t, "", env, "issue", "create",
		"--title", "contract suite probe",
		"--priority", "P1",
		"--issue-type", "task",
		"--labels", "probe",
		"--description", "a round-trip description")
	if created.exitCode != 0 {
		t.Fatalf("issue create: exit=%d stdout=%s stderr=%s", created.exitCode, created.stdout, created.stderr)
	}
	createdResp := decodeContractJSON[scriptout.Response](t, created.stdout)
	if createdResp.Error != nil {
		t.Fatalf("issue create: error envelope: %+v", createdResp.Error)
	}
	var issue schema.Issue
	if err := scriptout.Decode(createdResp.Result, &issue); err != nil {
		t.Fatalf("decode created issue: %v", err)
	}
	if issue.ID == "" {
		t.Fatal("issue create: empty id")
	}
	if issue.State != "open" {
		t.Fatalf("issue create: state = %q, want open", issue.State)
	}
	if issue.Description != "a round-trip description" {
		t.Fatalf("issue create: description = %q", issue.Description)
	}

	commented := runPGConnector(t, "", env, "issue", "comment", issue.ID, "--body", "commenting from the real e2e/contract suite")
	if commented.exitCode != 0 {
		t.Fatalf("issue comment: exit=%d stderr=%s", commented.exitCode, commented.stderr)
	}

	transitioned := runPGConnector(t, "", env, "issue", "transition", issue.ID, "--state", "in_progress")
	if transitioned.exitCode != 0 {
		t.Fatalf("issue transition: exit=%d stderr=%s", transitioned.exitCode, transitioned.stderr)
	}

	shown := runPGConnector(t, "", env, "issue", "show", issue.ID)
	if shown.exitCode != 0 {
		t.Fatalf("issue show: exit=%d stderr=%s", shown.exitCode, shown.stderr)
	}
	shownResp := decodeContractJSON[scriptout.Response](t, shown.stdout)
	var shownIssue schema.Issue
	if err := scriptout.Decode(shownResp.Result, &shownIssue); err != nil {
		t.Fatalf("decode shown issue: %v", err)
	}
	if shownIssue.State != "in_progress" {
		t.Fatalf("issue show after transition: state = %q, want in_progress", shownIssue.State)
	}
	if shownIssue.ID != issue.ID || shownIssue.Title != issue.Title {
		t.Fatalf("issue show identity mismatch: got %+v, want id/title matching %+v", shownIssue, issue)
	}

	// A genuinely unknown id must round-trip as pg-connector's own
	// not_found exit code (4, outcome.go's TargetedExitCode) against the
	// REAL bd binary, not a fake.
	missing := runPGConnector(t, "", env, "issue", "show", issue.ID+"-doesnotexist")
	if missing.exitCode != 4 {
		t.Fatalf("issue show(unknown id): exit=%d, want 4 (not_found); stdout=%s", missing.exitCode, missing.stdout)
	}
}

// ---------------------------------------------------------------------
// scm (pg-connector-scm-git): worktree add/remove/list, branch detect —
// every op this backend's own capabilities response declares
// (cmd/pg-connector-scm-git/main.go's newDispatchTable).
// ---------------------------------------------------------------------

func TestContract_ScmGit_RealGit_WorktreeAndBranch(t *testing.T) {
	requireTool(t, "git")
	repoDir := newDisposableGitRepo(t)
	env := contractEnv(map[string]string{
		"PG_PR_CONFIG": writeContractRegistry(t),
	})

	detected := runPGConnector(t, repoDir, env, "scm", "branch", "detect")
	if detected.exitCode != 0 {
		t.Fatalf("scm branch detect: exit=%d stderr=%s", detected.exitCode, detected.stderr)
	}
	detectedResp := decodeContractJSON[scriptout.Response](t, detected.stdout)
	var branchInfo schema.BranchInfo
	if err := scriptout.Decode(detectedResp.Result, &branchInfo); err != nil {
		t.Fatalf("decode branch info: %v", err)
	}
	if branchInfo.Repo != filepath.Base(repoDir) {
		t.Fatalf("scm branch detect: repo = %q, want %q", branchInfo.Repo, filepath.Base(repoDir))
	}
	if branchInfo.Branch != "main" {
		t.Fatalf("scm branch detect: branch = %q, want main", branchInfo.Branch)
	}

	added := runPGConnector(t, repoDir, env, "scm", "worktree", "add", "feature/contract-probe")
	if added.exitCode != 0 {
		t.Fatalf("scm worktree add: exit=%d stderr=%s stdout=%s", added.exitCode, added.stderr, added.stdout)
	}
	addedResp := decodeContractJSON[scriptout.Response](t, added.stdout)
	var wt schema.WorktreeInfo
	if err := scriptout.Decode(addedResp.Result, &wt); err != nil {
		t.Fatalf("decode worktree info: %v", err)
	}
	if wt.Branch != "feature/contract-probe" || wt.Ref != "feature/contract-probe" {
		t.Fatalf("scm worktree add: got %+v, want branch/ref = feature/contract-probe", wt)
	}
	if !strings.Contains(wt.Path, filepath.Join(".worktrees", "pg-connector-scm-git")) {
		t.Fatalf("scm worktree add: path = %q, want it under .worktrees/pg-connector-scm-git", wt.Path)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("scm worktree add: reported path does not exist on disk: %v", err)
	}

	listed := runPGConnector(t, repoDir, env, "scm", "worktree", "list")
	if listed.exitCode != 0 {
		t.Fatalf("scm worktree list: exit=%d stderr=%s", listed.exitCode, listed.stderr)
	}
	listedResp := decodeContractJSON[scriptout.Response](t, listed.stdout)
	var worktrees []schema.WorktreeInfo
	if err := scriptout.Decode(listedResp.Result, &worktrees); err != nil {
		t.Fatalf("decode worktree list: %v", err)
	}
	found := false
	for _, w := range worktrees {
		if w.Path == wt.Path {
			found = true
		}
	}
	if !found {
		t.Fatalf("scm worktree list: %+v does not contain the just-added worktree %q", worktrees, wt.Path)
	}

	removed := runPGConnector(t, repoDir, env, "scm", "worktree", "remove", wt.Path)
	if removed.exitCode != 0 {
		t.Fatalf("scm worktree remove: exit=%d stderr=%s stdout=%s", removed.exitCode, removed.stderr, removed.stdout)
	}
	if _, err := os.Stat(wt.Path); err == nil {
		t.Fatalf("scm worktree remove: %q still exists on disk", wt.Path)
	}

	// A path git does not know about at all rounds-trips as not_found
	// (exit 4) against the REAL git binary, not a fake.
	removedAgain := runPGConnector(t, repoDir, env, "scm", "worktree", "remove", wt.Path)
	if removedAgain.exitCode != 4 {
		t.Fatalf("scm worktree remove(already removed): exit=%d, want 4 (not_found); stdout=%s", removedAgain.exitCode, removedAgain.stdout)
	}
}

// ---------------------------------------------------------------------
// Tier-1-only fan-outs: config validate / auth status across all FOUR
// real, registered backends at once.
// ---------------------------------------------------------------------

// ghRealModeConfigured reports whether the operator has pointed this run at
// a real GitHub PR (both $PG_CONNECTOR_E2E_REPO and $PG_CONNECTOR_E2E_PR
// set — the same gate requireGHRepoAndPR enforces for the dedicated
// real-GH cases below) and gh is on PATH. Used only to decide how strictly
// THIS file's fan-out assertions can pin the pr-github/ci-github-actions
// rows (see the two functions below): with real GH access configured for
// this run, those two backends' auth_status is expected to succeed and a
// degraded row would be a genuine defect; without it, gh may be on PATH
// but not usefully configured for this particular run, so a degraded row
// there is tolerated rather than asserted against.
//
// Deliberately does NOT itself exec `gh` (e.g. `gh auth status`) to probe
// this live: exec.LookPath only searches PATH, it never spawns the
// binary, and TestGHExecChokePoint (chokepoint_test.go) statically scans
// this whole module — regardless of build tags — for any direct
// `exec.Command(Context)?(...,"gh"...)` call outside the two allowlisted
// ghexec.go/token.go pairs. This helper must not become a second, unlisted
// such call site.
func ghRealModeConfigured() bool {
	if _, err := exec.LookPath("gh"); err != nil {
		return false
	}
	return strings.TrimSpace(os.Getenv("PG_CONNECTOR_E2E_REPO")) != "" &&
		strings.TrimSpace(os.Getenv("PG_CONNECTOR_E2E_PR")) != ""
}

func TestContract_ConfigValidate_AllFourRealBackends(t *testing.T) {
	env := contractEnv(map[string]string{
		"PG_PR_CONFIG": writeContractRegistry(t),
	})

	res := runPGConnector(t, "", env, "config", "validate")
	outcome := decodeContractJSON[FanOutOutcome](t, res.stdout)
	assertFourBackendSources(t, outcome.Sources)

	wantExit := outcome.ExitCode()
	if res.exitCode != wantExit {
		t.Fatalf("config validate: exit=%d, want ExitCode()=%d for sources %+v", res.exitCode, wantExit, outcome.Sources)
	}
	assertLocalBackendsHealthy(t, outcome.Sources)
	if ghRealModeConfigured() {
		assertGHBackendsHealthy(t, outcome.Sources)
		if res.exitCode != 0 {
			t.Fatalf("config validate: exit=%d, want 0 with gh authenticated and both local backends healthy; sources=%+v", res.exitCode, outcome.Sources)
		}
	}
}

func TestContract_AuthStatus_AllFourRealBackends(t *testing.T) {
	env := contractEnv(map[string]string{
		"PG_PR_CONFIG": writeContractRegistry(t),
	})

	res := runPGConnector(t, "", env, "auth", "status")
	outcome := decodeContractJSON[FanOutOutcome](t, res.stdout)
	assertFourBackendSources(t, outcome.Sources)

	// issue-beads/scm-git implement no AuthChecker at all (backend.go's own
	// doc comment, scm main.go's own doc comment) — pg-connector's generic
	// unknown_op recognition must report them "disabled: not applicable",
	// never a failure, regardless of bd/git availability.
	for _, name := range []string{"pg-connector-issue-beads", "pg-connector-scm-git"} {
		s := sourceFor(t, outcome.Sources, name)
		if s.Status != SourceDisabled {
			t.Fatalf("auth status: %s = %+v, want disabled (not applicable)", name, s)
		}
	}
	if ghRealModeConfigured() {
		assertGHBackendsHealthy(t, outcome.Sources)
	}

	wantExit := outcome.ExitCode()
	if res.exitCode != wantExit {
		t.Fatalf("auth status: exit=%d, want ExitCode()=%d for sources %+v", res.exitCode, wantExit, outcome.Sources)
	}
}

// assertFourBackendSources asserts sources contains exactly one row per
// real registered backend (contractBackendBinaries' four Tier-2 entries),
// each a well-formed row this contract-suite-built binary actually
// answered — never a malformed/missing row.
func assertFourBackendSources(t *testing.T, sources []SourceResult) {
	t.Helper()
	want := map[string]bool{
		"pg-connector-pr-github":         true,
		"pg-connector-ci-github-actions": true,
		"pg-connector-issue-beads":       true,
		"pg-connector-scm-git":           true,
	}
	if len(sources) != len(want) {
		t.Fatalf("sources = %+v, want exactly %d rows (one per registered backend)", sources, len(want))
	}
	for _, s := range sources {
		if !want[s.Source] {
			t.Fatalf("unexpected source %q in %+v", s.Source, sources)
		}
		delete(want, s.Source)
	}
	if len(want) != 0 {
		t.Fatalf("sources %+v missing backend(s): %v", sources, want)
	}
}

func sourceFor(t *testing.T, sources []SourceResult, name string) SourceResult {
	t.Helper()
	for _, s := range sources {
		if s.Source == name {
			return s
		}
	}
	t.Fatalf("no source named %q in %+v", name, sources)
	return SourceResult{}
}

// assertLocalBackendsHealthy asserts issue-beads and scm-git — neither of
// which implements auth_status — report SourceSucceeded deterministically:
// capabilities always answers regardless of bd/git availability
// (capabilitiesBase's own doc comment for issue-beads; the equivalent
// static response for scm-git), and a missing auth_status entry is
// recognized generically as "disabled" (== healthy, outcome.go's own
// ExitCode doc comment), never a failure — true independent of whether bd
// or git actually happens to be on THIS machine's PATH.
func assertLocalBackendsHealthy(t *testing.T, sources []SourceResult) {
	t.Helper()
	for _, name := range []string{"pg-connector-issue-beads", "pg-connector-scm-git"} {
		s := sourceFor(t, sources, name)
		if s.Status != SourceSucceeded {
			t.Fatalf("%s = %+v, want succeeded", name, s)
		}
	}
}

// assertGHBackendsHealthy asserts pr-github and ci-github-actions report
// SourceSucceeded — only called by a case that has already confirmed (via
// ghRealModeConfigured) that this run is pointed at a real GitHub PR with
// gh on PATH, so a degraded row here is a genuine defect, not an
// environment limitation.
func assertGHBackendsHealthy(t *testing.T, sources []SourceResult) {
	t.Helper()
	for _, name := range []string{"pg-connector-pr-github", "pg-connector-ci-github-actions"} {
		s := sourceFor(t, sources, name)
		if s.Status != SourceSucceeded {
			t.Fatalf("%s = %+v, want succeeded (this run is configured for real GitHub access)", name, s)
		}
	}
}

// ---------------------------------------------------------------------
// pr (pg-connector-pr-github): show, categorize, feedback-set — every op
// this backend's own capabilities response declares
// (cmd/pg-connector-pr-github/main.go's newDispatchTable) — against a
// real, caller-chosen PR/repo (requireGHRepoAndPR).
// ---------------------------------------------------------------------

func TestContract_PRGithub_RealGH_ShowCategorizeFeedbackSet(t *testing.T) {
	repo, prNumber := requireGHRepoAndPR(t)
	id := fmt.Sprintf("%s#%d", repo, prNumber)
	env := contractEnv(map[string]string{
		"PG_PR_CONFIG":   writeContractRegistry(t),
		"XDG_STATE_HOME": t.TempDir(),
	})

	shown := runPGConnector(t, "", env, "pr", "show", id)
	if shown.exitCode != 0 {
		t.Fatalf("pr show %s: exit=%d stderr=%s stdout=%s", id, shown.exitCode, shown.stderr, shown.stdout)
	}
	shownResp := decodeContractJSON[scriptout.Response](t, shown.stdout)
	var pr schema.PR
	if err := scriptout.Decode(shownResp.Result, &pr); err != nil {
		t.Fatalf("decode PR: %v", err)
	}
	if pr.ID != id {
		t.Fatalf("pr show %s: id = %q, want %q", id, pr.ID, id)
	}
	if pr.Repo != repo {
		t.Fatalf("pr show %s: repo = %q, want %q", id, pr.Repo, repo)
	}
	if pr.Number != prNumber {
		t.Fatalf("pr show %s: number = %d, want %d", id, pr.Number, prNumber)
	}
	if pr.Title == "" {
		t.Fatalf("pr show %s: empty title from a real PR", id)
	}
	t.Logf("real PR fetched: %s %q state=%s comments=%d reviews=%d", pr.ID, pr.Title, pr.State, len(pr.Comments), len(pr.Reviews))

	categorized := runPGConnector(t, "", env, "pr", "categorize", id, "--category", "focus")
	if categorized.exitCode != 0 {
		t.Fatalf("pr categorize %s: exit=%d stderr=%s", id, categorized.exitCode, categorized.stderr)
	}
	catResp := decodeContractJSON[scriptout.Response](t, categorized.stdout)
	var catResult schema.CategorizeResult
	if err := scriptout.Decode(catResp.Result, &catResult); err != nil {
		t.Fatalf("decode categorize result: %v", err)
	}
	if catResult.ID != id || catResult.Category != "focus" {
		t.Fatalf("pr categorize %s: got %+v, want category=focus", id, catResult)
	}

	reshown := runPGConnector(t, "", env, "pr", "show", id)
	reshownResp := decodeContractJSON[scriptout.Response](t, reshown.stdout)
	var reshownPR schema.PR
	if err := scriptout.Decode(reshownResp.Result, &reshownPR); err != nil {
		t.Fatalf("decode reshown PR: %v", err)
	}
	if reshownPR.Category != "focus" {
		t.Fatalf("pr show %s after categorize: category = %q, want focus (persisted write not reflected)", id, reshownPR.Category)
	}

	// feedback-set needs a real comment/review-thread entry id to set a
	// disposition on; not every real PR has one. When the caller-chosen PR
	// genuinely has none, this logs and moves on rather than failing —
	// there is nothing to point feedback-set at, and that is a fact about
	// the chosen PR, not a defect in this backend.
	commentID := firstCommentID(pr)
	if commentID == "" {
		t.Logf("pr %s has no comments/review-thread entries; skipping feedback-set assertion", id)
		return
	}
	fedback := runPGConnector(t, "", env, "pr", "feedback-set", id, commentID, "--disposition", "will-fix")
	if fedback.exitCode != 0 {
		t.Fatalf("pr feedback-set %s %s: exit=%d stderr=%s", id, commentID, fedback.exitCode, fedback.stderr)
	}
	fedbackResp := decodeContractJSON[scriptout.Response](t, fedback.stdout)
	var fedbackResult schema.FeedbackSetResult
	if err := scriptout.Decode(fedbackResp.Result, &fedbackResult); err != nil {
		t.Fatalf("decode feedback-set result: %v", err)
	}
	if fedbackResult.ID != id || fedbackResult.CommentID != commentID || fedbackResult.Disposition != schema.DispositionWillFix {
		t.Fatalf("pr feedback-set %s %s: got %+v, want disposition=will-fix", id, commentID, fedbackResult)
	}

	reshownAgain := runPGConnector(t, "", env, "pr", "show", id)
	reshownAgainResp := decodeContractJSON[scriptout.Response](t, reshownAgain.stdout)
	var reshownAgainPR schema.PR
	if err := scriptout.Decode(reshownAgainResp.Result, &reshownAgainPR); err != nil {
		t.Fatalf("decode reshown PR: %v", err)
	}
	if got := dispositionFor(reshownAgainPR, commentID); got != schema.DispositionWillFix {
		t.Fatalf("pr show %s after feedback-set: comment %s disposition = %q, want will-fix (persisted write not reflected)", id, commentID, got)
	}

	// A nonexistent PR number on the same real repo rounds-trips as
	// not_found (exit 4) against real gh, not a fake — the same
	// "GraphQL: Could not resolve to a PullRequest" classification this
	// backend's own unit tests pin against a fixture, confirmed here
	// against the real gh binary.
	bogus := runPGConnector(t, "", env, "pr", "show", fmt.Sprintf("%s#999999999", repo))
	if bogus.exitCode != 4 {
		t.Fatalf("pr show %s#999999999: exit=%d, want 4 (not_found); stdout=%s", repo, bogus.exitCode, bogus.stdout)
	}
}

// firstCommentID returns the id of pr's first top-level comment, or (if
// none) its first review's first comment, or "" if pr has no
// comments/review-thread entries at all.
func firstCommentID(pr schema.PR) string {
	if len(pr.Comments) > 0 {
		return pr.Comments[0].ID
	}
	for _, r := range pr.Reviews {
		if len(r.Comments) > 0 {
			return r.Comments[0].ID
		}
	}
	return ""
}

// dispositionFor returns commentID's disposition on pr, checking both
// top-level comments and every review's nested comments (mirroring
// firstCommentID's own search order).
func dispositionFor(pr schema.PR, commentID string) schema.Disposition {
	for _, c := range pr.Comments {
		if c.ID == commentID {
			return c.Disposition
		}
	}
	for _, r := range pr.Reviews {
		for _, c := range r.Comments {
			if c.ID == commentID {
				return c.Disposition
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------
// ci (pg-connector-ci-github-actions): list, logs, rerun-failed — every
// op this backend's own capabilities response declares
// (cmd/pg-connector-ci-github-actions/main.go's newDispatchTable) —
// against the same real, caller-chosen PR/repo.
// ---------------------------------------------------------------------

func TestContract_CIGithubActions_RealGH_ListLogsRerunFailed(t *testing.T) {
	repo, prNumber := requireGHRepoAndPR(t)
	id := fmt.Sprintf("%s#%d", repo, prNumber)
	env := contractEnv(map[string]string{
		"PG_PR_CONFIG": writeContractRegistry(t),
	})

	listed := runPGConnector(t, "", env, "ci", "list", id)
	if listed.exitCode != 0 {
		t.Fatalf("ci list %s: exit=%d stderr=%s stdout=%s", id, listed.exitCode, listed.stderr, listed.stdout)
	}
	outcome := decodeContractJSON[ciListOutcome](t, listed.stdout)
	if len(outcome.Sources) != 1 || outcome.Sources[0].Source != "pg-connector-ci-github-actions" {
		t.Fatalf("ci list %s: sources = %+v, want exactly one row for pg-connector-ci-github-actions", id, outcome.Sources)
	}
	if outcome.Sources[0].Status != SourceSucceeded {
		t.Fatalf("ci list %s: source = %+v, want succeeded (a real, resolvable PR/repo)", id, outcome.Sources[0])
	}
	for _, r := range outcome.Runs {
		if r.PRID != id {
			t.Fatalf("ci list %s: run %+v has pr_id %q, want %q", id, r, r.PRID, id)
		}
	}
	t.Logf("real CI runs found for %s: %d", id, len(outcome.Runs))

	if len(outcome.Runs) == 0 {
		// A genuinely nonexistent run id rounds-trips as not_found (exit 4)
		// against real gh — exercising get_logs' real wiring without a real
		// run to read logs from.
		logs := runPGConnector(t, "", env, "ci", "logs", "999999999999")
		if logs.exitCode != 4 {
			t.Fatalf("ci logs 999999999999: exit=%d, want 4 (not_found); stdout=%s", logs.exitCode, logs.stdout)
		}
	} else {
		logs := runPGConnector(t, "", env, "ci", "logs", outcome.Runs[0].ID)
		if logs.exitCode != 0 {
			t.Fatalf("ci logs %s: exit=%d stderr=%s", outcome.Runs[0].ID, logs.exitCode, logs.stderr)
		}
		logsResp := decodeContractJSON[scriptout.Response](t, logs.stdout)
		var raw []byte
		if err := scriptout.Decode(logsResp.Result, &raw); err != nil {
			t.Fatalf("decode logs: %v", err)
		}
		if len(raw) == 0 {
			t.Fatalf("ci logs %s: empty log body from a real run", outcome.Runs[0].ID)
		}
	}

	// rerun-failed either succeeds (a genuinely rerunnable run existed and
	// gh really re-ran it — an accepted real side effect on whatever real
	// PR the caller pointed this at, exactly like this suite's other real
	// mutations) or reports not_found (no rerunnable run) — both are
	// well-formed real answers; anything else is a defect.
	rerun := runPGConnector(t, "", env, "ci", "rerun-failed", id)
	if rerun.exitCode != 0 && rerun.exitCode != 4 {
		t.Fatalf("ci rerun-failed %s: exit=%d, want 0 or 4; stderr=%s stdout=%s", id, rerun.exitCode, rerun.stderr, rerun.stdout)
	}
}

// ---------------------------------------------------------------------
// Completion-script regression guard: bash/zsh/fish must each still
// generate non-empty output from the REAL binary (bead pg2-qp50z's own
// "fold in a completion regression check" ask) — a regression guard for
// functionality already confirmed working today (default.nix's postInstall
// already generates these three at package build time), not new
// functionality.
// ---------------------------------------------------------------------

func TestContract_CompletionScripts_NonEmpty(t *testing.T) {
	bin := filepath.Join(contractBinDir, "pg-connector")
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "completion", shell)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("pg-connector completion %s: %v\nstderr=%s", shell, err, stderr.String())
			}
			if len(strings.TrimSpace(stdout.String())) == 0 {
				t.Fatalf("pg-connector completion %s: empty output", shell)
			}
		})
	}
}
