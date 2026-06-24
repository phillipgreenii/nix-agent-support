package roles

import (
	"strings"
	"testing"
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
	if len(rs) != 2 || rs[0].Name != "feedback" || rs[1].Name != "worker" {
		t.Fatalf("builtin set wrong: %+v", rs)
	}
	fb, wk := rs[0], rs[1]
	if fb.CCPool.Completion != CloseOnly || fb.CCPool.OnFailure != Unclaim || fb.CCPool.OnDispatchFail != DispatchUnclaim || fb.CCPool.AuthorshipGuard {
		t.Fatalf("feedback behavior wrong: %+v", fb.CCPool)
	}
	if wk.CCPool.Completion != CloseOrHandback || wk.CCPool.OnFailure != AddHuman || wk.CCPool.OnDispatchFail != DispatchLeave || !wk.CCPool.AuthorshipGuard {
		t.Fatalf("worker behavior wrong: %+v", wk.CCPool)
	}
	if fb.CCPool.Actor != "pgii-pool__process-feedback" || wk.CCPool.Actor != "pgii-pool__worker" {
		t.Fatalf("actors wrong")
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
