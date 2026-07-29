package prpoolacl

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

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

// fresh stamps each fixture PR with an as-of time INSIDE pg-pr's freshness bound.
// Every fixture whose subject is not freshness must go through it: the ACL
// refuses to act on a row with no usable as-of time (staleForAction), so a bare
// PR literal would simply be skipped and the fixture would prove nothing.
func fresh(prs ...PR) []PR {
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]PR, 0, len(prs))
	for _, pr := range prs {
		pr.LastSyncedAt = now
		out = append(out, pr)
	}
	return out
}

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
	prs := fresh(PR{Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: "open", Ownership: "mine"})

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
	prs := fresh(PR{Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: "open"})

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
	prs := fresh(PR{Repo: "o/r", Number: 7, State: "open"})

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
	prs := fresh(PR{Repo: "o/r", Number: 7, State: "open"})
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
	prs := fresh(
		PR{Repo: "o/r", Number: 7, State: "open"},
		PR{Repo: "o/r", Number: 9, State: "open"},
	)
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
	prs := fresh(PR{Repo: "o/r", Number: 7, State: "open"})
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
	prs := fresh(PR{Repo: "o/r", Number: 7, HeadSHA: "newsha", Branch: "feat/x", State: "open", Ownership: "team"})

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
	prs := fresh(PR{Repo: "o/r", Number: 7, HeadSHA: "samesha", Branch: "feat/x", State: "open", Ownership: "team"})

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
	prs := fresh(PR{Repo: "o/r", Number: 7, HeadSHA: "newsha", Branch: "feat/x", State: "open", Ownership: "team"})

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

// birthFake is a fakeBD scripted for the review-pr BIRTH path of o/r#7: an open
// MR bead, no existing review-pr, and a create that returns zr-rv7.
func birthFake() *fakeBD {
	return &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[]}`,
		"create":             "zr-rv7\n",
		"gate create":        `{"data":{"id":"g7"}}`,
		"gate list":          `{"data":[]}`,
	}}
}

// TestReconcile_DraftSelectionMatrix (I2) is the FULL (ownership x draft) truth
// table for the draft gate. It must match pg-pr's beadsbridge predicate
// (packages/pg-pr/internal/beadsbridge/bridge.go:111-112):
//
//	mine := p.Ownership != "team" // mine OR co-owned
//	if !h.suppressDraftReviews && (mine || !p.Draft) {
//
// i.e. every combination is reviewed EXCEPT a team PR still in draft. The
// co-owned+draft row is the parity break pg2-42uap fixed: the old
// `pr.Ownership != "mine"` gate silently dropped co-owned drafts forever.
func TestReconcile_DraftSelectionMatrix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ownership string
		state     string
		reviewed  bool
	}{
		{"mine/not-draft", ownershipMine, "open", true},
		{"mine/draft", ownershipMine, "draft", true},
		{"co-owned/not-draft", ownershipCoOwned, "open", true},
		{"co-owned/draft", ownershipCoOwned, "draft", true},
		{"team/not-draft", ownershipTeam, "open", true},
		{"team/draft", ownershipTeam, "draft", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := birthFake()
			prs := fresh(PR{Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: tc.state, Ownership: tc.ownership})

			ids, errs := Reconcile(context.Background(), f, prs)
			if len(errs) != 0 {
				t.Fatalf("unexpected errs: %v", errs)
			}
			if tc.reviewed {
				if len(ids) != 1 || ids[0] != "zr-rv7" {
					t.Fatalf("ownership=%s state=%s must be reviewed, got ids=%v", tc.ownership, tc.state, ids)
				}
				if f.countCalls("create", "--type=task", "review-pr: o/r#7") != 1 {
					t.Errorf("expected exactly one review-pr create; calls=%v", f.calls)
				}
				return
			}
			if len(ids) != 0 {
				t.Fatalf("ownership=%s state=%s must be skipped, got ids=%v", tc.ownership, tc.state, ids)
			}
			if f.countCalls("create") != 0 {
				t.Errorf("a team draft PR must not be reviewed; calls=%v", f.calls)
			}
		})
	}
}

// TestReconcile_CoOwnedDraftReviewed is the regression for pg2-42uap: a co-owned
// PR still in GitHub draft MUST yield a review-pr bead (child of the MR, with its
// active-pr gate), because pg-pr's beadsbridge counts co-owned as mine
// (`mine := p.Ownership != "team"`, bridge.go:111-112) and reviews mine while draft.
func TestReconcile_CoOwnedDraftReviewed(t *testing.T) {
	f := birthFake()
	prs := fresh(PR{Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: "draft", Ownership: ownershipCoOwned})

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(ids) != 1 || ids[0] != "zr-rv7" {
		t.Fatalf("a co-owned draft PR must yield a review-pr bead, got ids=%v", ids)
	}
	if f.countCalls("create", "--type=task", "review-pr: o/r#7") != 1 {
		t.Errorf("expected exactly one review-pr create; calls=%v", f.calls)
	}
	if f.countCalls("dep", "add", "zr-rv7", "zr-mr7") != 1 {
		t.Errorf("expected the review-pr linked as a child of the MR bead; calls=%v", f.calls)
	}
	if f.countCalls("gate", "create", "--type=pg-pr:active-pr") != 1 {
		t.Errorf("expected an active-pr gate created at birth; calls=%v", f.calls)
	}
}

// TestActsAsMineParity pins pr-pool's copied predicate to pg-pr's two
// formulations over the CLOSED 3-value ownership set — ownership.ActsAsMine's
// `o == Mine || o == CoOwned` (internal/ownership/ownership.go:46) and
// beadsbridge's `p.Ownership != "team"` (internal/beadsbridge/bridge.go:111) —
// so the duplication cannot silently drift. An out-of-band value degrades to
// team-style selection by design (conservative: a draft is skipped, not reviewed).
func TestActsAsMineParity(t *testing.T) {
	for _, o := range []string{ownershipMine, ownershipCoOwned, ownershipTeam} {
		if got, want := actsAsMine(o), o != ownershipTeam; got != want {
			t.Errorf("actsAsMine(%q)=%v; pg-pr's `Ownership != %q` says %v", o, got, ownershipTeam, want)
		}
	}
	for _, o := range []string{"", "unknown"} {
		if actsAsMine(o) {
			t.Errorf("actsAsMine(%q) must be false (out-of-band degrades to team-style)", o)
		}
	}
}

func TestParsePRList(t *testing.T) {
	prs, err := parsePRList([]byte(`[
		{"repo":"o/r","number":7,"state":"open","ownership":"mine","draft":false,"branch":"feat/x","head_sha":"abc123","last_synced_at":"2026-07-29T11:59:30Z","stale":false},
		{"repo":"o/r","number":9,"state":"draft","ownership":"team","draft":true,"branch":"feat/y","head_sha":"def456","last_synced_at":"2026-07-29T10:00:00Z","stale":true}
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
	// The freshness fields must decode under pg-pr's exact wire names, or the
	// ACL would silently see every row as "as-of unknown".
	if prs[0].LastSyncedAt != "2026-07-29T11:59:30Z" || prs[0].Stale {
		t.Errorf("PR0 freshness parsed wrong: last_synced_at=%q stale=%v", prs[0].LastSyncedAt, prs[0].Stale)
	}
	if prs[1].LastSyncedAt != "2026-07-29T10:00:00Z" || !prs[1].Stale {
		t.Errorf("PR1 freshness parsed wrong: last_synced_at=%q stale=%v", prs[1].LastSyncedAt, prs[1].Stale)
	}
}

