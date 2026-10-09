package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// sync_test.go is this packet's wire-double test harness: a reentrant
// test-helper process impersonates `pg-connector issue`, mirroring
// internal/gather/gather_test.go's own testmain-style pattern exactly (this
// packet's own Files instruction: "Tests against an in-process
// pg-connector wire double reusing pkg/scriptout/conformance").

// --- reentrant helper-process plumbing -------------------------------------

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

const ambientBeadsDir = "ambient-beads-dir"

func helperCmdFactory(recordFile string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(
			os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"GO_HELPER_CALLS_RECORD_FILE="+recordFile,
			"BEADS_DIR="+ambientBeadsDir,
		)
		return cmd
	}
}

func withFactory(t *testing.T) string {
	t.Helper()
	recordFile := filepath.Join(t.TempDir(), "calls.jsonl")
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory(recordFile)
	t.Cleanup(func() { execCmdFactory = orig })
	return recordFile
}

// callRecord is one recorded pg-connector invocation: its args, plus the
// two beads-workspace env vars this helper observed (proving the
// PG_CONNECTOR_ISSUE_BEADS_DIR fallback rule).
type callRecord struct {
	Args                []string `json:"args"`
	PGConnectorBeadsDir string   `json:"pg_connector_issue_beads_dir"`
	BeadsDir            string   `json:"beads_dir"`
}

const unsetSentinel = "<unset>"

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

func (r callRecord) verb() string {
	if len(r.Args) < 2 {
		return strings.Join(r.Args, " ")
	}
	return r.Args[0] + " " + r.Args[1]
}

// helperMain answers every `issue create|update|transition` call with a
// well-formed wire-envelope success response. create's id is deterministic
// — a counter over how many lines the calls-record file already held
// before this call — so tests can predict ids without a shared side
// channel beyond the same record file they already read.
func helperMain() {
	args := findChildArgs()
	recordCall(args)

	if len(args) < 2 || args[0] != "issue" {
		os.Stderr.WriteString("unexpected pg-connector invocation: " + strings.Join(args, " "))
		os.Exit(99)
	}

	switch args[1] {
	case "create":
		// GO_HELPER_REQUIRE_DIR impersonates the beads backend pinned to a
		// workspace that is not there (an unmounted volume): create fails the
		// way the real backend does, with wire code "unavailable" and bd's
		// chdir error, until that directory exists (bead pg2-xb6fs).
		if dir := os.Getenv("GO_HELPER_REQUIRE_DIR"); dir != "" {
			if _, err := os.Stat(dir); err != nil {
				writeWireError("unavailable", "pg-connector-issue-beads: chdir "+dir+": no such file or directory")
			}
		}
		// GO_HELPER_FAIL_CODE makes create fail with that wire error code.
		if code := os.Getenv("GO_HELPER_FAIL_CODE"); code != "" {
			writeWireError(code, "injected "+code+" failure")
		}
		n := priorCallCount()
		id := fmt.Sprintf("bd-created-%d", n)
		writeIssueResult(id)
	case "show":
		// GO_HELPER_SHOW_METADATA is the JSON metadata object `issue show`
		// reports (closed-anchor audit, pg2-a6aw6).
		md := os.Getenv("GO_HELPER_SHOW_METADATA")
		if md == "" {
			md = "{}"
		}
		os.Stdout.WriteString(fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":6,"result":{"id":%q,"state":"closed","metadata":%s}}`, args[2], md))
		os.Exit(0)
	case "update", "transition":
		id := ""
		if len(args) > 2 {
			id = args[2]
		}
		// GO_HELPER_FAIL_ID makes any call naming that bead id fail (exit 1,
		// stderr) — the closure-failure injection point (pg2-kftf9.1).
		if failID := os.Getenv("GO_HELPER_FAIL_ID"); failID != "" && failID == id {
			os.Stderr.WriteString("boom: injected failure for " + id)
			os.Exit(1)
		}
		// GO_HELPER_PARENT_OF impersonates bd >= 1.3.1, which refuses to
		// close a parent while any child is still open ("cannot close X: N
		// open child issue(s); close children first"). It is a comma list of
		// child:parent edges. Closing a bead fails unless EVERY descendant
		// of it (children, grandchildren, ...) already has an earlier
		// recorded `issue transition <id> closed` call.
		if edges := os.Getenv("GO_HELPER_PARENT_OF"); edges != "" && args[1] == "transition" {
			if open := openDescendantsAtThisCall(id, strings.Split(edges, ",")); open > 0 {
				writeWireError("unavailable", fmt.Sprintf("cannot close %s: %d open child issue(s); close children first or use --force to override", id, open))
			}
		}
		writeIssueResult(id)
	default:
		os.Stderr.WriteString("unexpected issue verb: " + args[1])
		os.Exit(99)
	}
}

// openDescendantsAtThisCall counts how many descendants of id (reached through
// the child:parent edges, transitively and cycle-safe) have NO earlier recorded
// `issue transition <descendant> closed` call. The record file already holds
// this invocation's own line (recordCall ran first), so only earlier lines
// count.
func openDescendantsAtThisCall(id string, edges []string) int {
	b, _ := os.ReadFile(os.Getenv("GO_HELPER_CALLS_RECORD_FILE"))
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	closed := map[string]bool{}
	for _, line := range lines {
		var rec callRecord
		if json.Unmarshal([]byte(line), &rec) != nil || len(rec.Args) < 3 {
			continue
		}
		if rec.verb() == "issue transition" && strings.Contains(strings.Join(rec.Args, " "), "closed") {
			closed[rec.Args[2]] = true
		}
	}
	childrenOf := map[string][]string{}
	for _, e := range edges {
		if child, parent, ok := strings.Cut(e, ":"); ok {
			childrenOf[parent] = append(childrenOf[parent], child)
		}
	}
	open := 0
	seen := map[string]bool{id: true}
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range childrenOf[cur] {
			if seen[child] {
				continue
			}
			seen[child] = true
			if !closed[child] {
				open++
			}
			queue = append(queue, child)
		}
	}
	return open
}

