package gather

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// gather_test.go is this packet's wire-double test harness: a reentrant
// test-helper process impersonates pg-connector (mirroring
// packages/pg-router-source-pg-connector/cmd/pg-router-source-pg-connector's
// testmain_test.go pattern exactly, one layer up), and every fixture the
// helper answers a TARGETED op with (pr show/files/commits, issue deps) is
// checked against pkg/scriptout/conformance's real wire-envelope schema
// (TestFixturesConformToWireSchema below) — reusing that package rather
// than hand-rolling a parallel shape check, per this packet's own Files
// instruction, confirmed importable at
// github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance
// via `git grep -rl scriptout/conformance`. Fan-out ops (ci list, issue
// list) have no capability-specific schema in that package (it covers only
// the generic wire envelope), so their fixtures are plain literals, exactly
// like packages/pg-connector's own CLI layer prints them (no result/error
// wrapper).

// --- reentrant helper-process plumbing -------------------------------------

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

// ambientBeadsDir is the value this test suite sets for the inherited
// $BEADS_DIR in every helper invocation, so a test can prove gather's own
// PG_CONNECTOR_ISSUE_BEADS_DIR fallback rule ("nothing at all when the
// configured beads_dir is empty, so the child falls back to whatever
// BEADS_DIR it inherited") without depending on whatever $BEADS_DIR the
// real test-runner environment happens to have.
const ambientBeadsDir = "ambient-beads-dir"

// helperCmdFactory mirrors packages/pg-router-source-pg-connector's own
// helperCmdFactory: re-execs this test binary with
// -test.run=^TestHelperProcess$, recognized by TestHelperProcess above.
func helperCmdFactory(behavior, recordFile string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(
			os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"GO_HELPER_BEHAVIOR="+behavior,
			"GO_HELPER_CALLS_RECORD_FILE="+recordFile,
			"BEADS_DIR="+ambientBeadsDir,
		)
		return cmd
	}
}

// withFactory installs the reentrant helper as execCmdFactory for the
// duration of one test and returns the path of the calls-record file the
// helper appends one JSON line to per invocation (see callRecord).
func withFactory(t *testing.T, behavior string) string {
	t.Helper()
	recordFile := filepath.Join(t.TempDir(), "calls.jsonl")
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory(behavior, recordFile)
	t.Cleanup(func() { execCmdFactory = orig })
	return recordFile
}

// callRecord is one line of the calls-record file: the real pg-connector
// CLI args gather invoked (proving "every one of the six inputs is called
// with the right args"), plus what this helper process itself observed for
// the two beads-workspace env vars (proving the PG_CONNECTOR_ISSUE_BEADS_DIR
// / BEADS_DIR fallback rule).
type callRecord struct {
	Args                []string `json:"args"`
	PGConnectorBeadsDir string   `json:"pg_connector_issue_beads_dir"`
	BeadsDir            string   `json:"beads_dir"`
}

const unsetSentinel = "<unset>"

func readCallRecords(t *testing.T, path string) []callRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	var out []callRecord
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec callRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode call record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// verbOf joins a call's first two args ("pr show", "issue deps", ...) —
// this suite's own shorthand for "which of the six inputs was this".
func (r callRecord) verb() string {
	if len(r.Args) < 2 {
		return strings.Join(r.Args, " ")
	}
	return r.Args[0] + " " + r.Args[1]
}

func findChildArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			if i+2 <= len(os.Args) {
				return os.Args[i+2:]
			}
			return nil
		}
	}
	return nil
}