// TestStaleForAction is the truth table for the freshness gate. "Fresh" means
// BOTH: pg-pr did not flag the row stale, AND the row carries a parseable as-of
// time. Anything else refuses action (fail closed).
func TestStaleForAction(t *testing.T) {
	rfc := time.Now().UTC().Format(time.RFC3339)
	for _, tc := range []struct {
		name string
		pr   PR
		want bool
	}{
		{"fresh row with an as-of time", PR{LastSyncedAt: rfc, Stale: false}, false},
		{"pg-pr flagged it stale", PR{LastSyncedAt: rfc, Stale: true}, true},
		{"as-of absent from the seam (older pg-pr)", PR{}, true},
		{"as-of empty", PR{LastSyncedAt: ""}, true},
		{"as-of unparseable", PR{LastSyncedAt: "yesterday"}, true},
		{"as-of not RFC3339 (bare date)", PR{LastSyncedAt: "2026-07-29"}, true},
		{"stale flag wins even with a valid as-of", PR{LastSyncedAt: rfc, Stale: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := staleForAction(tc.pr); got != tc.want {
				t.Errorf("staleForAction(%+v) = %v, want %v", tc.pr, got, tc.want)
			}
		})
	}
}

// TestReconcile_StalePRRefusedNoBeadNoGate is the INV-FRESH-1 behaviour: a PR
// pg-pr flagged stale must NOT be acted on — no review-pr bead is created (phase
// 1) and its active-pr gate is NOT resolved (phase 2). Resolving that gate would
// assert "pg-pr reports PR open/active" on the strength of data pg-pr itself says
// is past its bound.
func TestReconcile_StalePRRefusedNoBeadNoGate(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[]}`,
		"create":             "zr-rv7\n",
		"gate create":        `{"data":{"id":"g7"}}`,
		"gate list":          `{"data":[{"id":"g7","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#7"}]}`,
	}}
	prs := []PR{{
		Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: "open",
		Ownership: "mine",
		// Synced an hour ago and flagged by pg-pr: the daemon is behind/stopped.
		LastSyncedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
		Stale:        true,
	}}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("a stale PR is an expected refusal, not an error: %v", errs)
	}
	if len(ids) != 0 {
		t.Errorf("must not emit review work from stale facts, got %v", ids)
	}
	if f.countCalls("create") != 0 {
		t.Errorf("must not create a review-pr bead from stale facts; calls=%v", f.calls)
	}
	if f.countCalls("gate", "resolve") != 0 {
		t.Errorf("must not resolve the active-pr gate from stale facts; calls=%v", f.calls)
	}
}

// TestReconcile_MissingAsOfRefused: an older pg-pr that predates the freshness
// fields emits no last_synced_at, which decodes to "". An unknown as-of is
// treated as stale, never as fresh — the whole point of the fail-closed
// direction. The pass is a loud no-op, not a silent act-on-unknown-age.
func TestReconcile_MissingAsOfRefused(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `]}`,
		"list task":          `{"data":[]}`,
		"create":             "zr-rv7\n",
		"gate create":        `{"data":{"id":"g7"}}`,
		"gate list":          `{"data":[{"id":"g7","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#7"}]}`,
	}}
	// No LastSyncedAt, no Stale flag — exactly what an older pg-pr's payload
	// decodes to.
	prs := []PR{{Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: "open", Ownership: "mine"}}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("an unknown as-of is an expected refusal, not an error: %v", errs)
	}
	if len(ids) != 0 || f.countCalls("create") != 0 || f.countCalls("gate", "resolve") != 0 {
		t.Errorf("a row with no usable as-of must not be acted on; ids=%v calls=%v", ids, f.calls)
	}
}

// TestReconcile_FreshnessGateIsPerPR: the refusal is per PR, not whole-pass. One
// stale row must not withhold action on a freshly synced one — a repo mid-refresh
// legitimately holds rows of different ages.
func TestReconcile_FreshnessGateIsPerPR(t *testing.T) {
	f := &fakeBD{responses: map[string]string{
		"list merge-request": `{"data":[` + mrJSON("zr-mr7", "o/r", 7, "open") + `,` + mrJSON("zr-mr9", "o/r", 9, "open") + `]}`,
		"list task":          `{"data":[]}`,
		"create":             "zr-rv9\n",
		"gate create":        `{"data":{"id":"g9"}}`,
		"gate list": `{"data":[` +
			`{"id":"g7","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#7"},` +
			`{"id":"g9","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#9"}]}`,
	}}
	now := time.Now().UTC()
	prs := []PR{
		{
			Repo: "o/r", Number: 7, State: "open", Ownership: "mine",
			LastSyncedAt: now.Add(-time.Hour).Format(time.RFC3339), Stale: true,
		},
		{
			Repo: "o/r", Number: 9, State: "open", Ownership: "mine",
			LastSyncedAt: now.Format(time.RFC3339),
		},
	}

	ids, errs := Reconcile(context.Background(), f, prs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(ids) != 1 || ids[0] != "zr-rv9" {
		t.Fatalf("the FRESH PR must still be processed, got ids=%v", ids)
	}
	if f.countCalls("create", "review-pr: o/r#9") != 1 {
		t.Errorf("expected the fresh PR's review-pr create; calls=%v", f.calls)
	}
	if f.countCalls("create", "review-pr: o/r#7") != 0 {
		t.Errorf("the stale PR must not be created; calls=%v", f.calls)
	}
	// Only the fresh PR's gate is resolved; the stale PR's gate stays open, so its
	// review-pr bead (from an earlier pass) remains held out of `bd ready`.
	if f.countCalls("gate", "resolve", "g9") != 1 {
		t.Errorf("expected the fresh PR's gate resolved; calls=%v", f.calls)
	}
	if f.countCalls("gate", "resolve", "g7") != 0 {
		t.Errorf("the stale PR's gate must stay unresolved; calls=%v", f.calls)
	}
}

// TestReconcile_StaleRowSelfHeals: the refusal is not sticky. The SAME PR, once
// pg-pr's sync catches up (fresh as-of, flag cleared), is acted on normally on the
// next pass — no operator intervention, no bead surgery.
func TestReconcile_StaleRowSelfHeals(t *testing.T) {
	pr := PR{Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x", State: "open", Ownership: "mine"}

	stalePass := birthFake()
	stalePass.responses["gate list"] = `{"data":[{"id":"g7","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#7"}]}`
	stale := pr
	stale.LastSyncedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	stale.Stale = true
	if ids, _ := Reconcile(context.Background(), stalePass, []PR{stale}); len(ids) != 0 {
		t.Fatalf("stale pass must emit nothing, got %v", ids)
	}

	freshPass := birthFake()
	freshPass.responses["gate list"] = `{"data":[{"id":"g7","issue_type":"gate","await_type":"pg-pr:active-pr","await_id":"o/r#7"}]}`
	ids, errs := Reconcile(context.Background(), freshPass, fresh(pr))
	if len(errs) != 0 {
		t.Fatalf("unexpected errs on the recovered pass: %v", errs)
	}
	if len(ids) != 1 || ids[0] != "zr-rv7" {
		t.Fatalf("once pg-pr catches up the PR must be acted on, got %v", ids)
	}
	if freshPass.countCalls("gate", "resolve", "g7") != 1 {
		t.Errorf("the recovered pass must resolve the active-pr gate; calls=%v", freshPass.calls)
	}
}