// priorCallCount counts how many lines the calls-record file held BEFORE
// this invocation's own recordCall already appended one — i.e. this call's
// own 1-based ordinal.
func priorCallCount() int {
	file := os.Getenv("GO_HELPER_CALLS_RECORD_FILE")
	b, err := os.ReadFile(file)
	if err != nil {
		return 1
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return len(lines)
}

// writeWireError answers with pg-connector's targeted-op failure shape: an
// error envelope on stdout and exit 1.
func writeWireError(code, message string) {
	os.Stdout.WriteString(fmt.Sprintf(`{"protocolVersion":1,"error":{"code":%q,"message":%q}}`, code, message))
	os.Exit(1)
}

func writeIssueResult(id string) {
	env := fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":5,"result":{"id":%q,"state":"open"}}`, id)
	os.Stdout.WriteString(env)
	os.Exit(0)
}

// --- fixtures ----------------------------------------------------------------

const (
	fixtureRepo    = "myorg/repo"
	fixtureNumber  = 42
	fixturePRKey   = "myorg/repo#42"
	fixtureEntity  = "myorg/repo#42"
	fixtureHeadSHA = "sha-AAA"
)

// prFixture builds a minimal `pr show`-shaped PRShow payload.
func prFixture(overrides map[string]any) json.RawMessage {
	base := map[string]any{
		"repo": fixtureRepo, "number": fixtureNumber, "title": "fix the bug",
		"state": "open", "branch": "feature", "base": "main", "author": "alice",
		"url": "https://example.test/pr/42", "draft": false, "merged": false,
	}
	for k, v := range overrides {
		base[k] = v
	}
	raw, err := json.Marshal(base)
	if err != nil {
		panic(err)
	}
	return raw
}

// workBeadsFixture builds a Facts.WorkBeads-shaped fan-out payload from a
// list of entities.
func workBeadsFixture(entities ...map[string]any) json.RawMessage {
	raw, err := json.Marshal(map[string]any{"entities": entities, "present_ids": []string{}, "sources": []any{}})
	if err != nil {
		panic(err)
	}
	return raw
}

func testConfig(mode string) *config.Config {
	return &config.Config{
		SelfLogin:           "alice",
		Repos:               []config.RepoConfig{{Remote: fixtureRepo, BeadsDir: "/configured/beads"}},
		AgentTrackerBackend: "beads",
		Sync:                config.SyncConfig{Mode: mode},
	}
}

var fixedClock = interpret.FixedClock(time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC))

func newTestSyncer(t *testing.T, mode string) *Syncer {
	t.Helper()
	st := store.OpenForTest(t)
	return New(testConfig(mode), st, WithClock(fixedClock))
}

// interpNoFeedbackTeamDraft: ownership=team, no unaddressed feedback — no
// review needed (draft team PR), no cycle needed.
func interpFor(ownership string, dispositions []interpret.Disposition) interpret.Interpretation {
	return interpret.Interpretation{Ownership: ownership, Dispositions: dispositions, AsOf: "2026-09-17T00:00:00Z"}
}

func openDisposition(id string) interpret.Disposition {
	return interpret.Disposition{CommentID: id, Verdict: interpret.DispositionOpen}
}

// --- tests: off / plan never write to pg-connector --------------------------

func TestSync_OffMode_NoConnectorNoLedger(t *testing.T) {
	recordFile := withFactory(t)
	s := newTestSyncer(t, ModeOff)

	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA}
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if recs := readCallRecords(t, recordFile); len(recs) != 0 {
		t.Fatalf("off mode invoked pg-connector: %+v", recs)
	}
	if _, found, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor); found {
		t.Fatal("off mode wrote an anchor ledger row, want none")
	}
}

func TestSync_PlanMode_NoConnectorWrite_ButLedgerPlanned(t *testing.T) {
	recordFile := withFactory(t)
	s := newTestSyncer(t, ModePlan)

	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA}
	interp := interpFor("mine", []interpret.Disposition{openDisposition("c1")})
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if recs := readCallRecords(t, recordFile); len(recs) != 0 {
		t.Fatalf("plan mode invoked pg-connector: %+v", recs)
	}

	anchor, found, err := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if err != nil || !found {
		t.Fatalf("expected a planned anchor ledger row: found=%v err=%v", found, err)
	}
	if anchor.BeadID != "" {
		t.Fatalf("plan mode anchor bead_id = %q, want empty (planned, not applied)", anchor.BeadID)
	}
	cycle, found, err := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindFeedbackCycle)
	if err != nil || !found || cycle.BeadID != "" {
		t.Fatalf("expected a planned feedback-cycle ledger row with empty bead_id: found=%v entry=%+v err=%v", found, cycle, err)
	}
}

// --- test: anchor created lazily, only after cycle/review need one ----------

func TestSync_Apply_AnchorNotCreatedWithoutCycleOrReview(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	withFactory(t)

	// team + draft: no review needed (D13/ACL); no unaddressed feedback.
	facts := gather.Facts{PRShow: prFixture(map[string]any{"author": "bob", "draft": true}), HeadSHA: fixtureHeadSHA}
	interp := interpFor("team", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, found, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor); found {
		t.Fatal("anchor was created eagerly with no cycle/review need")
	}
}

func TestSync_Apply_AnchorCreatedOnceReviewNeeded(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)

	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA} // author=alice=self => mine
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	anchor, found, err := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if err != nil || !found || anchor.BeadID == "" {
		t.Fatalf("expected a real anchor bead id: found=%v entry=%+v err=%v", found, anchor, err)
	}
	review, found, err := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if err != nil || !found || review.BeadID == "" {
		t.Fatalf("expected a real review-request bead id: found=%v entry=%+v err=%v", found, review, err)
	}

	var sawCreateAnchor, sawCreateReview bool
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() != "issue create" {
			continue
		}
		joined := strings.Join(r.Args, " ")
		if strings.Contains(joined, "merge-request") {
			sawCreateAnchor = true
		}
		if strings.Contains(joined, "review-pr:") {
			sawCreateReview = true
		}
	}
	if !sawCreateAnchor || !sawCreateReview {
		t.Fatalf("expected both anchor and review-request create calls; records: %+v", readCallRecords(t, recordFile))
	}
}

// --- test: plan/apply parity -------------------------------------------------

func TestSync_PlanApplyParity(t *testing.T) {
	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA}
	interp := interpFor("mine", []interpret.Disposition{openDisposition("c1"), openDisposition("c2")})

	planSyncer := newTestSyncer(t, ModePlan)
	withFactory(t)
	if err := planSyncer.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("plan Sync: %v", err)
	}

	applySyncer := newTestSyncer(t, ModeApply)
	withFactory(t)
	if err := applySyncer.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("apply Sync: %v", err)
	}

	for _, kind := range []string{KindAnchor, KindFeedbackCycle, KindReviewRequest} {
		planEntry, _, err := planSyncer.store.GetLedger(fixtureRepo, "pr", fixtureEntity, kind)
		if err != nil {
			t.Fatalf("plan GetLedger(%s): %v", kind, err)
		}
		applyEntry, _, err := applySyncer.store.GetLedger(fixtureRepo, "pr", fixtureEntity, kind)
		if err != nil {
			t.Fatalf("apply GetLedger(%s): %v", kind, err)
		}
		if planEntry.BeadID != "" {
			t.Fatalf("%s: plan mode bead_id = %q, want empty", kind, planEntry.BeadID)
		}
		if applyEntry.BeadID == "" {
			t.Fatalf("%s: apply mode bead_id is empty, want a real id", kind)
		}
		if planEntry.LastSyncedContentHash != applyEntry.LastSyncedContentHash {
			t.Fatalf("%s: content hash mismatch — plan=%q apply=%q (planned rows must match, kind for kind, what apply performs)",
				kind, planEntry.LastSyncedContentHash, applyEntry.LastSyncedContentHash)
		}
	}
}

