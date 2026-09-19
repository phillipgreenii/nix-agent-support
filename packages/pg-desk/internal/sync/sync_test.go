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
		n := priorCallCount()
		id := fmt.Sprintf("bd-created-%d", n)
		writeIssueResult(id)
	case "update", "transition":
		id := ""
		if len(args) > 2 {
			id = args[2]
		}
		writeIssueResult(id)
	default:
		os.Stderr.WriteString("unexpected issue verb: " + args[1])
		os.Exit(99)
	}
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
	}); err != nil {
		t.Fatalf("seed review ledger: %v", err)
	}

	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: "new-sha"}
	interp := interpFor("mine", nil)
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeChanged, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	var reopened, refreshed bool
	for _, r := range readCallRecords(t, recordFile) {
		joined := strings.Join(r.Args, " ")
		if r.verb() == "issue transition" && strings.Contains(joined, "bd-review-existing") && strings.Contains(joined, "open") {
			reopened = true
		}
		if r.verb() == "issue update" && strings.Contains(joined, "bd-review-existing") && strings.Contains(joined, "head_sha=new-sha") {
			refreshed = true
		}
	}
	if !reopened || !refreshed {
		t.Fatalf("head advance did not reopen+refresh the review request; records: %+v", readCallRecords(t, recordFile))
	}

	review, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindReviewRequest)
	if review.LastReviewedHeadSHA != "new-sha" {
		t.Fatalf("ledger LastReviewedHeadSHA = %q, want %q", review.LastReviewedHeadSHA, "new-sha")
	}

	// Same head again: no-op.
	before := len(readCallRecords(t, recordFile))
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeChanged, facts, interp); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if after := len(readCallRecords(t, recordFile)); after != before {
		t.Fatalf("unchanged head issued %d more call(s), want 0", after-before)
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