func recordCall(args []string) {
	file := os.Getenv("GO_HELPER_CALLS_RECORD_FILE")
	if file == "" {
		return
	}
	lookup := func(name string) string {
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return unsetSentinel
	}
	rec := callRecord{
		Args:                args,
		PGConnectorBeadsDir: lookup("PG_CONNECTOR_ISSUE_BEADS_DIR"),
		BeadsDir:            lookup("BEADS_DIR"),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// --- canned fixtures --------------------------------------------------------

const (
	fixtureRepo    = "myorg/repo"
	fixtureNumber  = 42
	fixturePRKey   = "myorg/repo#42"
	fixtureHeadSHA = "sha-AAA"
	fixtureAsOf    = "2026-09-16T00:00:00Z"
	fixtureBeadID  = "bd-work-1"

	// fixtureTicketKey1/2 are Phase 13's own ticket-key scan fixtures.
	// fixtureTicketKey1 appears in BOTH branch and title (proving the
	// "upsert once per field, one issue show call" rule); fixtureTicketKey2
	// appears only in body (proving multiple distinct keys in one PR are
	// each recognized).
	fixtureTicketKey1 = "PROJ-99"
	fixtureTicketKey2 = "PROJ-100"
)

var fixtureTicketPatterns = []string{`[A-Z]+-\d+`}

func prShowFixture(state string, merged bool, headSHA string) string {
	return `{"protocolVersion":1,"schemaVersion":4,"result":{` +
		`"id":"PR1","repo":"` + fixtureRepo + `","number":` + itoa(fixtureNumber) + `,` +
		`"state":"` + state + `","merged":` + boolStr(merged) + `,` +
		`"head_sha":"` + headSHA + `","as_of":"` + fixtureAsOf + `"}}`
}

// prShowWithTicketsFixture is prShowFixture widened with a
// branch/title/body carrying fixtureTicketKey1 (branch + title) and
// fixtureTicketKey2 (body only) — Phase 13's own ticket-key scan input.
func prShowWithTicketsFixture() string {
	return `{"protocolVersion":1,"schemaVersion":4,"result":{` +
		`"id":"PR1","repo":"` + fixtureRepo + `","number":` + itoa(fixtureNumber) + `,` +
		`"title":"fix ` + fixtureTicketKey1 + `: the thing","branch":"user.` + fixtureTicketKey1 + `.fix",` +
		`"body":"Also relates to ` + fixtureTicketKey2 + `.",` +
		`"state":"open","merged":false,` +
		`"head_sha":"` + fixtureHeadSHA + `","as_of":"` + fixtureAsOf + `"}}`
}

// jiraIssueShowFixture is a canned `issue show <ticket-key>` result for
// Phase 13's ticket-key scan — schema.Issue's own shape, minimal.
func jiraIssueShowFixture(key, priority string) string {
	return `{"protocolVersion":1,"schemaVersion":5,"result":{` +
		`"id":"` + key + `","title":"a jira issue","state":"open","priority":"` + priority + `"}}`
}

func notFoundFixture() string {
	return `{"protocolVersion":1,"error":{"code":"not_found","message":"no such pr"}}`
}

func prFilesFixture() string {
	return `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PR1","files":[{"path":"a.go","additions":1,"deletions":0}]}}`
}

func prCommitsFixture() string {
	return `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PR1","commits":[{"sha":"` + fixtureHeadSHA + `","author":"alice","message":"m"}]}}`
}

func ciListFixture() string {
	return `{"runs":[{"id":"run1","name":"ci","status":"completed","conclusion":"success","provider":"github","head_sha":"` + fixtureHeadSHA + `","pr_id":"PR1"}],"sources":[{"source":"b1","status":"succeeded","count":1}]}`
}

// issueListFixture returns a work-beads fan-out result with exactly one
// entity matching fixtureRepo/fixtureNumber by its "<repo>#<n>:" title
// prefix (design section 7.5's own merge-request/review-pr bead shape).
func issueListFixture() string {
	return `{"entities":[{"id":"` + fixtureBeadID + `","title":"` + fixturePRKey + `: fix the bug","metadata":{}}],` +
		`"present_ids":["` + fixtureBeadID + `"],"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":1}]}`
}

func issueDepsFixture() string {
	return `{"protocolVersion":1,"schemaVersion":1,"result":{"ids":["bd-blocker-1"],"entities":[]}}`
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// --- helper process dispatch -------------------------------------------------

func helperMain() {
	args := findChildArgs()
	recordCall(args)
	behavior := os.Getenv("GO_HELPER_BEHAVIOR")
	verb := ""
	if len(args) >= 2 {
		verb = args[0] + " " + args[1]
	}

	writeAndExit := func(body string, exitCode int) {
		_, _ = os.Stdout.WriteString(body)
		os.Exit(exitCode)
	}

	switch behavior {
	case "happy", "degraded_ci":
		switch verb {
		case "pr show":
			writeAndExit(prShowFixture("open", false, fixtureHeadSHA), 0)
		case "pr files":
			writeAndExit(prFilesFixture(), 0)
		case "pr commits":
			writeAndExit(prCommitsFixture(), 0)
		case "ci list":
			if behavior == "degraded_ci" {
				os.Stderr.WriteString("unavailable")
				os.Exit(3)
			}
			writeAndExit(ciListFixture(), 0)
		case "issue list":
			writeAndExit(issueListFixture(), 0)
		case "issue deps":
			writeAndExit(issueDepsFixture(), 0)
		default:
			os.Stderr.WriteString("unexpected verb: " + verb)
			os.Exit(99)
		}
	case "happy_with_jira", "jira_not_found", "jira_error":
		// Same six-input happy path, but `pr show` carries ticket keys in
		// branch/title/body, and `issue show` (the seventh input) answers
		// per this behavior's own name.
		switch verb {
		case "pr show":
			writeAndExit(prShowWithTicketsFixture(), 0)
		case "pr files":
			writeAndExit(prFilesFixture(), 0)
		case "pr commits":
			writeAndExit(prCommitsFixture(), 0)
		case "ci list":
			writeAndExit(ciListFixture(), 0)
		case "issue list":
			writeAndExit(issueListFixture(), 0)
		case "issue deps":
			writeAndExit(issueDepsFixture(), 0)
		case "issue show":
			key := ""
			if len(args) >= 3 {
				key = args[2]
			}
			switch behavior {
			case "jira_not_found":
				writeAndExit(notFoundFixture(), 4)
			case "jira_error":
				os.Stderr.WriteString("unavailable")
				os.Exit(1)
			default:
				priority := "Low"
				if key == fixtureTicketKey1 {
					priority = "Highest"
				}
				writeAndExit(jiraIssueShowFixture(key, priority), 0)
			}
		default:
			os.Stderr.WriteString("unexpected verb: " + verb)
			os.Exit(99)
		}
	case "removed_open":
		writeAndExit(prShowFixture("open", false, fixtureHeadSHA), 0)
	case "removed_merged":
		writeAndExit(prShowFixture("closed", true, fixtureHeadSHA), 0)
	case "removed_closed":
		writeAndExit(prShowFixture("closed", false, fixtureHeadSHA), 0)
	case "removed_not_found":
		writeAndExit(notFoundFixture(), 4)
	case "sweep_same_head":
		switch verb {
		case "pr show":
			writeAndExit(prShowFixture("open", false, fixtureHeadSHA), 0)
		default:
			os.Stderr.WriteString("unexpected call for an unchanged sweep: " + verb)
			os.Exit(99)
		}
	default:
		os.Stderr.WriteString("unknown GO_HELPER_BEHAVIOR: " + behavior)
		os.Exit(99)
	}
}

// --- tests -------------------------------------------------------------------

func testConfig(beadsDir string) *config.Config {
	return &config.Config{
		SelfLogin: "tester",
		Repos:     []config.RepoConfig{{Remote: fixtureRepo, BeadsDir: beadsDir}},
	}
}

// testConfigWithTicketPatterns mirrors testConfig, additionally populating
// TicketPatterns — Phase 13's own config-driven source of valid ticket-key
// patterns (see this package's gatherJiraXrefs), never a hardcoded pattern.
func testConfigWithTicketPatterns(beadsDir string, patterns []string) *config.Config {
	cfg := testConfig(beadsDir)
	cfg.TicketPatterns = patterns
	return cfg
}

// fakeXrefUpserter is this suite's own xrefUpserter test double (mirroring
// this package's local-interface pattern one layer down): it records every
// UpsertXref call without touching a real SQLite store, and answers
// ListXrefsByFrom from a canned linkedThreads slice/error the caller sets
// (Phase 13's eighth input, gatherLinkedThreads, below) — zero-value
// (nil, nil) by default, matching "no linked threads, no error" for every
// pre-existing test that never sets it.
type fakeXrefUpserter struct {
	upserts       []store.Xref
	linkedThreads []store.Xref
	listErr       error
}

func (f *fakeXrefUpserter) UpsertXref(x store.Xref) error {
	f.upserts = append(f.upserts, x)
	return nil
}

func (f *fakeXrefUpserter) ListXrefsByFrom(repo, fromType, fromID, toType string) ([]store.Xref, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.linkedThreads, nil
}

// TestFixturesConformToWireSchema proves this suite's own targeted-op
// fixtures are real, protocol-valid wire envelopes — the actual "wire
// double" check this packet's Files bullet asks for, reusing
// pkg/scriptout/conformance rather than hand-rolling a parallel shape
// check.
func TestFixturesConformToWireSchema(t *testing.T) {
	successFixtures := []string{
		prShowFixture("open", false, fixtureHeadSHA),
		prShowFixture("closed", true, fixtureHeadSHA),
		prShowWithTicketsFixture(),
		prFilesFixture(),
		prCommitsFixture(),
		issueDepsFixture(),
		jiraIssueShowFixture(fixtureTicketKey1, "Highest"),
	}
	for _, fx := range successFixtures {
		if err := conformance.CheckResponseBytes([]byte(fx)); err != nil {
			t.Fatalf("fixture %q does not conform to the wire envelope schema: %v", fx, err)
		}
	}
	if err := conformance.CheckResponseBytes([]byte(notFoundFixture())); err != nil {
		t.Fatalf("not_found fixture does not conform to the wire envelope schema: %v", err)
	}
}

// TestGather_SixInputs_CalledWithRightArgsAndEnv is the packet's own
// primary acceptance criterion: every one of the six gather inputs is
// invoked, with the right positional args, and every ISSUE exec carries
// PG_CONNECTOR_ISSUE_BEADS_DIR while every PR/CI exec does not.
func TestGather_SixInputs_CalledWithRightArgsAndEnv(t *testing.T) {
	recordFile := withFactory(t, "happy")
	g := NewGatherer(testConfig("/configured/beads"), nil)

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if facts.Degraded != "" {
		t.Fatalf("Degraded = %q, want empty", facts.Degraded)
	}
	if facts.HeadSHA != fixtureHeadSHA || facts.AsOf != fixtureAsOf {
		t.Fatalf("HeadSHA/AsOf = %q/%q, want %q/%q", facts.HeadSHA, facts.AsOf, fixtureHeadSHA, fixtureAsOf)
	}
	for name, raw := range map[string]json.RawMessage{
		"PRShow": facts.PRShow, "PRFiles": facts.PRFiles, "PRCommits": facts.PRCommits,
		"CI": facts.CI, "WorkBeads": facts.WorkBeads, "Deps": facts.Deps,
	} {
		if len(raw) == 0 {
			t.Fatalf("Facts.%s is empty, want populated", name)
		}
	}

	records := readCallRecords(t, recordFile)
	byVerb := map[string]callRecord{}
	for _, r := range records {
		byVerb[r.verb()] = r
	}
	wantVerbs := []string{"pr show", "pr files", "pr commits", "ci list", "issue list", "issue deps"}
	if len(records) != len(wantVerbs) {
		t.Fatalf("recorded %d calls, want exactly %d: %+v", len(records), len(wantVerbs), records)
	}
	for _, v := range wantVerbs {
		if _, ok := byVerb[v]; !ok {
			t.Fatalf("missing call for %q; recorded: %+v", v, records)
		}
	}

	if got := byVerb["pr show"].Args; strings.Join(got, " ") != "pr show PR1" {
		t.Fatalf("pr show args = %v, want [pr show PR1]", got)
	}
	if got := byVerb["pr files"].Args; strings.Join(got, " ") != "pr files PR1" {
		t.Fatalf("pr files args = %v, want [pr files PR1]", got)
	}
	if got := byVerb["pr commits"].Args; strings.Join(got, " ") != "pr commits PR1" {
		t.Fatalf("pr commits args = %v, want [pr commits PR1]", got)
	}
	if got := byVerb["ci list"].Args; strings.Join(got, " ") != "ci list PR1" {
		t.Fatalf("ci list args = %v, want [ci list PR1]", got)
	}
	if got := byVerb["issue list"].Args; strings.Join(got, " ") != "issue list --query work-beads" {
		t.Fatalf("issue list args = %v, want [issue list --query work-beads]", got)
	}
	if got := byVerb["issue deps"].Args; strings.Join(got, " ") != "issue deps "+fixtureBeadID+" --full" {
		t.Fatalf("issue deps args = %v, want [issue deps %s --full]", got, fixtureBeadID)
	}

	// PG_CONNECTOR_ISSUE_BEADS_DIR/BEADS_DIR fallback (acceptance
	// criterion #2): every issue exec carries the configured dir; every
	// pr/ci exec leaves it unset (and BEADS_DIR passes through
	// unmodified everywhere, proving gather never clobbers it).
	for _, v := range wantVerbs {
		rec := byVerb[v]
		if rec.BeadsDir != ambientBeadsDir {
			t.Fatalf("%s: BEADS_DIR = %q, want unmodified ambient %q", v, rec.BeadsDir, ambientBeadsDir)
		}
		wantIssueDir := unsetSentinel
		if strings.HasPrefix(v, "issue ") {
			wantIssueDir = "/configured/beads"
		}
		if rec.PGConnectorBeadsDir != wantIssueDir {
			t.Fatalf("%s: PG_CONNECTOR_ISSUE_BEADS_DIR = %q, want %q", v, rec.PGConnectorBeadsDir, wantIssueDir)
		}
	}
}

// TestGather_NoConfiguredBeadsDir_FallsBackToAmbientBEADSDIR proves the
// other half of the fallback rule: an empty configured beads_dir means
// gather sets nothing at all, never PG_CONNECTOR_ISSUE_BEADS_DIR="".
func TestGather_NoConfiguredBeadsDir_FallsBackToAmbientBEADSDIR(t *testing.T) {
	recordFile := withFactory(t, "happy")
	g := NewGatherer(testConfig(""), nil)

	if _, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, r := range readCallRecords(t, recordFile) {
		if !strings.HasPrefix(r.verb(), "issue ") {
			continue
		}
		if r.PGConnectorBeadsDir != unsetSentinel {
			t.Fatalf("%s: PG_CONNECTOR_ISSUE_BEADS_DIR = %q, want unset (never \"\")", r.verb(), r.PGConnectorBeadsDir)
		}
		if r.BeadsDir != ambientBeadsDir {
			t.Fatalf("%s: BEADS_DIR = %q, want unmodified ambient %q", r.verb(), r.BeadsDir, ambientBeadsDir)
		}
	}
}

// TestGather_DegradedInput_RunContinues is the packet's required
// degradation test: ci list fails, everything else still succeeds, and the
// run reports degraded rather than erroring.
func TestGather_DegradedInput_RunContinues(t *testing.T) {
	withFactory(t, "degraded_ci")
	g := NewGatherer(testConfig("/configured/beads"), nil)

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeChanged)
	if err != nil {
		t.Fatalf("Gather returned an error for a non-critical degraded input: %v", err)
	}
	if facts.Degraded != "ci list" {
		t.Fatalf("Degraded = %q, want %q", facts.Degraded, "ci list")
	}
	if len(facts.CI) != 0 {
		t.Fatalf("CI = %q, want empty on a failed ci list call", string(facts.CI))
	}
	if len(facts.PRShow) == 0 || len(facts.PRFiles) == 0 || len(facts.WorkBeads) == 0 || len(facts.Deps) == 0 {
		t.Fatalf("degradation of one non-critical input must not blank the others: %+v", facts)
	}
}

// TestGather_Removed_ReReadRule is the packet's required removed re-read
// test, covering all four RemovedState outcomes the design pins.
func TestGather_Removed_ReReadRule(t *testing.T) {
	cases := []struct {
		behavior string
		want     string
	}{
		{"removed_open", "open"},
		{"removed_merged", "merged"},
		{"removed_closed", "closed"},
		{"removed_not_found", "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.behavior, func(t *testing.T) {
			recordFile := withFactory(t, tc.behavior)
			g := NewGatherer(testConfig("/configured/beads"), nil)

			facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeRemoved)
			if err != nil {
				t.Fatalf("Gather: %v", err)
			}
			if facts.RemovedState != tc.want {
				t.Fatalf("RemovedState = %q, want %q", facts.RemovedState, tc.want)
			}
			// The re-read is exactly one pr show call — removed never
			// fetches files/commits/ci/work-beads/deps.
			records := readCallRecords(t, recordFile)
			if len(records) != 1 || records[0].verb() != "pr show" {
				t.Fatalf("removed handling made calls %+v, want exactly one [pr show ...]", records)
			}
		})
	}
}