// --- test: crash safety ------------------------------------------------------

func TestSync_CrashSafety_AdoptionResolvesBeforeCreate(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)

	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA}
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	anchor, found, err := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if err != nil || !found || anchor.BeadID == "" {
		t.Fatalf("expected a real anchor bead id after the first run: found=%v entry=%+v err=%v", found, anchor, err)
	}
	firstCreateCount := len(readCallRecords(t, recordFile))

	// Simulate "crash between issue create and the ledger write": a FRESH
	// store with no ledger knowledge at all, but facts.WorkBeads shows the
	// anchor bead as already existing (as it would moments after a real
	// create that crashed before the ledger write landed).
	freshStore := store.OpenForTest(t)
	freshSyncer := New(testConfig(ModeApply), freshStore, WithClock(fixedClock))

	adoptedFacts := gather.Facts{
		PRShow:  prFixture(nil),
		HeadSHA: fixtureHeadSHA,
		WorkBeads: workBeadsFixture(map[string]any{
			"id": anchor.BeadID, "title": fmt.Sprintf("%s: fix the bug", fixturePRKey),
			"state": "open", "priority": "P2", "labels": []string{},
			"metadata": map[string]string{"repo": fixtureRepo, "pr_number": "42"},
		}),
	}
	if err := freshSyncer.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, adoptedFacts, interp); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	all := readCallRecords(t, recordFile)
	for _, r := range all[firstCreateCount:] {
		joined := strings.Join(r.Args, " ")
		if r.verb() == "issue create" && strings.Contains(joined, "merge-request") {
			t.Fatalf("second run re-created the anchor after a simulated crash: %+v", r)
		}
	}
	adoptedAnchor, found, err := freshStore.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if err != nil || !found || adoptedAnchor.BeadID != anchor.BeadID {
		t.Fatalf("expected the fresh store to adopt the same anchor id %q: found=%v entry=%+v err=%v", anchor.BeadID, found, adoptedAnchor, err)
	}
}

// --- test: adoption of pre-existing beads creates nothing --------------------

func TestSync_Adoption_PreExistingBeadsCreateNothing(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)

	facts := gather.Facts{
		PRShow:  prFixture(nil),
		HeadSHA: fixtureHeadSHA,
		WorkBeads: workBeadsFixture(
			map[string]any{
				"id": "bd-anchor-1", "title": fixturePRKey + ": fix the bug", "state": "open",
				"priority": "P2", "labels": []string{},
				"metadata": map[string]string{"repo": fixtureRepo, "pr_number": "42"},
			},
			map[string]any{
				"id": "bd-cycle-1", "title": "process-feedback: " + fixturePRKey, "state": "open",
				"labels": []string{"mine"}, "metadata": map[string]string{},
			},
			map[string]any{
				"id": "bd-review-1", "title": "review-pr: " + fixturePRKey, "state": "open",
				"labels": []string{}, "metadata": map[string]string{"repo": fixtureRepo, "pr_number": "42", "head_sha": fixtureHeadSHA},
				"updated_at": "2026-09-16T00:00:00Z",
			},
		),
	}
	interp := interpFor("mine", []interpret.Disposition{openDisposition("c1")})
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() == "issue create" {
			t.Fatalf("adoption of pre-existing beads still created one: %+v", r)
		}
	}
	for kind, wantID := range map[string]string{KindAnchor: "bd-anchor-1", KindFeedbackCycle: "bd-cycle-1", KindReviewRequest: "bd-review-1"} {
		entry, found, err := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, kind)
		if err != nil || !found || entry.BeadID != wantID {
			t.Fatalf("%s: expected adopted bead_id %q: found=%v entry=%+v err=%v", kind, wantID, found, entry, err)
		}
	}
	_ = recordFile
}

// --- test: title-keyed adoption + metadata backfill on first apply ----------

func TestSync_Adoption_TitleKeyedAnchor_BackfillsMetadataOnApplyOnly(t *testing.T) {
	titleKeyedAnchor := map[string]any{
		"id": "bd-anchor-legacy", "title": fixturePRKey + ": fix the bug", "state": "open",
		"priority": "P2", "labels": []string{}, "metadata": map[string]string{},
	}
	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA, WorkBeads: workBeadsFixture(titleKeyedAnchor)}
	interp := interpFor("mine", nil)

	// Plan mode: adopted, but no backfill call (plan never calls connector).
	planSyncer := newTestSyncer(t, ModePlan)
	planRecordFile := withFactory(t)
	if err := planSyncer.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("plan Sync: %v", err)
	}
	if recs := readCallRecords(t, planRecordFile); len(recs) != 0 {
		t.Fatalf("plan mode invoked pg-connector during adoption backfill: %+v", recs)
	}
	anchor, found, err := planSyncer.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if err != nil || !found || anchor.BeadID != "bd-anchor-legacy" {
		t.Fatalf("plan mode did not adopt the title-keyed anchor: found=%v entry=%+v err=%v", found, anchor, err)
	}

	// Apply mode: adopted AND backfilled, never duplicated.
	applySyncer := newTestSyncer(t, ModeApply)
	applyRecordFile := withFactory(t)
	if err := applySyncer.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("apply Sync: %v", err)
	}
	var sawCreateAnchor, sawBackfillUpdate bool
	for _, r := range readCallRecords(t, applyRecordFile) {
		joined := strings.Join(r.Args, " ")
		// needsReview (ownership=mine, no adoption match for review-request
		// in this fixture) legitimately creates a review-pr bead here too —
		// only an ANCHOR ("merge-request") create would be the duplicate
		// this test guards against.
		if r.verb() == "issue create" && strings.Contains(joined, "merge-request") {
			sawCreateAnchor = true
		}
		if r.verb() == "issue update" && strings.Contains(joined, "bd-anchor-legacy") &&
			strings.Contains(joined, "repo="+fixtureRepo) && strings.Contains(joined, "pr_number=42") {
			sawBackfillUpdate = true
		}
	}
	if sawCreateAnchor {
		t.Fatal("apply mode created a duplicate anchor for a title-keyed bead, want adoption only")
	}
	if !sawBackfillUpdate {
		t.Fatalf("apply mode did not backfill repo/pr_number metadata onto the title-keyed anchor; records: %+v", readCallRecords(t, applyRecordFile))
	}
	applyAnchor, found, err := applySyncer.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if err != nil || !found || applyAnchor.BeadID != "bd-anchor-legacy" {
		t.Fatalf("apply mode did not adopt the title-keyed anchor: found=%v entry=%+v err=%v", found, applyAnchor, err)
	}
}

