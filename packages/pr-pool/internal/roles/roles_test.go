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
	// M3: roles BIND to event types instead of embedding a query.
	if len(fb.Binds) != 1 || fb.Binds[0] != EventFeedbackReady {
		t.Fatalf("feedback must bind %q, got %+v", EventFeedbackReady, fb.Binds)
	}
	if len(wk.Binds) != 1 || wk.Binds[0] != EventWorkReady {
		t.Fatalf("worker must bind %q, got %+v", EventWorkReady, wk.Binds)
	}
	if len(rv.Binds) != 1 || rv.Binds[0] != EventReviewReady {
		t.Fatalf("review must bind %q, got %+v", EventReviewReady, rv.Binds)
	}
	if fb.CCPool.Actor != "pgii-pool__process-feedback" || wk.CCPool.Actor != "pgii-pool__worker" || rv.CCPool.Actor != "pgii-pool__review" {
		t.Fatalf("actors wrong")
	}
}

// TestBuiltinQuerySet_pairsWithRoles verifies the built-in producers emit exactly
// the event types the built-in roles bind, with the review query preserving the
// review-pr filter it used to embed — the M3 decoupling reproduces today's
// pairing through the shared event-type string.
func TestBuiltinQuerySet_pairsWithRoles(t *testing.T) {
	qs := BuiltinQuerySet(BuiltinParams{MaxFeedback: 1, MaxWorker: 1})
	if len(qs) != 3 {
		t.Fatalf("want 3 built-in queries, got %d", len(qs))
	}
	byEmit := map[string]query.BeadsReady{}
	for _, s := range qs {
		br, ok := s.Query.(query.BeadsReady)
		if !ok {
			t.Fatalf("built-in query %q must be beads-ready, got %T", s.Name, s.Query)
		}
		if len(br.Emits()) != 1 {
			t.Fatalf("query %q must emit exactly one type, got %+v", s.Name, br.Emits())
		}
		if !query.IsPeriod(br.Trigger()) {
			t.Fatalf("built-in query %q must use a PeriodTrigger, got %#v", s.Name, br.Trigger())
		}
		byEmit[br.Emits()[0]] = br
	}
	if _, ok := byEmit[EventFeedbackReady]; !ok {
		t.Fatalf("no query emits %q", EventFeedbackReady)
	}
	if _, ok := byEmit[EventWorkReady]; !ok {
		t.Fatalf("no query emits %q", EventWorkReady)
	}
	rv := byEmit[EventReviewReady]
	if rv.TitlePrefix != "review-pr: " || rv.ItemType != "task" {
		t.Fatalf("review query must filter to review-pr task beads: %+v", rv)
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

// TestReviewPrompt_MineOwnershipFilesProcessFeedbackNotGitHub is the Phase A
// proof (pg2-ynhr.5) that the review prompt's rendered TEXT — for a
// self-authored PR — routes through beads instead of GitHub: it must instruct
// filing a "process-feedback: <repo>#<num>" bead carrying the "mine" label
// (the exact shape roles.BuiltinQuerySet's feedback-source query selects,
// TitlePrefix "process-feedback:" + Labels ["mine"]) and must NOT instruct
// `pg-pr review submit`, the GitHub-posting verb only a teammate PR should use.
func TestReviewPrompt_MineOwnershipFilesProcessFeedbackNotGitHub(t *testing.T) {
	for _, ownership := range []string{"mine", "co-owned"} {
		rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", MaxWorker: 1, MaxFeedback: 1})
		rv := rs[2]
		out, err := prompt.Render(rv.CCPool.Prompt, prompt.Context{
			Item: item.Item{
				ID: "zr-rv7", Type: "task", Title: "review-pr: o/r#7",
				Metadata: map[string]any{
					"repo": "o/r", "pr_number": float64(7), "branch": "feat/x",
					"head_sha": "abc123", "ownership": ownership,
				},
			},
			WorktreeDir: "/wt/zr-rv7",
		})
		if err != nil {
			t.Fatalf("ownership=%s: review prompt render: %v", ownership, err)
		}
		for _, want := range []string{
			`bd create --type=task --title "process-feedback: o/r#7"`,
			"--label mine",
			"do NOT post anything to GitHub",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("ownership=%s: review prompt missing %q:\n%s", ownership, want, out)
			}
		}
		if strings.Contains(out, "pg-pr review submit") {
			t.Errorf("ownership=%s: review prompt must NOT instruct pg-pr review submit (GitHub write) for a self-authored PR:\n%s", ownership, out)
		}
	}
}

// TestReviewPrompt_TeamOwnershipStillPostsToGitHub pins the unchanged teammate
// path: an explicit ownership=team (and, by TestReviewPrompt_
// RendersCoordsCheckoutAndPost, an absent ownership key) must still post
// through pg-pr review submit and must NOT file a process-feedback bead.
func TestReviewPrompt_TeamOwnershipStillPostsToGitHub(t *testing.T) {
	rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", MaxWorker: 1, MaxFeedback: 1})
	rv := rs[2]
	out, err := prompt.Render(rv.CCPool.Prompt, prompt.Context{
		Item: item.Item{
			ID: "zr-rv7", Type: "task", Title: "review-pr: o/r#7",
			Metadata: map[string]any{
				"repo": "o/r", "pr_number": float64(7), "branch": "feat/x",
				"head_sha": "abc123", "ownership": "team",
			},
		},
		WorktreeDir: "/wt/zr-rv7",
	})
	if err != nil {
		t.Fatalf("review prompt render: %v", err)
	}
	if !strings.Contains(out, "pg-pr review submit") {
		t.Errorf("team ownership must still post via pg-pr review submit:\n%s", out)
	}
	if strings.Contains(out, "process-feedback:") {
		t.Errorf("team ownership must NOT file a process-feedback bead:\n%s", out)
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
