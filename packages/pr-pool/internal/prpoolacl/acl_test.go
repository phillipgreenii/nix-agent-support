package prpoolacl

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/beads"
)

// fakeBD scripts bd responses for the ACL. Responses are keyed by a coarse
// command key (keyFor); errSubstr forces an error when the joined args contain a
// substring (used to fail one specific PR's create for the partial-failure test).
type fakeBD struct {
	responses map[string]string
	errSubstr map[string]error
	calls     [][]string
}

func (f *fakeBD) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	for sub, e := range f.errSubstr {
		if strings.Contains(joined, sub) {
			return "", e
		}
	}
	return f.responses[keyFor(args)], nil
}

func keyFor(args []string) string {
	has := func(s string) bool {
		for _, a := range args {
			if a == s {
				return true
			}
		}
		return false
	}
	switch {
	case len(args) >= 2 && args[0] == "gate":
		return "gate " + args[1]
	case args[0] == "list" && has("--type=merge-request"):
		return "list merge-request"
	case args[0] == "list" && has("--type=task"):
		return "list task"
	case args[0] == "create":
		return "create"
	case args[0] == "dep":
		return "dep add"
	}
	return args[0]
}

func (f *fakeBD) countCalls(match ...string) int {
	n := 0
	for _, c := range f.calls {
		joined := strings.Join(c, " ")
		ok := true
		for _, m := range match {
			if !strings.Contains(joined, m) {
				ok = false
				break
			}
		}
		if ok {
			n++
		}
	}
	return n
}

var _ beads.Runner = (*fakeBD)(nil)

func mrJSON(id, repo string, num int, status string) string {
	return `{"id":"` + id + `","issue_type":"merge-request","status":"` + status +
		`","metadata":{"repo":"` + repo + `","pr_number":` + strconv.Itoa(num) + `}}`
}

func reviewJSON(id, repo string, num int) string {
	return `{"id":"` + id + `","issue_type":"task","title":"review-pr: ` + repo + "#" + strconv.Itoa(num) +
		`","metadata":{"repo":"` + repo + `","pr_number":` + strconv.Itoa(num) + `}}`
}

// closedReviewJSON builds a CLOSED review-pr task bead. When headSHA is empty the
// head_sha metadata key is OMITTED (a legacy pre-cursor bead), so the re-review
// guard treats it as "reviewed sha unknown -> do not re-emit".
func closedReviewJSON(id, repo string, num int, headSHA string) string {
	meta := `"repo":"` + repo + `","pr_number":` + strconv.Itoa(num)
	if headSHA != "" {
		meta += `,"head_sha":"` + headSHA + `"`
	}
	return `{"id":"` + id + `","issue_type":"task","status":"closed","title":"review-pr: ` + repo + "#" + strconv.Itoa(num) +
		`","metadata":{` + meta + `}}`
}

// TestReconcile_EnsuresReviewChildGateAndResolves: MR present, no existing
// review-pr -> creates the review-pr task bead (prefix + metadata), links it as
// a child of the MR, creates the active-pr gate at birth, and the watcher pass
// resolves that gate (PR is in the open list). Emits the review-pr id.
func TestReconcile_EnsuresReviewChildGateAndResolves(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[]}`,
		"create":             "zr-rv7\n",
		"gate create":        `{"data":{"id":"g7"}}`,
		"gate list":          `{"data":[{"id":"g7","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#7"}]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: "open", Ownership: "mine"}}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(ids) != 1 || ids[0] != "zr-rv7" {
		t.Fatalf("expected review id zr-rv7, got %v", ids)
	}
	if f.countCalls("create", "--type=task", "review-pr: o/r#7") != 1 {
		t.Errorf("expected exactly one review-pr create; calls=%v", f.calls)
	}
	if f.countCalls("dep", "add", "zr-rv7", "zr-mr7") != 1 {
		t.Errorf("expected review-pr linked as child of the MR bead")
	}
	if f.countCalls("gate", "create", "--type=pg-pr:active-pr") != 1 {
		t.Errorf("expected an active-pr gate created at birth")
	}
	if f.countCalls("gate", "resolve", "g7") != 1 {
		t.Errorf("watcher must resolve the active-pr gate for an open PR; calls=%v", f.calls)
	}
}