// --- test: removed/open closes nothing; confirmed closure closes anchor+cycle

func seedExistingAnchorCycleAndReview(t *testing.T, s *Syncer) (anchorID, cycleID, reviewID string) {
	t.Helper()
	if err := s.store.UpsertLedger(store.LedgerEntry{
		Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindAnchor,
		BeadID: "bd-anchor-existing", LastSyncedContentHash: "somehash", LastSyncedAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed anchor ledger: %v", err)
	}
	if err := s.store.UpsertLedger(store.LedgerEntry{
		Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindFeedbackCycle,
		BeadID: "bd-cycle-existing", LastSyncedContentHash: "somehash", LastSyncedAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed cycle ledger: %v", err)
	}
	if err := s.store.UpsertLedger(store.LedgerEntry{
		Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindReviewRequest,
		BeadID: "bd-review-existing", LastSyncedContentHash: "somehash", LastSyncedAt: "2026-09-16T00:00:00Z",
		LastReviewedHeadSHA: fixtureHeadSHA,
	}); err != nil {
		t.Fatalf("seed review ledger: %v", err)
	}
	return "bd-anchor-existing", "bd-cycle-existing", "bd-review-existing"
}

func TestSync_Removed_OpenClosesNothing(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	seedExistingAnchorCycleAndReview(t, s)

	facts := gather.Facts{PRShow: prFixture(map[string]any{"state": "open"}), HeadSHA: fixtureHeadSHA, RemovedState: "open"}
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() == "issue transition" && strings.Contains(strings.Join(r.Args, " "), "closed") {
			t.Fatalf("a removed/open re-read closed something: %+v", r)
		}
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if anchor.LastSyncedContentHash == closedSentinel {
		t.Fatal("anchor was marked closed on a removed/open re-read")
	}
}

// TestSync_ConfirmedClosure_ClosesAnchorAndBothCycleTypes guards pg2-ryexi:
// a confirmed closure MUST cascade-close BOTH cycle types symmetrically —
// the process-feedback cycle AND the review-pr request — not just the
// feedback cycle. Before the pg2-ryexi fix, review-pr beads were never
// touched on this path and only ever caught by the slower sweep
// re-verification, which is exactly the ~8x stale-rate asymmetry that bead
// measured live.
func TestSync_ConfirmedClosure_ClosesAnchorAndBothCycleTypes(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)

	facts := gather.Facts{HeadSHA: fixtureHeadSHA, RemovedState: "merged"}
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	var closedAnchor, closedCycle, closedReview bool
	for _, r := range readCallRecords(t, recordFile) {
		joined := strings.Join(r.Args, " ")
		if r.verb() == "issue transition" && strings.Contains(joined, anchorID) && strings.Contains(joined, "closed") {
			closedAnchor = true
		}
		if r.verb() == "issue transition" && strings.Contains(joined, cycleID) && strings.Contains(joined, "closed") {
			closedCycle = true
		}
		if r.verb() == "issue transition" && strings.Contains(joined, reviewID) && strings.Contains(joined, "closed") {
			closedReview = true
		}
	}
	if !closedAnchor || !closedCycle || !closedReview {
		t.Fatalf("confirmed closure did not close anchor, cycle, and review request; records: %+v", readCallRecords(t, recordFile))
	}

	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if anchor.LastSyncedContentHash != closedSentinel {
		t.Fatalf("anchor ledger row not marked closed: %+v", anchor)
	}
	cycle, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindFeedbackCycle)
	if cycle.LastSyncedContentHash != closedSentinel {
		t.Fatalf("feedback-cycle ledger row not marked closed: %+v", cycle)
	}
	review, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if review.LastSyncedContentHash != closedSentinel {
		t.Fatalf("review-request ledger row not marked closed: %+v", review)
	}

	// Re-run: idempotent, no further transition calls.
	before := len(readCallRecords(t, recordFile))
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interp); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if after := len(readCallRecords(t, recordFile)); after != before {
		t.Fatalf("re-closing an already-closed anchor issued %d more call(s), want 0", after-before)
	}
}

// TestSync_ConfirmedClosure_ClosesEveryOpenChildOfAnchor guards pg2-kftf9.7:
// the cascade is type-blind — every open direct child of the anchor closes,
// not just the ledger-tracked cycle/review beads (e.g. an improvised
// "Human: unblock ..." bead). Closed children, children of other parents,
// and parentless beads are left alone; a child that is also the ledger
// cycle is transitioned exactly once.
func TestSync_ConfirmedClosure_ClosesEveryOpenChildOfAnchor(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, _ := seedExistingAnchorCycleAndReview(t, s)

	facts := gather.Facts{
		HeadSHA:      fixtureHeadSHA,
		RemovedState: "merged",
		WorkBeads: workBeadsFixture(
			map[string]any{"id": cycleID, "title": "process-feedback: " + fixturePRKey, "state": "open", "parent": anchorID},
			map[string]any{"id": "bd-human-1", "title": "Human: unblock stuck pending review on PR #42", "state": "open", "parent": anchorID},
			map[string]any{"id": "bd-human-2", "title": "something else", "state": "in_progress", "parent": anchorID},
			map[string]any{"id": "bd-already-closed", "title": "old", "state": "closed", "parent": anchorID},
			map[string]any{"id": "bd-other-child", "title": "other pr's child", "state": "open", "parent": "bd-some-other-anchor"},
			map[string]any{"id": "bd-orphan", "title": "no parent", "state": "open"},
		),
	}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	closed := map[string]int{}
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() == "issue transition" && strings.Contains(strings.Join(r.Args, " "), "closed") {
			closed[r.Args[2]]++
		}
	}
	for _, id := range []string{anchorID, cycleID, "bd-human-1", "bd-human-2"} {
		if closed[id] != 1 {
			t.Errorf("%s closed %d times, want 1; all: %v", id, closed[id], closed)
		}
	}
	for _, id := range []string{"bd-already-closed", "bd-other-child", "bd-orphan"} {
		if closed[id] != 0 {
			t.Errorf("%s must not be closed, was closed %d times", id, closed[id])
		}
	}
}

