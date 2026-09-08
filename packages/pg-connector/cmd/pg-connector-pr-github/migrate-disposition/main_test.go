package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// withFakeRunner swaps the package-level runPgPr var for fn for the
// duration of the calling test, restoring the original afterward. This
// mirrors pg-pr's own established pattern for injecting a fake in place of
// a real subprocess/config call (e.g. cmd/pg-pr's newConfigLoader,
// loadConfigForRepoPath package vars).
func withFakeRunner(t *testing.T, fn pgPrRunner) {
	t.Helper()
	orig := runPgPr
	runPgPr = fn
	t.Cleanup(func() { runPgPr = orig })
}

// fakeRunner returns a pgPrRunner keyed by the joined pg-pr subcommand
// args (e.g. "pr list --repo owner/repo1 --json"), mirroring
// migrate_test.go's own inline-JSON fixture style but for this tool's
// subprocess boundary rather than a direct Go struct literal. An
// unrecognised invocation fails the test loudly rather than silently
// returning an empty response.
func fakeRunner(t *testing.T, responses map[string]string, errs map[string]error) pgPrRunner {
	t.Helper()
	return func(_ context.Context, bin string, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		if err, ok := errs[key]; ok {
			return nil, err
		}
		resp, ok := responses[key]
		if !ok {
			t.Fatalf("fakeRunner: unexpected pg-pr invocation: %s %s", bin, key)
		}
		return []byte(resp), nil
	}
}

func TestFormatLocalPRID(t *testing.T) {
	if got, want := formatLocalPRID("owner/repo", 7), "owner/repo#7"; got != want {
		t.Fatalf("formatLocalPRID = %q, want %q", got, want)
	}
}

func TestParseFlags_Defaults(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	opts, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.pgPrBin != "pg-pr" {
		t.Errorf("pgPrBin = %q, want %q", opts.pgPrBin, "pg-pr")
	}
	if opts.dryRun {
		t.Error("dryRun = true, want false by default")
	}
	wantSuffix := filepath.Join("pg-connector-pr-github", "store.json")
	if !strings.HasSuffix(opts.store, wantSuffix) {
		t.Errorf("store = %q, want it to end with %q (internal.DefaultStorePath's own convention)", opts.store, wantSuffix)
	}
}