// TestGather_HardFailure_TriggeringEntityOnly proves the design's other
// half of degradation: a failure fetching the triggering PR itself (not a
// removed re-read) is a hard error, not a degradation.
func TestGather_HardFailure_TriggeringEntityOnly(t *testing.T) {
	withFactory(t, "removed_not_found") // pr show itself answers not_found
	g := NewGatherer(testConfig("/configured/beads"), nil)

	_, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded)
	if err == nil {
		t.Fatal("Gather returned no error for a failed triggering-entity fetch on a non-removed change")
	}
}

// TestGather_UnsupportedEntityType_Rejected proves Gather rejects anything
// other than "pr" outright rather than silently no-op'ing (this phase's
// gather is pr-triggered only; see the package doc comment).
func TestGather_UnsupportedEntityType_Rejected(t *testing.T) {
	withFactory(t, "happy")
	g := NewGatherer(testConfig("/configured/beads"), nil)

	_, err := g.Gather(context.Background(), "issue", "BD1", ChangeAdded)
	if err == nil {
		t.Fatal("Gather accepted entityType \"issue\", want a rejection (pr-triggered gather only in Phase 9)")
	}
}

// TestGather_Sweep_UnchangedHead_SkipsRestOfStageOne proves the per-run
// budget's sweep-skip half: a second sweep call for the same entity, whose
// head has not moved since this Gatherer's own last call for it, makes no
// further calls at all.
func TestGather_Sweep_UnchangedHead_SkipsRestOfStageOne(t *testing.T) {
	recordFile := withFactory(t, "happy")
	g := NewGatherer(testConfig("/configured/beads"), nil)

	if _, err := g.Gather(context.Background(), "pr", "PR1", ChangeSweep); err != nil {
		t.Fatalf("first Gather: %v", err)
	}
	firstCount := len(readCallRecords(t, recordFile))
	if firstCount != 6 {
		t.Fatalf("first sweep made %d calls, want 6 (full stage 1)", firstCount)
	}

	// Switch to a behavior that only tolerates a "pr show" call — proving
	// the second sweep, at the same head_sha, makes no further calls.
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory("sweep_same_head", recordFile)
	t.Cleanup(func() { execCmdFactory = orig })

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeSweep)
	if err != nil {
		t.Fatalf("second Gather: %v", err)
	}
	if facts.Degraded != "" {
		t.Fatalf("Degraded = %q, want empty on an intentional sweep skip", facts.Degraded)
	}

	all := readCallRecords(t, recordFile)
	if len(all) != firstCount+1 {
		t.Fatalf("second sweep made %d additional call(s), want exactly 1 (pr show only): %+v", len(all)-firstCount, all[firstCount:])
	}
	if all[firstCount].verb() != "pr show" {
		t.Fatalf("second sweep's one call was %q, want \"pr show\"", all[firstCount].verb())
	}
}

