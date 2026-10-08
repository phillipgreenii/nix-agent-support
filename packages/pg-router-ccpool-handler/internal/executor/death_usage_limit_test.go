package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
)

// slowCapacityCC is a FakeCC whose Capacity blocks until its context ends,
// standing in for a ccpool that hangs.
type slowCapacityCC struct{ *dtest.FakeCC }

func (s slowCapacityCC) Capacity(ctx context.Context) (ccpool.Capacity, error) {
	<-ctx.Done()
	return ccpool.Capacity{}, ctx.Err()
}

// TestDispatch_unexplainedDeath_logsUsageLimitReading (INV-CCH-26): the
// unexplained-death branch of waitDone records the ccpool usage-limit state as
// evidence, and nothing else about the death changes: the failure text and the
// incomplete marker are the same whatever the reading returns, and a failing or
// hanging reading never holds up the failure.
func TestDispatch_unexplainedDeath_logsUsageLimitReading(t *testing.T) {
	resets := time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		cap      ccpool.Capacity
		capErr   error
		slow     bool
		wantLog  []string
		wantNone []string
	}{
		{
			name:     "usage limit present",
			cap:      ccpool.Capacity{MaxSessions: 4, UsageLimit: &ccpool.UsageLimit{Window: "five_hour", UsedPct: 100, ResetsAt: resets}},
			wantLog:  []string{"session died; usage-limit reading", "usage_limit_present=true", "window=five_hour", "used_pct=100", "resets_at=2026-10-08T18:00:00.000Z"},
			wantNone: []string{"usage_limit_reading_error"},
		},
		{
			name:     "usage limit absent",
			cap:      ccpool.Capacity{MaxSessions: 4, Free: 2},
			wantLog:  []string{"session died; usage-limit reading", "usage_limit_present=false"},
			wantNone: []string{"window=", "usage_limit_reading_error"},
		},
		{
			name:     "capacity read fails",
			capErr:   errors.New("ccpool capacity: boom"),
			wantLog:  []string{"usage_limit_reading_error", "ccpool capacity: boom"},
			wantNone: []string{"session died; usage-limit reading"},
		},
		{
			name:     "capacity read hangs past the timeout",
			slow:     true,
			wantLog:  []string{"usage_limit_reading_error", "context deadline exceeded"},
			wantNone: []string{"session died; usage-limit reading"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			cfg := fastCfg()
			cfg.WorktreeDir = t.TempDir()
			role := noBudget(reviewRole(cfg))
			display := role.DisplayName(cfg.SessionPrefix, "bead-1")
			dead := ccpool.Session{
				ExternalID: "att-1", Name: display, Live: false, State: ccpool.StateIdle, CloseReason: "handler",
				Meta: map[string]string{ccpool.MetaKeyEventID: "review.ready:bead-1", ccpool.MetaKeyHeadSHA: "h1"},
			}
			bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bead-1": {"open"}}}
			fake := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{dead}}, Cap: tc.cap, CapErr: tc.capErr}
			var cc ccpool.Runner = fake
			if tc.slow {
				cc = slowCapacityCC{fake}
			}
			d := DispatchContext{
				Role: role, EventID: "review.ready:bead-1",
				Item: item.Item{ID: "bead-1", Metadata: map[string]any{"repo": "example/repo", "pr_number": "7", "head_sha": "h1"}},
			}
			deps := newExec(fake, bd, cfg).deps
			deps.CC = cc
			deps.ExternalID = "att-2"
			deps.Git = &dtest.NoopGit{}
			deps.GitOpener = (&dtest.NoopGitOpener{}).Open

			start := time.Now()
			_, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
			elapsed := time.Since(start)

			if err == nil || err.Error() != "bead-1: session exited before completing" {
				t.Errorf("the failure must be unchanged; err=%v", err)
			}
			if elapsed > deathUsageLimitTimeout+3*time.Second {
				t.Errorf("the reading must not hold up the failure; took %s", elapsed)
			}
			stamped := false
			for _, m := range fake.SetMetaCalls() {
				stamped = stamped || (m.ExternalID == "att-1" && m.Key == ccpool.MetaKeyIncomplete)
			}
			if !stamped {
				t.Errorf("the row must still be stamped incomplete; sets=%v", fake.SetMetaCalls())
			}
			out := logs.String()
			for _, w := range tc.wantLog {
				if !strings.Contains(out, w) {
					t.Errorf("log must contain %q; logs:\n%s", w, out)
				}
			}
			for _, w := range tc.wantNone {
				if strings.Contains(out, w) {
					t.Errorf("log must not contain %q; logs:\n%s", w, out)
				}
			}
		})
	}
}
