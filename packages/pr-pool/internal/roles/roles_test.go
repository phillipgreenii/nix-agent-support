package roles

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/query"
)

func TestExternalID_andDisplayName(t *testing.T) {
	r := Role{Name: "worker"}
	if got := r.ExternalID("pr-pool-", "zr-w.2", "STAMP"); got != "pr-pool-worker-zr-w.2-STAMP" {
		t.Fatalf("ExternalID = %q", got)
	}
	if got := r.DisplayName("pr-pool-", "zr-w.2"); got != "pr-pool-worker-zr-w.2" {
		t.Fatalf("DisplayName = %q", got)
	}
}

func TestBuiltinRoleSet_shape(t *testing.T) {
	rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", SkillMD: "S", WorkerSkillMD: "W", MaxFeedback: 1, MaxWorker: 1})
	if len(rs) != 3 || rs[0].Name != "feedback" || rs[1].Name != "worker" || rs[2].Name != "review" {
		t.Fatalf("builtin set wrong: %+v", rs)
	}
	fb, wk, rv := rs[0], rs[1], rs[2]
	if fb.CCPool.Completion != CloseOnly || fb.CCPool.OnFailure != Unclaim || fb.CCPool.OnDispatchFail != DispatchUnclaim || fb.CCPool.AuthorshipGuard {
		t.Fatalf("feedback behavior wrong: %+v", fb.CCPool)
	}
	if wk.CCPool.Completion != CloseOrHandback || wk.CCPool.OnFailure != AddHuman || wk.CCPool.OnDispatchFail != DispatchLeave || !wk.CCPool.AuthorshipGuard {
		t.Fatalf("worker behavior wrong: %+v", wk.CCPool)
	}
	// review reviews teammate PRs too, so its authorship guard MUST be off.
	if rv.CCPool.Completion != CloseOrHandback || rv.CCPool.AuthorshipGuard {
		t.Fatalf("review behavior wrong (authorship guard must be false): %+v", rv.CCPool)
	}
	// review must select ONLY the review-pr beads.
	brq, ok := rv.Query.(query.BeadsReady)
	if !ok || brq.TitlePrefix != "review-pr: " || brq.ItemType != "task" {
		t.Fatalf("review query must filter to review-pr task beads: %+v", rv.Query)
	}
	if fb.CCPool.Actor != "pgii-pool__process-feedback" || wk.CCPool.Actor != "pgii-pool__worker" || rv.CCPool.Actor != "pgii-pool__review" {
		t.Fatalf("actors wrong")
	}
}

// TestReviewPrompt_RendersCoordsCheckoutAndPost verifies the ported review
// prompt renders with the PR coords templated from the review-pr bead metadata,
// the NH4 PR-head checkout instruction, and the pg-pr post-back + complete-on-close.
func TestReviewPrompt_RendersCoordsCheckoutAndPost(t *testing.T) {
	rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", MaxWorker: 1, MaxFeedback: 1})
	rv := rs[2]
	out, err := prompt.Render(rv.CCPool.Prompt, prompt.Context{
		Item: item.Item{
			ID: "zr-rv7", Type: "task", Title: "review-pr: o/r#7",
			Metadata: map[string]any{
				"repo": "o/r", "pr_number": float64(7), "branch": "feat/x", "head_sha": "abc123",
			},
		},
		WorktreeDir: "/wt/zr-rv7",
	})
	if err != nil {
		t.Fatalf("review prompt render: %v", err)
	}
	for _, want := range []string{
		"o/r#7",                    // coords
		"fetch origin pull/7/head", // NH4: fetch the PR head
		"checkout abc123",          // NH4: check out the reviewed commit
		"pg-pr review",             // post back through pg-pr
		"bd close zr-rv7",          // complete-on-close
		"bd update zr-rv7 --claim", // claims first
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review prompt missing %q:\n%s", want, out)
		}
	}
}

func TestBuiltinWorkerPrompt_forbidsAskUserQuestion(t *testing.T) {
	rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", MaxWorker: 1, MaxFeedback: 1})
	body := rs[1].CCPool.PromptBody
	if !strings.Contains(body, "AskUserQuestion") {
		t.Fatalf("worker prompt must explicitly name and forbid AskUserQuestion (D2 prompt-forbid lever); body:\n%s", body)
	}
}

func TestBuiltinWorkerPrompt_taskBodyHasNoRails(t *testing.T) {
	rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", MaxWorker: 1, MaxFeedback: 1})
	body := rs[1].CCPool.PromptBody
	for _, rail := range []string{"phillipg.", "git push --force"} {
		if strings.Contains(body, rail) {
			t.Fatalf("worker task body must NOT contain rail %q (it lives in the injected preamble)", rail)
		}
	}
	if !strings.Contains(body, "{{.BeadID}}") {
		t.Fatalf("worker task body should interpolate the bead id")
	}
}