// TestGather_HeadSHACache_SkipsRedundantFilesCommits proves the per-run
// budget's files/commits half: a second call for the same PR at the same
// head_sha (here, an "added" then a "changed" event that observed no head
// movement) reuses the cached files/commits rather than re-fetching them.
func TestGather_HeadSHACache_SkipsRedundantFilesCommits(t *testing.T) {
	recordFile := withFactory(t, "happy")
	g := NewGatherer(testConfig("/configured/beads"), nil)

	if _, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded); err != nil {
		t.Fatalf("first Gather: %v", err)
	}
	firstCount := len(readCallRecords(t, recordFile))

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeChanged)
	if err != nil {
		t.Fatalf("second Gather: %v", err)
	}
	if len(facts.PRFiles) == 0 || len(facts.PRCommits) == 0 {
		t.Fatalf("second Gather returned empty PRFiles/PRCommits, want the cached copy")
	}

	all := readCallRecords(t, recordFile)
	for _, r := range all[firstCount:] {
		if r.verb() == "pr files" || r.verb() == "pr commits" {
			t.Fatalf("second Gather re-invoked %q at an unchanged head_sha, want the cache reused", r.verb())
		}
	}
}

// --- Phase 13: Jira ticket-key scan (packet pg2-2j5ac.40.2) ----------------