// TestReconcile_Idempotent: an existing review-pr -> no create, no link, no new
// gate; emits the existing id. (Second reconcile pass = no dupes.)
func TestReconcile_Idempotent(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[` + reviewJSON("zr-rv7", "o/r", 7) + `]}`,
		"gate list":          `{"data":[]}`, // already resolved
	}}
	prs := []PR{{Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: "open"}}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(ids) != 1 || ids[0] != "zr-rv7" {
		t.Fatalf("expected existing review id zr-rv7, got %v", ids)
	}
	if f.countCalls("create") != 0 {
		t.Errorf("idempotent pass must NOT create; calls=%v", f.calls)
	}
	if f.countCalls("gate", "create") != 0 {
		t.Errorf("idempotent pass must NOT create a gate")
	}
}

// TestReconcile_NoMergeRequestSkips (NH2): with no pg-pr merge-request bead, the
// ACL must NOT create one — it skips the PR (pg-pr's sync daemon owns MR creation).
func TestReconcile_NoMergeRequestSkips(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[]}`,
		"list task":          `{"data":[]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, State: "open"}}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("missing MR is an expected skip, not an error: %v", errs)
	}
	if len(ids) != 0 {
		t.Errorf("expected no review ids when MR is absent, got %v", ids)
	}
	if f.countCalls("create") != 0 {
		t.Errorf("NH2 violated: ACL must not create a merge-request/review bead when MR is absent; calls=%v", f.calls)
	}
}

// TestReconcile_MergeRequestClosedSkips: a closed MR bead -> no review child.
func TestReconcile_MergeRequestClosedSkips(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "closed") + `]}`,
		"list task":          `{"data":[]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, State: "open"}}
	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 || len(ids) != 0 {
		t.Fatalf("closed MR: expected no ids/errs, got ids=%v errs=%v", ids, errs)
	}
	if f.countCalls("create") != 0 {
		t.Errorf("must not attach a review child to a closed MR bead")
	}
}

// TestReconcile_ExitZeroOnPartial: one PR's create fails; the other is still
// processed. Reconcile returns collected errs (never aborts) so the caller can
// exit 0 and a following drain is not stranded.
func TestReconcile_ExitZeroOnPartial(t *testing.T) {
	f := &fakeBD{
		responses: map[string]string{
			"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `,` + mrJSON("zr-mr9", "o/r", 9, "open") + `]}`,
			"list task":          `{"data":[]}`,
			"create":             "zr-rv9\n",
			"gate create":        `{"data":{"id":"g9"}}`,
			"gate list":          `{"data":[{"id":"g9","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#9"}]}`,
		},
		errSubstr: map[string]error{"review-pr: o/r#7": errors.New("boom create 7")},
	}
	prs := []PR{
		{Repo: "o/r", Number: 7, State: "open"},
		{Repo: "o/r", Number: 9, State: "open"},
	}
	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) == 0 {
		t.Fatalf("expected the failing PR to be collected as an error")
	}
	if len(ids) != 1 || ids[0] != "zr-rv9" {
		t.Errorf("the healthy PR must still be processed, got ids=%v", ids)
	}
}

// TestReconcile_ClosedReviewNotResurrected (C1): a completed (closed) review-pr
// bead for a still-open PR must NOT be recreated — the task snapshot is fetched
// with --all so the closed bead is found and the PR is skipped.
func TestReconcile_ClosedReviewNotResurrected(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[{"id":"zr-rv7","issue_type":"task","status":"closed","title":"review-pr: o/r#7","metadata":{"repo":"o/r","pr_number":7}}]}`,
		"gate list":          `{"data":[]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, State: "open"}}
	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(ids) != 0 {
		t.Errorf("a closed review-pr must not be re-emitted as work, got %v", ids)
	}
	if f.countCalls("create") != 0 {
		t.Errorf("must NOT resurrect a closed review-pr; calls=%v", f.calls)
	}
}

// TestReconcile_HeadAdvancedReopensClosedReview (6a): a CLOSED review-pr whose
// reviewed head_sha is behind the PR's current head_sha means new commits landed
// after the review — the ACL re-emits it by REOPENING the same bead with the NEW
// head_sha + branch (the re-review-on-head-advance cursor replacing pg-pr's
// retired reopenStaleReviews). It must NOT create a second bead.
func TestReconcile_HeadAdvancedReopensClosedReview(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[` + closedReviewJSON("zr-rv7", "o/r", 7, "oldsha") + `]}`,
		"gate list":          `{"data":[]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, HeadSHA: "newsha", Branch: "feat/x", State: "open", Ownership: "team"}}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(ids) != 1 || ids[0] != "zr-rv7" {
		t.Fatalf("expected the reopened review id zr-rv7, got %v", ids)
	}
	if f.countCalls("update", "zr-rv7", "--status=open", "head_sha=newsha") != 1 {
		t.Errorf("expected a reopen update carrying the NEW head_sha; calls=%v", f.calls)
	}
	if f.countCalls("update", "zr-rv7", "branch=feat/x") != 1 {
		t.Errorf("expected the reopen to refresh branch; calls=%v", f.calls)
	}
	if f.countCalls("create") != 0 {
		t.Errorf("head advance must REOPEN, not create a second bead; calls=%v", f.calls)
	}
}

