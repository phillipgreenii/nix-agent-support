package roles

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

func TestNewRegistry_fromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.SkillMD = "/skills/fb.md"
	cfg.WorkerSkillMD = "/skills/wk.md"
	cfg.MaxWorker = 2
	reg := NewRegistry(cfg)
	if reg.Feedback.Actor != "pgii-pool__process-feedback" {
		t.Errorf("feedback actor = %q", reg.Feedback.Actor)
	}
	if reg.Worker.Actor != "pgii-pool__worker" {
		t.Errorf("worker actor = %q", reg.Worker.Actor)
	}
	if reg.Worker.Cap != 2 || reg.Feedback.Cap != 1 {
		t.Errorf("caps = %d/%d", reg.Worker.Cap, reg.Feedback.Cap)
	}
	if reg.Feedback.Name != "feedback-processor" || reg.Worker.Name != "worker" {
		t.Errorf("names = %q/%q", reg.Feedback.Name, reg.Worker.Name)
	}
	if reg.Feedback.Kind != Feedback || reg.Worker.Kind != Worker {
		t.Error("kinds wrong")
	}
}

func TestSessionName(t *testing.T) {
	reg := NewRegistry(config.Default())
	if got := reg.Worker.SessionName("pr-pool-", "zr-lweh.2"); got != "pr-pool-worker-zr-lweh.2" {
		t.Errorf("worker session name = %q", got)
	}
	if got := reg.Feedback.SessionName("pr-pool-", "zr-7"); got != "pr-pool-feedback-processor-zr-7" {
		t.Errorf("feedback session name = %q", got)
	}
}

func TestWorkerNudge_contract(t *testing.T) {
	reg := NewRegistry(withSkills(config.Default(), "/fb.md", "/wk.md"))
	n := reg.Worker.Nudge("zr-w1", "/state/worktrees")
	for _, sub := range []string{
		"/wk.md", "zr-w1", "bd update zr-w1 --claim", "phillipg.",
		"--add-label human", "/state/worktrees", "force-with-lease",
		"bd comment", "NEVER leave the bead in_progress",
	} {
		if !strings.Contains(n, sub) {
			t.Errorf("worker nudge missing %q\n---\n%s", sub, n)
		}
	}
	if strings.Contains(n, "needs-push") {
		t.Errorf("worker nudge must not mention needs-push")
	}
}

func TestFeedbackNudge_contract(t *testing.T) {
	reg := NewRegistry(withSkills(config.Default(), "/fb.md", "/wk.md"))
	n := reg.Feedback.Nudge("zr-c1", "/ignored")
	for _, sub := range []string{"/fb.md", "zr-c1", "open work bead", "child of the PR bead", "Close each feedback"} {
		if !strings.Contains(n, sub) {
			t.Errorf("feedback nudge missing %q\n---\n%s", sub, n)
		}
	}
	if strings.Contains(n, "/exit") {
		t.Errorf("feedback nudge must not mention /exit")
	}
}

func withSkills(c config.Config, fb, wk string) config.Config {
	c.SkillMD, c.WorkerSkillMD = fb, wk
	return c
}