// TestSync_ConfirmedClosure_ClosesChildrenBeforeAnchor guards pg2-mhz7b: bd
// 1.3.1 (the 2026-10-08 upgrade) refuses to close a parent bead while any
// child is still open, so the anchor MUST be closed AFTER every child (both
// ledger-tracked cycles and the type-blind open children). Closing the anchor
// first failed every confirmed closure, left the anchor's ledger last_synced_at
// frozen (pg_desk_oldest_anchor_check_age_seconds grew to 37318s) and
// re-stamped closed_at on the anchor bead on every retry.
func TestSync_ConfirmedClosure_ClosesChildrenBeforeAnchor(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)
	t.Setenv("GO_HELPER_PARENT_OF", strings.Join([]string{cycleID + ":" + anchorID, reviewID + ":" + anchorID, "bd-human-1:" + anchorID}, ","))

	facts := gather.Facts{
		HeadSHA:      fixtureHeadSHA,
		RemovedState: "merged",
		WorkBeads: workBeadsFixture(
			map[string]any{"id": "bd-human-1", "title": "Human: unblock stuck pending review on PR #42", "state": "open", "parent": anchorID},
		),
	}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	var order []string
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() == "issue transition" && strings.Contains(strings.Join(r.Args, " "), "closed") {
			order = append(order, r.Args[2])
		}
	}
	if len(order) != 4 || order[len(order)-1] != anchorID {
		t.Fatalf("close order = %v, want the anchor %s closed last, after cycle, review and open child", order, anchorID)
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if anchor.LastSyncedContentHash != closedSentinel {
		t.Fatalf("anchor ledger row not marked closed: %+v", anchor)
	}
}

// closedTransitions lists, in call order, the bead ids a recorded run asked
// to close.
func closedTransitions(t *testing.T, recordFile string) []string {
	t.Helper()
	var order []string
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() == "issue transition" && strings.Contains(strings.Join(r.Args, " "), "closed") {
			order = append(order, r.Args[2])
		}
	}
	return order
}

// TestSync_ConfirmedClosure_ClosesDescendantsDepthFirst guards that bd
// 1.3.1 refuses to close a bead with ANY open child, so a cycle bead (or any
// other child of the anchor) that has open children of its own can only close
// after them. Every open descendant closes before its parent, depth first, and
// the anchor closes last.
func TestSync_ConfirmedClosure_ClosesDescendantsDepthFirst(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)
	t.Setenv("GO_HELPER_PARENT_OF", strings.Join([]string{
		cycleID + ":" + anchorID, reviewID + ":" + anchorID, "bd-human-1:" + anchorID,
		"bd-cycle-kid:" + cycleID, "bd-review-kid:" + reviewID,
		"bd-human-kid:bd-human-1", "bd-human-grandkid:bd-human-kid",
	}, ","))

	facts := gather.Facts{
		HeadSHA:      fixtureHeadSHA,
		RemovedState: "merged",
		WorkBeads: workBeadsFixture(
			map[string]any{"id": cycleID, "title": "process-feedback: " + fixturePRKey, "state": "open", "parent": anchorID},
			map[string]any{"id": "bd-cycle-kid", "title": "kid", "state": "open", "parent": cycleID},
			map[string]any{"id": "bd-review-kid", "title": "kid", "state": "in_progress", "parent": reviewID},
			map[string]any{"id": "bd-human-1", "title": "Human: unblock stuck pending review on PR #42", "state": "open", "parent": anchorID},
			map[string]any{"id": "bd-human-kid", "title": "kid", "state": "open", "parent": "bd-human-1"},
			map[string]any{"id": "bd-human-grandkid", "title": "grandkid", "state": "open", "parent": "bd-human-kid"},
			map[string]any{"id": "bd-closed-kid", "title": "kid", "state": "closed", "parent": "bd-human-1"},
		),
	}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	order := closedTransitions(t, recordFile)
	if len(order) != 8 || order[len(order)-1] != anchorID {
		t.Fatalf("close order = %v, want 8 closes with the anchor %s last", order, anchorID)
	}
	at := map[string]int{}
	for i, id := range order {
		if _, dup := at[id]; dup {
			t.Fatalf("%s closed more than once: %v", id, order)
		}
		at[id] = i
	}
	for _, edge := range [][2]string{
		{"bd-cycle-kid", cycleID},
		{"bd-review-kid", reviewID},
		{"bd-human-grandkid", "bd-human-kid"},
		{"bd-human-kid", "bd-human-1"},
	} {
		if at[edge[0]] > at[edge[1]] {
			t.Errorf("%s must close before its parent %s: %v", edge[0], edge[1], order)
		}
	}
	if _, closed := at["bd-closed-kid"]; closed {
		t.Errorf("an already-closed descendant must be left alone: %v", order)
	}
}

// TestSync_ConfirmedClosure_DescendantFailureLeavesAnchorOpen guards that
// a failing grandchild close fails the run and leaves the anchor untouched
// (not stamped, not closed, ledger row open), so the retry finishes the job
// without re-stamping the anchor per failed attempt.
func TestSync_ConfirmedClosure_DescendantFailureLeavesAnchorOpen(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, _ := seedExistingAnchorCycleAndReview(t, s)
	facts := gather.Facts{
		HeadSHA:      fixtureHeadSHA,
		RemovedState: "merged",
		WorkBeads: workBeadsFixture(
			map[string]any{"id": cycleID, "title": "process-feedback: " + fixturePRKey, "state": "open", "parent": anchorID},
			map[string]any{"id": "bd-cycle-kid", "title": "kid", "state": "open", "parent": cycleID},
		),
	}
	interp := interpFor("mine", nil)

	t.Setenv("GO_HELPER_FAIL_ID", "bd-cycle-kid")
	err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interp)
	if err == nil || !strings.Contains(err.Error(), "bd-cycle-kid") {
		t.Fatalf("Sync error = %v, want one naming bd-cycle-kid", err)
	}
	for _, r := range readCallRecords(t, recordFile) {
		if len(r.Args) > 2 && r.Args[2] == anchorID {
			t.Fatalf("anchor was written despite a failed descendant close: %v", r.Args)
		}
	}
	if anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor); anchor.LastSyncedContentHash == closedSentinel {
		t.Fatal("anchor ledger row marked closed although a descendant's close failed")
	}

	t.Setenv("GO_HELPER_FAIL_ID", "")
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interp); err != nil {
		t.Fatalf("retry Sync: %v", err)
	}
	if order := closedTransitions(t, recordFile); len(order) < 1 || order[len(order)-1] != anchorID {
		t.Fatalf("retry close order = %v, want the anchor %s last", order, anchorID)
	}
}