// TestReconcile_HeadUnchangedNotResurrected (6a): a CLOSED review-pr whose
// reviewed head_sha equals the PR's current head_sha = already reviewed this head
// -> no reopen, no create (the ClosedReviewNotResurrected invariant, now keyed on
// the sha rather than "closed always suppresses").
func TestReconcile_HeadUnchangedNotResurrected(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[` + closedReviewJSON("zr-rv7", "o/r", 7, "samesha") + `]}`,
		"gate list":          `{"data":[]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, HeadSHA: "samesha", Branch: "feat/x", State: "open", Ownership: "team"}}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(ids) != 0 {
		t.Errorf("head unchanged must not re-emit, got %v", ids)
	}
	if f.countCalls("update", "--status=open") != 0 {
		t.Errorf("must not reopen when the head is unchanged; calls=%v", f.calls)
	}
	if f.countCalls("create") != 0 {
		t.Errorf("must not create when the head is unchanged; calls=%v", f.calls)
	}
}

// TestReconcile_LegacyClosedNoHeadSHANotResurrected (6a / Q9): a CLOSED review-pr
// created before the cursor (no head_sha metadata) must NOT be re-reviewed even
// when the PR head has a value — we don't know which commit was reviewed, so
// re-emitting could re-review an already-reviewed head. Missing reviewed sha =>
// suppress.
func TestReconcile_LegacyClosedNoHeadSHANotResurrected(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[` + closedReviewJSON("zr-rv7", "o/r", 7, "") + `]}`,
		"gate list":          `{"data":[]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, HeadSHA: "newsha", Branch: "feat/x", State: "open", Ownership: "team"}}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(ids) != 0 {
		t.Errorf("a legacy closed review-pr with no reviewed sha must not be re-emitted, got %v", ids)
	}
	if f.countCalls("update", "--status=open") != 0 || f.countCalls("create") != 0 {
		t.Errorf("legacy closed bead must be left alone; calls=%v", f.calls)
	}
}

// TestReconcile_TeamDraftSkipped (I2): a teammate PR still in draft is not
// reviewed (selection parity with pg-pr's beadsbridge).
func TestReconcile_TeamDraftSkipped(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[]}`,
		"gate list":          `{"data":[]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, State: "draft", Ownership: "team"}}
	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 || len(ids) != 0 {
		t.Fatalf("team draft PR: expected skip, got ids=%v errs=%v", ids, errs)
	}
	if f.countCalls("create") != 0 {
		t.Errorf("teammate draft PR must not be reviewed")
	}
}

// TestReconcile_MineDraftReviewed (I2): my own PR is reviewed even while a draft.
func TestReconcile_MineDraftReviewed(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[]}`,
		"create":             "zr-rv7\n",
		"gate create":        `{"data":{"id":"g7"}}`,
		"gate list":          `{"data":[]}`,
	}}
	prs := []PR{{Repo: "o/r", Number: 7, State: "draft", Ownership: "mine"}}
	ids, _ := Reconcile(context.Background(), f, prs)
	if len(ids) != 1 || ids[0] != "zr-rv7" {
		t.Errorf("my draft PR must be reviewed (parity), got %v", ids)
	}
}

func TestParsePRList(t *testing.T) {
	prs, err := parsePRList([]byte(`[
		{"repo":"o/r","number":7,"state":"open","ownership":"mine","draft":false,"branch":"feat/x","head_sha":"abc123"},
		{"repo":"o/r","number":9,"state":"draft","ownership":"team","draft":true,"branch":"feat/y","head_sha":"def456"}
	]`))
	if err != nil {
		t.Fatalf("parsePRList: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %d", len(prs))
	}
	if prs[0].Repo != "o/r" || prs[0].Number != 7 || prs[0].HeadSHA != "abc123" || prs[0].Branch != "feat/x" {
		t.Errorf("PR0 parsed wrong: %+v", prs[0])
	}
	if prs[1].Number != 9 || prs[1].Ownership != "team" {
		t.Errorf("PR1 parsed wrong: %+v", prs[1])
	}
}