// TestGather_JiraTicketScan_UpsertsXrefsAndPopulatesJiraIssues is this
// packet's own primary acceptance criterion: a PR whose branch/title/body
// contains a recognizable Jira ticket key gets an `issue show` gather call
// and an upserted xref row, and the fetched issue lands in Facts.JiraIssues
// for interpret's scoreUrgencyWithHealth to read.
func TestGather_JiraTicketScan_UpsertsXrefsAndPopulatesJiraIssues(t *testing.T) {
	recordFile := withFactory(t, "happy_with_jira")
	xrefs := &fakeXrefUpserter{}
	g := NewGatherer(testConfigWithTicketPatterns("/configured/beads", fixtureTicketPatterns), xrefs)

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if facts.Degraded != "" {
		t.Fatalf("Degraded = %q, want empty", facts.Degraded)
	}

	// fixtureTicketKey1 is found in both branch and title -> one issue
	// show call (dedup fetch), two xref upserts (one per field);emphasises
	// the "upsert once per field, fetch once per distinct key" rule.
	// fixtureTicketKey2 is found only in body -> one issue show call, one
	// xref upsert.
	records := readCallRecords(t, recordFile)
	issueShowCalls := 0
	for _, r := range records {
		if r.verb() == "issue show" {
			issueShowCalls++
		}
	}
	if issueShowCalls != 2 {
		t.Fatalf("recorded %d \"issue show\" calls, want exactly 2 (one per distinct key): %+v", issueShowCalls, records)
	}

	if len(xrefs.upserts) != 3 {
		t.Fatalf("recorded %d UpsertXref calls, want exactly 3 (key1 x branch+title, key2 x body): %+v", len(xrefs.upserts), xrefs.upserts)
	}
	byKeyAndEvidence := map[string]bool{}
	for _, x := range xrefs.upserts {
		if x.Repo != fixtureRepo || x.FromType != "pr" || x.FromID != "PR1" || x.ToType != "issue" {
			t.Fatalf("unexpected xref shape: %+v", x)
		}
		byKeyAndEvidence[x.ToID+"/"+x.Evidence] = true
	}
	for _, want := range []string{
		fixtureTicketKey1 + "/branch",
		fixtureTicketKey1 + "/title",
		fixtureTicketKey2 + "/body",
	} {
		if !byKeyAndEvidence[want] {
			t.Fatalf("missing xref upsert %q; got %+v", want, xrefs.upserts)
		}
	}

	if len(facts.JiraIssues) != 2 {
		t.Fatalf("Facts.JiraIssues has %d entries, want 2: %+v", len(facts.JiraIssues), facts.JiraIssues)
	}
	for _, key := range []string{fixtureTicketKey1, fixtureTicketKey2} {
		if len(facts.JiraIssues[key]) == 0 {
			t.Fatalf("Facts.JiraIssues[%q] is empty, want the fetched issue show result", key)
		}
	}
}