// TestSync_ConfirmedClosure_MalformedGraphTerminates guards that the
// work-beads read is not trusted to be a tree. A parent loop through the
// anchor and a bead listed twice are each closed at most once and the walk
// ends; a chain nested past the depth bound fails the run with an error
// naming the bound instead of walking on.
func TestSync_ConfirmedClosure_MalformedGraphTerminates(t *testing.T) {
	t.Run("loop-and-duplicate", func(t *testing.T) {
		s := newTestSyncer(t, ModeApply)
		recordFile := withFactory(t)
		anchorID, cycleID, _ := seedExistingAnchorCycleAndReview(t, s)
		facts := gather.Facts{
			HeadSHA:      fixtureHeadSHA,
			RemovedState: "merged",
			WorkBeads: workBeadsFixture(
				map[string]any{"id": cycleID, "title": "process-feedback: " + fixturePRKey, "state": "open", "parent": anchorID},
				map[string]any{"id": "bd-loop-a", "title": "a", "state": "open", "parent": anchorID},
				map[string]any{"id": "bd-loop-b", "title": "b", "state": "open", "parent": "bd-loop-a"},
				map[string]any{"id": "bd-loop-b", "title": "b again", "state": "open", "parent": "bd-loop-a"},
				map[string]any{"id": anchorID, "title": "anchor", "state": "open", "parent": "bd-loop-b"},
			),
		}
		if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interpFor("mine", nil)); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		closed := map[string]int{}
		for _, id := range closedTransitions(t, recordFile) {
			closed[id]++
		}
		for _, id := range []string{anchorID, cycleID, "bd-loop-a", "bd-loop-b"} {
			if closed[id] != 1 {
				t.Errorf("%s closed %d times, want 1; all: %v", id, closed[id], closed)
			}
		}
	})

	t.Run("too-deep", func(t *testing.T) {
		s := newTestSyncer(t, ModeApply)
		recordFile := withFactory(t)
		anchorID, _, _ := seedExistingAnchorCycleAndReview(t, s)
		var chain []map[string]any
		parent := anchorID
		for i := 0; i < maxClosureDepth+2; i++ {
			id := fmt.Sprintf("bd-deep-%d", i)
			chain = append(chain, map[string]any{"id": id, "title": "deep", "state": "open", "parent": parent})
			parent = id
		}
		facts := gather.Facts{HeadSHA: fixtureHeadSHA, RemovedState: "merged", WorkBeads: workBeadsFixture(chain...)}
		err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interpFor("mine", nil))
		if err == nil || !strings.Contains(err.Error(), "deeper than") {
			t.Fatalf("Sync error = %v, want a nesting-depth error", err)
		}
		for _, id := range closedTransitions(t, recordFile) {
			if id == anchorID {
				t.Fatal("anchor closed despite an unwalked, too deeply nested descendant")
			}
		}
	})
}

// --- test: review-request ACL matches the fixture set -----------------------

func TestSync_ReviewRequestACL(t *testing.T) {
	cases := []struct {
		name       string
		ownership  string
		draft      bool
		wantReview bool
	}{
		{"mine-not-draft", "mine", false, true},
		{"mine-draft", "mine", true, true},
		{"co-owned-draft", "co-owned", true, true},
		{"co-owned-not-draft", "co-owned", false, true},
		{"team-not-draft", "team", false, true},
		{"team-draft", "team", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSyncer(t, ModePlan)
			withFactory(t)
			facts := gather.Facts{PRShow: prFixture(map[string]any{"draft": tc.draft}), HeadSHA: fixtureHeadSHA}
			interp := interpFor(tc.ownership, nil)
			if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			_, found, err := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
			if err != nil {
				t.Fatalf("GetLedger: %v", err)
			}
			if found != tc.wantReview {
				t.Fatalf("ownership=%s draft=%v: review-request found=%v, want %v", tc.ownership, tc.draft, found, tc.wantReview)
			}
		})
	}
}

// --- test: review-request reopens on head advance ---------------------------

func TestSync_ReviewRequest_ReopensOnHeadAdvance(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	if err := s.store.UpsertLedger(store.LedgerEntry{
		Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindReviewRequest,
		BeadID: "bd-review-existing", LastSyncedContentHash: "old-hash", LastSyncedAt: "2026-09-16T00:00:00Z",
		LastReviewedHeadSHA: "old-sha",
		// new-sha was first seen a day before the clock: its settle window
		// (pg2-a9yhn) has long elapsed, so this run reopens.
		FirstSeenHeadSHA: "new-sha", FirstSeenHeadAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed review ledger: %v", err)
	}

	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: "new-sha"}
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeChanged, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// pg2-vhs3e: the reopen also clears the deferral (--clear-defer), which
	// the review worker set when it released a blocked review for the OLD
	// head; bd keeps defer_until across a reopen, so without it the new head
	// stays hidden from `bd ready` for up to the deferral (12h).
	// pg2-1pt7r: the reopen is ONE `issue update` that sets status open AND
	// clears the assignee AND refreshes head_sha — never a separate
	// `issue transition` (which cannot clear the assignee and would strand
	// the bead open + still claimed by the previous reviewer, so no worker
	// could claim the re-review; beads-lifecycle B-4).
	var reopenedAndRefreshed bool
	for _, r := range readCallRecords(t, recordFile) {
		joined := strings.Join(r.Args, " ")
		if r.verb() == "issue transition" && strings.Contains(joined, "bd-review-existing") {
			t.Fatalf("reopen must not use a separate transition (cannot clear the assignee); record: %+v", r)
		}
		if r.verb() == "issue update" && strings.Contains(joined, "bd-review-existing") &&
			strings.Contains(joined, "--status open") &&
			strings.Contains(joined, "--clear-assignee") &&
			strings.Contains(joined, "--clear-defer") &&
			strings.Contains(joined, "head_sha=new-sha") {
			reopenedAndRefreshed = true
		}
	}
	if !reopenedAndRefreshed {
		t.Fatalf("head advance did not reopen (status open + clear assignee) and refresh in ONE update; records: %+v", readCallRecords(t, recordFile))
	}

	review, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if review.LastReviewedHeadSHA != "new-sha" {
		t.Fatalf("ledger LastReviewedHeadSHA = %q, want %q", review.LastReviewedHeadSHA, "new-sha")
	}

	// Same head again: no-op for the review request itself (the anchor still
	// gets its last_checked_at stamp, pg2-kftf9.3, so count review calls only).
	countReview := func() int {
		n := 0
		for _, r := range readCallRecords(t, recordFile) {
			if strings.Contains(strings.Join(r.Args, " "), "bd-review-existing") {
				n++
			}
		}
		return n
	}
	before := countReview()
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeChanged, facts, interp); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if after := countReview(); after != before {
		t.Fatalf("unchanged head issued %d more review-request call(s), want 0", after-before)
	}
}