func TestParseFlags_OverridesAndDryRun(t *testing.T) {
	opts, err := parseFlags([]string{"--pg-pr-bin", "/opt/bin/pg-pr", "--store", "/tmp/store.json", "--dry-run"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.pgPrBin != "/opt/bin/pg-pr" || opts.store != "/tmp/store.json" || !opts.dryRun {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseFlags_RejectsRepoLikePositionalArgument(t *testing.T) {
	// This tool takes no --repo flag and no positional args at all — it
	// discovers every tracked repo itself (Produces: "No --repo flag on
	// THIS tool").
	if _, err := parseFlags([]string{"owner/repo"}, &bytes.Buffer{}); err == nil {
		t.Fatal("parseFlags: expected an error for an unexpected positional argument")
	}
}

// feedbackItemJSON renders one representative pg-pr store.Feedback row's
// JSON encoding, mirroring migrate_test.go's own
// TestLegacyFeedbackItem_DecodesPgPrFeedbackListJSONShape fixture: the real
// `pg-pr feedback list --json` output carries every exported store.Feedback
// field (no json tags), most irrelevant to this migration.
func feedbackItemJSON(kind, commentNodeID, externalID, dispositionAction string) string {
	return fmt.Sprintf(`{
		"ID": 1,
		"PRID": 1,
		"Kind": %q,
		"ExternalID": %q,
		"Fingerprint": "abc123",
		"Status": "open",
		"Title": "nit",
		"Body": "body",
		"AuthorLogin": "reviewer1",
		"AuthorKind": "human",
		"DispositionAction": %q,
		"DispositionNote": "",
		"File": "main.go",
		"Line": 10,
		"CommentNodeID": %q
	}`, kind, externalID, dispositionAction, commentNodeID)
}

// TestRun_HappyPath_ImportsAcrossReposAndPRs exercises the full
// enumerate -> export -> decode -> import pipeline against two tracked
// repos, proving: (1) config show --json drives repo discovery, (2) each
// repo's own pr list --repo --json call enumerates its PRs, (3) each PR's
// feedback list --json is imported via internal.ImportLegacyDispositions
// under this tool's own prID convention, and (4) the printed summary names
// every repo plus a final total.
func TestRun_HappyPath_ImportsAcrossReposAndPRs(t *testing.T) {
	responses := map[string]string{
		"config show --json":                `{"repos":[{"remote":"owner/repo1"},{"remote":"owner/repo2"}]}`,
		"pr list --repo owner/repo1 --json": `[{"repo":"owner/repo1","number":1},{"repo":"owner/repo1","number":2}]`,
		"pr list --repo owner/repo2 --json": `[{"repo":"owner/repo2","number":5}]`,
		"feedback list owner/repo1 1 --json": `[` +
			feedbackItemJSON("code-comment-thread", "PRRC_1", "", "will-fix") + `,` +
			feedbackItemJSON("ci-failure", "RUN_1", "", "will-fix") + // non-comment kind: skipped
			`]`,
		"feedback list owner/repo1 2 --json": `[` +
			feedbackItemJSON("pr-comments", "", "", "") + // never dispositioned: skipped
			`]`,
		"feedback list owner/repo2 5 --json": `[` +
			feedbackItemJSON("pr-comments", "IC_9", "", "no-action") + `]`,
	}
	withFakeRunner(t, fakeRunner(t, responses, nil))

	storePath := filepath.Join(t.TempDir(), "store.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--store", storePath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit code = %d, want 0; stderr=%s", code, stderr.String())
	}

	s := internal.NewStore(storePath)

	st1, err := s.Get("owner/repo1#1")
	if err != nil {
		t.Fatalf("Get owner/repo1#1: %v", err)
	}
	if st1.Dispositions["PRRC_1"] != schema.DispositionWillFix {
		t.Fatalf("owner/repo1#1 dispositions = %+v, want PRRC_1 -> will-fix", st1.Dispositions)
	}
	if len(st1.Dispositions) != 1 {
		t.Fatalf("owner/repo1#1 dispositions = %+v, want exactly one entry (ci-failure kind must be skipped)", st1.Dispositions)
	}

	st2, err := s.Get("owner/repo1#2")
	if err != nil {
		t.Fatalf("Get owner/repo1#2: %v", err)
	}
	if len(st2.Dispositions) != 0 {
		t.Fatalf("owner/repo1#2 dispositions = %+v, want empty (never-dispositioned item must not be imported)", st2.Dispositions)
	}

	st3, err := s.Get("owner/repo2#5")
	if err != nil {
		t.Fatalf("Get owner/repo2#5: %v", err)
	}
	if st3.Dispositions["IC_9"] != schema.DispositionNoAction {
		t.Fatalf("owner/repo2#5 dispositions = %+v, want IC_9 -> no-action", st3.Dispositions)
	}

	out := stdout.String()
	for _, want := range []string{"owner/repo1:", "owner/repo2:", "total: 2 disposition(s) imported across 2 repo(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

// TestRun_DryRun_DoesNotMutateRealStore proves --dry-run runs the full
// pipeline (the printed count reflects a real import) while never touching
// the real --store path at all (Produces: "WITHOUT calling SetDisposition
// (no mutation)").
func TestRun_DryRun_DoesNotMutateRealStore(t *testing.T) {
	responses := map[string]string{
		"config show --json":                 `{"repos":[{"remote":"owner/repo1"}]}`,
		"pr list --repo owner/repo1 --json":  `[{"repo":"owner/repo1","number":1}]`,
		"feedback list owner/repo1 1 --json": `[` + feedbackItemJSON("code-comment-thread", "PRRC_1", "", "will-fix") + `]`,
	}
	withFakeRunner(t, fakeRunner(t, responses, nil))

	realStore := filepath.Join(t.TempDir(), "store.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--store", realStore, "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit code = %d, want 0; stderr=%s", code, stderr.String())
	}

	if _, err := os.Stat(realStore); err == nil {
		t.Fatalf("--dry-run created the real store file at %s — it must never be opened/written", realStore)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", realStore, err)
	}

	if !strings.Contains(stdout.String(), "total: 1 disposition(s) imported across 1 repo(s)") {
		t.Errorf("stdout = %q, want the dry-run count to reflect the real import that would have happened", stdout.String())
	}
}

// TestRun_ConfigShowSubprocessFailure_IsFatal proves a subprocess-invocation
// failure at the repo-discovery step is fatal (Produces: "Exit code: ...
// nonzero on any subprocess-invocation or decode failure").
func TestRun_ConfigShowSubprocessFailure_IsFatal(t *testing.T) {
	withFakeRunner(t, fakeRunner(t, nil, map[string]error{
		"config show --json": errors.New("exit status 1"),
	}))

	var stdout, stderr bytes.Buffer
	code := run([]string{"--store", filepath.Join(t.TempDir(), "store.json")}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("run exit code = 0, want nonzero on a subprocess-invocation failure")
	}
	if !strings.Contains(stderr.String(), "config show") {
		t.Errorf("stderr = %q, want it to name the failing pg-pr invocation", stderr.String())
	}
}

// TestRun_FeedbackListDecodeFailure_IsFatal proves a malformed JSON payload
// from `pg-pr feedback list --json` is a fatal decode failure, not silently
// skipped.
func TestRun_FeedbackListDecodeFailure_IsFatal(t *testing.T) {
	responses := map[string]string{
		"config show --json":                 `{"repos":[{"remote":"owner/repo1"}]}`,
		"pr list --repo owner/repo1 --json":  `[{"repo":"owner/repo1","number":1}]`,
		"feedback list owner/repo1 1 --json": `not valid json`,
	}
	withFakeRunner(t, fakeRunner(t, responses, nil))

	var stdout, stderr bytes.Buffer
	code := run([]string{"--store", filepath.Join(t.TempDir(), "store.json")}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("run exit code = 0, want nonzero on a decode failure")
	}
	if !strings.Contains(stderr.String(), "decode") {
		t.Errorf("stderr = %q, want it to report a decode failure", stderr.String())
	}
}

// TestRun_UnrecognisedDispositionAction_ReportedNotFatal proves a per-item
// mapping error (an unrecognised legacy DispositionAction) is reported but
// does not abort the run — one bad row must not block every other PR's
// legitimate disposition from migrating, and the exit code stays 0.
func TestRun_UnrecognisedDispositionAction_ReportedNotFatal(t *testing.T) {
	responses := map[string]string{
		"config show --json":                `{"repos":[{"remote":"owner/repo1"}]}`,
		"pr list --repo owner/repo1 --json": `[{"repo":"owner/repo1","number":1}]`,
		"feedback list owner/repo1 1 --json": `[` +
			feedbackItemJSON("code-comment-thread", "PRRC_1", "", "bogus-action") + `,` +
			feedbackItemJSON("code-comment-thread", "PRRC_2", "", "will-fix") +
			`]`,
	}
	withFakeRunner(t, fakeRunner(t, responses, nil))

	storePath := filepath.Join(t.TempDir(), "store.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--store", storePath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit code = %d, want 0 (a per-item mapping error must not abort the run); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "bogus-action") {
		t.Errorf("stdout = %q, want the unrecognised DispositionAction to be reported", stdout.String())
	}

	s := internal.NewStore(storePath)
	st, err := s.Get("owner/repo1#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.Dispositions["PRRC_2"] != schema.DispositionWillFix {
		t.Fatalf("dispositions = %+v, want PRRC_2 imported despite the sibling error", st.Dispositions)
	}
	if _, ok := st.Dispositions["PRRC_1"]; ok {
		t.Fatalf("dispositions = %+v, want no entry for the unrecognised-action item", st.Dispositions)
	}
}

// TestRun_NoTrackedRepos_SucceedsWithZeroTotal proves an empty repos[] list
// from config show is not an error — zero repos tracked means zero work,
// not a failure.
func TestRun_NoTrackedRepos_SucceedsWithZeroTotal(t *testing.T) {
	withFakeRunner(t, fakeRunner(t, map[string]string{
		"config show --json": `{"repos":[]}`,
	}, nil))

	var stdout, stderr bytes.Buffer
	code := run([]string{"--store", filepath.Join(t.TempDir(), "store.json")}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "total: 0 disposition(s) imported across 0 repo(s)") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// TestRun_IdempotentReimport proves re-running against the same fixture
// export reaches the same end state, never a duplicate or a second
// conflicting write [binding decision: idempotent].
func TestRun_IdempotentReimport(t *testing.T) {
	responses := map[string]string{
		"config show --json":                 `{"repos":[{"remote":"owner/repo1"}]}`,
		"pr list --repo owner/repo1 --json":  `[{"repo":"owner/repo1","number":1}]`,
		"feedback list owner/repo1 1 --json": `[` + feedbackItemJSON("code-comment-thread", "PRRC_1", "", "will-fix") + `]`,
	}
	withFakeRunner(t, fakeRunner(t, responses, nil))

	storePath := filepath.Join(t.TempDir(), "store.json")
	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--store", storePath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run #%d exit code = %d; stderr=%s", i, code, stderr.String())
		}
	}

	s := internal.NewStore(storePath)
	st, err := s.Get("owner/repo1#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(st.Dispositions) != 1 || st.Dispositions["PRRC_1"] != schema.DispositionWillFix {
		t.Fatalf("dispositions = %+v, want exactly one PRRC_1 -> will-fix after two runs", st.Dispositions)
	}
}

// TestRun_UsesConfiguredPgPrBin proves --pg-pr-bin's value is actually
// threaded through to the subprocess seam rather than a hardcoded "pg-pr".
func TestRun_UsesConfiguredPgPrBin(t *testing.T) {
	var gotBin string
	withFakeRunner(t, func(_ context.Context, bin string, args ...string) ([]byte, error) {
		gotBin = bin
		if strings.Join(args, " ") == "config show --json" {
			return []byte(`{"repos":[]}`), nil
		}
		return nil, fmt.Errorf("unexpected call: %v", args)
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"--pg-pr-bin", "/custom/pg-pr", "--store", filepath.Join(t.TempDir(), "store.json")}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit code = %d; stderr=%s", code, stderr.String())
	}
	if gotBin != "/custom/pg-pr" {
		t.Fatalf("bin passed to the subprocess seam = %q, want %q", gotBin, "/custom/pg-pr")
	}
}