// TestGather_JiraTicketScan_NotFoundIsNotADegradation proves the
// not_found half of this method's own doc comment: a ticket key found in
// text but currently unknown to Jira is a well-formed negative answer
// (Facts stays healthy, no JiraIssues entry) — but the xref is STILL
// upserted, since the PR's text does reference that key regardless of
// whether Jira answers for it today.
func TestGather_JiraTicketScan_NotFoundIsNotADegradation(t *testing.T) {
	withFactory(t, "jira_not_found")
	xrefs := &fakeXrefUpserter{}
	g := NewGatherer(testConfigWithTicketPatterns("/configured/beads", fixtureTicketPatterns), xrefs)

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if facts.Degraded != "" {
		t.Fatalf("Degraded = %q, want empty (not_found is a well-formed negative answer)", facts.Degraded)
	}
	if len(facts.JiraIssues) != 0 {
		t.Fatalf("Facts.JiraIssues = %+v, want empty on a not_found issue show", facts.JiraIssues)
	}
	if len(xrefs.upserts) != 3 {
		t.Fatalf("recorded %d UpsertXref calls, want exactly 3 (the xref is upserted regardless of the issue show outcome): %+v", len(xrefs.upserts), xrefs.upserts)
	}
}

// TestGather_JiraTicketScan_ErrorDegrades proves the OTHER-than-not_found
// failure half: a genuine issue show failure degrades this run exactly
// like every other non-triggering-entity input, naming "issue show".
func TestGather_JiraTicketScan_ErrorDegrades(t *testing.T) {
	withFactory(t, "jira_error")
	g := NewGatherer(testConfigWithTicketPatterns("/configured/beads", fixtureTicketPatterns), &fakeXrefUpserter{})

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded)
	if err != nil {
		t.Fatalf("Gather returned an error for a non-critical degraded input: %v", err)
	}
	if facts.Degraded != "issue show" {
		t.Fatalf("Degraded = %q, want %q", facts.Degraded, "issue show")
	}
}