// --- test: every write carries PG_CONNECTOR_ISSUE_BEADS_DIR ------------------

func TestSync_EveryIssueWriteCarriesBeadsDirEnv(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)

	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA}
	interp := interpFor("mine", []interpret.Disposition{openDisposition("c1")})
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	records := readCallRecords(t, recordFile)
	if len(records) == 0 {
		t.Fatal("expected at least one pg-connector issue call")
	}
	for _, r := range records {
		if r.PGConnectorBeadsDir != "/configured/beads" {
			t.Fatalf("%v: PG_CONNECTOR_ISSUE_BEADS_DIR = %q, want %q", r.Args, r.PGConnectorBeadsDir, "/configured/beads")
		}
	}
}

// TestSync_ConfirmedClosure_ConnectorErrorIsReturnedAndRetrySafe guards
// pg2-kftf9.1: a connector error during handleClosure MUST surface as a Sync
// error (the pipeline turns it into a non-zero `run` so pg-router retries),
// and a RETRY after a partial failure MUST finish the cascade without
// re-closing what already closed. Children close before the anchor (pg2-mhz7b):
// the cycle closes, then the review request's close fails, so the anchor is NOT
// yet closed; the retry must close the review request and then the anchor, and
// must not re-close the cycle.
func TestSync_ConfirmedClosure_ConnectorErrorIsReturnedAndRetrySafe(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, cycleID, reviewID := seedExistingAnchorCycleAndReview(t, s)

	facts := gather.Facts{HeadSHA: fixtureHeadSHA, RemovedState: "merged"}
	interp := interpFor("mine", nil)

	t.Setenv("GO_HELPER_FAIL_ID", reviewID)
	err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interp)
	if err == nil {
		t.Fatal("Sync: expected the injected closure failure to be returned, got nil")
	}
	if !strings.Contains(err.Error(), reviewID) {
		t.Fatalf("error should name the failing bead %q, got: %v", reviewID, err)
	}
	review, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if review.LastSyncedContentHash == closedSentinel {
		t.Fatal("review ledger row marked closed despite its close failing")
	}
	if anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor); anchor.LastSyncedContentHash == closedSentinel {
		t.Fatal("anchor ledger row marked closed although a child's close failed first")
	}

	// Retry with the connector healthy again.
	t.Setenv("GO_HELPER_FAIL_ID", "")
	before := len(readCallRecords(t, recordFile))
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interp); err != nil {
		t.Fatalf("retry Sync: %v", err)
	}
	var retryCloses []string
	for _, r := range readCallRecords(t, recordFile)[before:] {
		if r.verb() == "issue transition" {
			retryCloses = append(retryCloses, r.Args[2])
		}
	}
	if len(retryCloses) != 2 || retryCloses[0] != reviewID || retryCloses[1] != anchorID {
		t.Fatalf("retry closed %v, want [%s %s] (cycle %s already closed)", retryCloses, reviewID, anchorID, cycleID)
	}
	review, _, _ = s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if review.LastSyncedContentHash != closedSentinel {
		t.Fatalf("review ledger row not closed after retry: %+v", review)
	}
}

// argsHave reports whether a recorded call carries the exact "--metadata k=v".
func argsHave(r callRecord, kv string) bool {
	for i, a := range r.Args {
		if a == "--metadata" && i+1 < len(r.Args) && r.Args[i+1] == kv {
			return true
		}
	}
	return false
}

// TestSync_ConfirmedClosure_StampsTerminalStateBeforeClose guards
// pg2-kftf9.3: a closed anchor MUST carry state=merged|closed and closed_at,
// written BEFORE the close transition.
func TestSync_ConfirmedClosure_StampsTerminalStateBeforeClose(t *testing.T) {
	for _, tc := range []struct{ removed, wantState string }{
		{"merged", "merged"}, {"closed", "closed"}, {"not_found", "closed"},
	} {
		t.Run(tc.removed, func(t *testing.T) {
			s := newTestSyncer(t, ModeApply)
			recordFile := withFactory(t)
			anchorID, _, _ := seedExistingAnchorCycleAndReview(t, s)
			facts := gather.Facts{HeadSHA: fixtureHeadSHA, RemovedState: tc.removed}
			if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, facts, interpFor("mine", nil)); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			updateIdx, closeIdx := -1, -1
			for i, r := range readCallRecords(t, recordFile) {
				if len(r.Args) < 3 || r.Args[2] != anchorID {
					continue
				}
				if r.verb() == "issue update" && argsHave(r, "state="+tc.wantState) {
					updateIdx = i
					if !strings.Contains(strings.Join(r.Args, " "), "closed_at=") {
						t.Errorf("closing update lacks closed_at: %v", r.Args)
					}
				}
				if r.verb() == "issue transition" {
					closeIdx = i
				}
			}
			if updateIdx < 0 || closeIdx < 0 || updateIdx > closeIdx {
				t.Fatalf("want state=%s update before close; update=%d close=%d", tc.wantState, updateIdx, closeIdx)
			}
		})
	}
}

// anchorCalls returns the recorded issue update/transition calls aimed at
// beadID, in order.
func anchorCalls(t *testing.T, recordFile, beadID string, from int) []callRecord {
	t.Helper()
	var out []callRecord
	for _, r := range readCallRecords(t, recordFile)[from:] {
		if (r.verb() == "issue update" || r.verb() == "issue transition") && len(r.Args) > 2 && r.Args[2] == beadID {
			out = append(out, r)
		}
	}
	return out
}

// TestSync_Reconcile_UnchangedRecordsCheckInLedgerNotBead guards bead
// pg2-u4c1s: an unchanged-hash check MUST make no issue update/transition
// call on the anchor (any update bumps updated_at, which the issue changes
// feed hashes, echoing an issue.changed -> desk-issue run), and records the
// check time in the anchor's LEDGER row instead. This intentionally reverses
// what closed pg2-cl3ya verified ("quiet open PR advances last_checked_at
// each sync").
func TestSync_Reconcile_UnchangedRecordsCheckInLedgerNotBead(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA}
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	anchor1, found, err := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if err != nil || !found || anchor1.BeadID == "" {
		t.Fatalf("anchor ledger after run 1: found=%v entry=%+v err=%v", found, anchor1, err)
	}

	// Run 2: same store, identical content, clock an hour later.
	later := time.Date(2026, 9, 17, 1, 0, 0, 0, time.UTC)
	s2 := New(testConfig(ModeApply), s.store, WithClock(interpret.FixedClock(later)))
	before := len(readCallRecords(t, recordFile))
	if err := s2.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	if calls := anchorCalls(t, recordFile, anchor1.BeadID, before); len(calls) != 0 {
		t.Fatalf("unchanged re-check wrote the anchor bead: %+v", calls)
	}
	anchor2, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if want := later.Format(time.RFC3339); anchor2.LastSyncedAt != want {
		t.Fatalf("anchor ledger LastSyncedAt = %q, want the run-2 time %q", anchor2.LastSyncedAt, want)
	}
	if anchor2.LastSyncedContentHash != anchor1.LastSyncedContentHash || anchor2.BeadID != anchor1.BeadID {
		t.Fatalf("unchanged check altered the ledger row beyond its timestamp: %+v -> %+v", anchor1, anchor2)
	}
}

