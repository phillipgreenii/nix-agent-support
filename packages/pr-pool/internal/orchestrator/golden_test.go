package orchestrator

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// TestGolden_workerDispatchShape pins the no-config worker dispatch shape against
// LITERAL expectations (structural, not a diff of the old binary): a deterministic
// external_id (pinned stamp), the editable task body free of safety rails, and the
// full sent nudge = injected preamble + rendered task — so the rails moved to the
// preamble (decision 4) cannot silently regress, and interpolation still works.
func TestGolden_workerDispatchShape(t *testing.T) {
	rs := roles.BuiltinRoleSet(roles.BuiltinParams{WorktreeDir: "/wt", WorkerSkillMD: "WSKILL", MaxWorker: 1, MaxFeedback: 1})
	wk := rs[1]
	if wk.Name != "worker" {
		t.Fatalf("rs[1] should be worker, got %q", wk.Name)
	}
	if got := wk.ExternalID("pr-pool-", "zr-w.2", testStamp); got != "pr-pool-worker-zr-w.2-"+testStamp {
		t.Fatalf("external_id = %q", got)
	}

	ctx := prompt.Context{Item: item.Item{ID: "zr-w.2"}, WorktreeDir: "/wt", SkillMD: "WSKILL"}
	task, err := prompt.Render(wk.CCPool.Prompt, ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Rails live ONLY in the injected preamble, never the editable task body.
	for _, rail := range []string{"phillipg.", "git push --force"} {
		if strings.Contains(task, rail) {
			t.Fatalf("task body must NOT contain rail %q (it belongs in the preamble)", rail)
		}
	}
	// The full sent nudge = preamble + task carries the rails AND the interpolated vars.
	full := prompt.AuthorshipPreamble() + task
	for _, want := range []string{"phillipg.", "NEVER git push --force", "human", "zr-w.2", "/wt"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full nudge missing %q\n---\n%s", want, full)
		}
	}

	// The built-in worker keeps today's behavior: authorship guard on, hand-back
	// completion, add-human-on-failure, leave-on-dispatch-fail, no email/etc.
	cc := wk.CCPool
	if !cc.AuthorshipGuard || cc.Completion != roles.CloseOrHandback || cc.OnFailure != roles.AddHuman || cc.OnDispatchFail != roles.DispatchLeave {
		t.Fatalf("worker ccpool behavior drifted from today's defaults: %+v", cc)
	}
}