// TestGather_NoTicketPatterns_NoJiraCallsAtAll proves the safe default:
// an unconfigured (empty) TicketPatterns list means no ticket key is ever
// recognized, so this seventh input makes no calls at all — exactly its
// pre-Phase-13 behavior, even when the PR's own text would otherwise
// match a hardcoded pattern.
func TestGather_NoTicketPatterns_NoJiraCallsAtAll(t *testing.T) {
	recordFile := withFactory(t, "happy_with_jira")
	xrefs := &fakeXrefUpserter{}
	g := NewGatherer(testConfig("/configured/beads"), xrefs) // no TicketPatterns configured

	if _, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() == "issue show" {
			t.Fatalf("recorded an \"issue show\" call with no TicketPatterns configured: %+v", r)
		}
	}
	if len(xrefs.upserts) != 0 {
		t.Fatalf("recorded %d UpsertXref calls with no TicketPatterns configured, want 0: %+v", len(xrefs.upserts), xrefs.upserts)
	}
}

// --- Phase 13: eighth input, linked threads (docket pg2-2j5ac.40's
// Slack-half sibling packet "pg-desk run thread") ---------------------------

// TestGather_LinkedThreads_PopulatesFactsWithoutAnyPgConnectorCall is this
// input's own primary acceptance criterion: every thread already
// cross-referenced to the triggering PR in the store (Store.ListXrefsByFrom)
// is populated into Facts.LinkedThreads, and this is a pure store read —
// the recorded pg-connector call count is unchanged from the plain
// six-input happy path (no pg-connector-thread-slack call of gather's own).
func TestGather_LinkedThreads_PopulatesFactsWithoutAnyPgConnectorCall(t *testing.T) {
	recordFile := withFactory(t, "happy")
	xrefs := &fakeXrefUpserter{
		linkedThreads: []store.Xref{
			{Repo: fixtureRepo, FromType: "pr", FromID: "PR1", ToType: "thread", ToID: "T1", Evidence: "permalink"},
			{Repo: fixtureRepo, FromType: "pr", FromID: "PR1", ToType: "thread", ToID: "T2", Evidence: "ticket-key"},
		},
	}
	g := NewGatherer(testConfig("/configured/beads"), xrefs)

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if facts.Degraded != "" {
		t.Fatalf("Degraded = %q, want empty", facts.Degraded)
	}
	if len(facts.LinkedThreads) != 2 {
		t.Fatalf("LinkedThreads = %+v, want exactly 2 entries", facts.LinkedThreads)
	}
	gotIDs := map[string]bool{}
	for _, tr := range facts.LinkedThreads {
		gotIDs[tr.ID] = true
	}
	for _, want := range []string{"T1", "T2"} {
		if !gotIDs[want] {
			t.Fatalf("LinkedThreads missing id %q; got %+v", want, facts.LinkedThreads)
		}
	}

	// Still exactly the six pg-connector calls the happy path always makes
	// (see TestGather_SixInputs_CalledWithRightArgsAndEnv) — the eighth
	// input never invokes pg-connector at all.
	if got := len(readCallRecords(t, recordFile)); got != 6 {
		t.Fatalf("recorded %d pg-connector call(s), want exactly 6 (the eighth input makes none of its own)", got)
	}
}