// TestSync_Reconcile_ChangedWritesBothStampsInOneUpdate guards bead
// pg2-u4c1s: when content really changes (draft flips), exactly one anchor
// update is written and it carries BOTH last_checked_at and last_synced_at.
func TestSync_Reconcile_ChangedWritesBothStampsInOneUpdate(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded,
		gather.Facts{PRShow: prFixture(map[string]any{"draft": true}), HeadSHA: fixtureHeadSHA}, interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)

	later := time.Date(2026, 9, 17, 1, 0, 0, 0, time.UTC)
	s2 := New(testConfig(ModeApply), s.store, WithClock(interpret.FixedClock(later)))
	before := len(readCallRecords(t, recordFile))
	if err := s2.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded,
		gather.Facts{PRShow: prFixture(map[string]any{"draft": false}), HeadSHA: fixtureHeadSHA}, interp); err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	calls := anchorCalls(t, recordFile, anchor.BeadID, before)
	if len(calls) != 1 || calls[0].verb() != "issue update" {
		t.Fatalf("want exactly one anchor update, got %+v", calls)
	}
	stamp := later.Format(time.RFC3339)
	for _, kv := range []string{"draft=false", "last_checked_at=" + stamp, "last_synced_at=" + stamp} {
		if !argsHave(calls[0], kv) {
			t.Errorf("anchor update lacks --metadata %s: %v", kv, calls[0].Args)
		}
	}
}

// TestSync_Reconcile_UnchangedPlanModeAdvancesLedgerOnly guards bead
// pg2-u4c1s for plan mode: an unchanged re-run calls pg-connector not at all
// and still advances the planned anchor row's timestamp.
func TestSync_Reconcile_UnchangedPlanModeAdvancesLedgerOnly(t *testing.T) {
	s := newTestSyncer(t, ModePlan)
	recordFile := withFactory(t)
	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA}
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	later := time.Date(2026, 9, 17, 1, 0, 0, 0, time.UTC)
	s2 := New(testConfig(ModePlan), s.store, WithClock(interpret.FixedClock(later)))
	if err := s2.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	if recs := readCallRecords(t, recordFile); len(recs) != 0 {
		t.Fatalf("plan mode called pg-connector: %+v", recs)
	}
	anchor, found, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)
	if !found || anchor.BeadID != "" {
		t.Fatalf("want a planned (empty bead_id) anchor row, got found=%v %+v", found, anchor)
	}
	if want := later.Format(time.RFC3339); anchor.LastSyncedAt != want {
		t.Fatalf("planned anchor LastSyncedAt = %q, want %q", anchor.LastSyncedAt, want)
	}
}

// TestSync_Reconcile_ExistingAnchorDraftDriftCorrected guards pg2-kftf9.3:
// the PR flips draft->ready while no cycle/review needs the anchor (team PR);
// the existing anchor's draft metadata MUST still be corrected.
func TestSync_Reconcile_ExistingAnchorDraftDriftCorrected(t *testing.T) {
	s := newTestSyncer(t, ModeApply)
	recordFile := withFactory(t)
	anchorID, _, _ := seedExistingAnchorCycleAndReview(t, s)
	facts := gather.Facts{PRShow: prFixture(map[string]any{"author": "bob", "draft": true}), HeadSHA: fixtureHeadSHA}
	// team + draft: needsReview=false, no cycle => anchorNeeded=false.
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interpFor("team", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	var sawDraft bool
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() == "issue update" && len(r.Args) > 2 && r.Args[2] == anchorID && argsHave(r, "draft=true") {
			sawDraft = true
		}
	}
	if !sawDraft {
		t.Fatalf("existing anchor's draft metadata not refreshed; records: %+v", readCallRecords(t, recordFile))
	}
}

// TestSync_AnchorStale_And_StampClosedAnchor covers bead pg2-a6aw6's sync
// half: a ledger-closed anchor whose bead metadata says state=open with no
// closed_at is reported stale and repaired with truthful terminal metadata;
// an already-truthful one is not stale.
func TestSync_AnchorStale_And_StampClosedAnchor(t *testing.T) {
	cases := []struct {
		name      string
		metadata  string
		wantStale bool
	}{
		{"open-no-closed-at", `{"state":"open"}`, true},
		{"merged-without-closed-at", `{"state":"merged"}`, true},
		{"truthful", `{"state":"merged","closed_at":"2026-09-01T00:00:00Z"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := withFactory(t)
			t.Setenv("GO_HELPER_SHOW_METADATA", tc.metadata)
			s := newTestSyncer(t, ModeApply)
			if err := s.store.UpsertLedger(store.LedgerEntry{
				Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindAnchor,
				BeadID: "bd-anchor", LastSyncedContentHash: closedSentinel,
			}); err != nil {
				t.Fatal(err)
			}
			stale, err := s.AnchorStale(context.Background(), fixtureRepo, fixtureEntity)
			if err != nil || stale != tc.wantStale {
				t.Fatalf("AnchorStale = %v, %v; want %v", stale, err, tc.wantStale)
			}
			if !stale {
				return
			}
			if err := s.StampClosedAnchor(context.Background(), fixtureRepo, fixtureEntity, "merged"); err != nil {
				t.Fatal(err)
			}
			calls := readCallRecords(t, rec)
			last := calls[len(calls)-1]
			if last.verb() != "issue update" || last.Args[2] != "bd-anchor" {
				t.Fatalf("last call = %v, want issue update bd-anchor", last.Args)
			}
			joined := strings.Join(last.Args, " ")
			for _, want := range []string{"state=merged", "draft=false", "closed_at=2026-09-17T00:00:00Z"} {
				if !strings.Contains(joined, want) {
					t.Errorf("update args %q missing %q", joined, want)
				}
			}
		})
	}
}