// TestGather_LinkedThreads_NoXrefStore_SkipsSilently proves the nil-xrefs
// safety net (NewGatherer's own doc comment): a nil xrefs (the common
// pre-Phase-13 caller shape) leaves LinkedThreads empty and never
// degrades — mirrors gatherJiraXrefs' own identical nil-check convention.
func TestGather_LinkedThreads_NoXrefStore_SkipsSilently(t *testing.T) {
	withFactory(t, "happy")
	g := NewGatherer(testConfig("/configured/beads"), nil)

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if facts.Degraded != "" {
		t.Fatalf("Degraded = %q, want empty", facts.Degraded)
	}
	if len(facts.LinkedThreads) != 0 {
		t.Fatalf("LinkedThreads = %+v, want empty with a nil xref store", facts.LinkedThreads)
	}
}

// TestGather_LinkedThreads_ReadFailureDegrades proves a genuine store-read
// failure degrades this run exactly like every other non-triggering-entity
// input, naming "linked threads".
func TestGather_LinkedThreads_ReadFailureDegrades(t *testing.T) {
	withFactory(t, "happy")
	xrefs := &fakeXrefUpserter{listErr: errors.New("store: connection lost")}
	g := NewGatherer(testConfig("/configured/beads"), xrefs)

	facts, err := g.Gather(context.Background(), "pr", "PR1", ChangeAdded)
	if err != nil {
		t.Fatalf("Gather returned an error for a non-critical degraded input: %v", err)
	}
	if facts.Degraded != "linked threads" {
		t.Fatalf("Degraded = %q, want %q", facts.Degraded, "linked threads")
	}
}
