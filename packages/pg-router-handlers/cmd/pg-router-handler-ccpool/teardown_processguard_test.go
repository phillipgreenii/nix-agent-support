package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// Real-git proof that the closed-bead teardown keeps a worktree a live process
// holds, and keeps it when the process listing cannot be read, while still
// purging the row; and that it removes the worktree when nobody holds it. The
// repository and worktree are real (temporary); the probe is injected.

func withTeardownProbe(t *testing.T, p cwdProbe) {
	t.Helper()
	old := teardownProbe
	teardownProbe = p
	t.Cleanup(func() { teardownProbe = old })
}

func TestTeardown_processGuardOnTheWorktreeRemoval(t *testing.T) {
	cases := []struct {
		name     string
		probe    func(wt string) cwdProbe
		wantKept bool
	}{
		{"no process holds it", func(string) cwdProbe {
			return func(context.Context) ([]string, error) { return []string{"/elsewhere"}, nil }
		}, false},
		{"a process is inside it", func(wt string) cwdProbe {
			return func(context.Context) ([]string, error) { return []string{filepath.Join(wt, "company", "store")}, nil }
		}, true},
		{"the probe fails", func(string) cwdProbe {
			return func(context.Context) ([]string, error) { return nil, errors.New("lsof: boom") }
		}, true},
	}
	paths := []struct {
		name string
		do   func(t *testing.T, h *sweepHarness, cc *fakeCC, s ccpool.Session) bool
	}{
		{"closeSession (two-phase)", func(t *testing.T, h *sweepHarness, cc *fakeCC, s ccpool.Session) bool {
			return closeSession(t.Context(), cc, h.opener, h.repo, h.wtDir, s, false)
		}},
		{"retryPurgePending", func(t *testing.T, h *sweepHarness, cc *fakeCC, s ccpool.Session) bool {
			s.Meta[ccpool.MetaKeyPurgePending] = purgeMarkerValue
			purged, attempted := retryPurgePending(t.Context(), cc, h.opener, h.repo, h.wtDir, nil, []ccpool.Session{s}, s)
			return purged && attempted
		}},
		{"closeSinglePhase", func(t *testing.T, h *sweepHarness, cc *fakeCC, s ccpool.Session) bool {
			return closeSinglePhase(t.Context(), cc, h.opener, h.repo, s, false)
		}},
	}
	for _, p := range paths {
		for _, tc := range cases {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				h := newSweepHarness(t)
				wt := h.leak("zr-k")
				withTeardownProbe(t, tc.probe(wt))
				s := ccpool.Session{
					ExternalID: "pg-router-worker-zr-k", State: ccpool.StateIdle, CWD: wt,
					Meta: map[string]string{ccpool.MetaKeyBead: "zr-k"},
				}
				cc := &fakeCC{ListSeq: [][]ccpool.Session{{s}}}
				if !p.do(t, h, cc, s) {
					t.Fatal("the row must be purged whether or not the worktree is kept")
				}
				if len(cc.ClosedPurge) == 0 || !cc.ClosedPurge[len(cc.ClosedPurge)-1] {
					t.Errorf("the row must end purged; ClosedPurge=%v", cc.ClosedPurge)
				}
				if kept := h.exists(wt); kept != tc.wantKept {
					t.Errorf("worktree kept = %v, want %v", kept, tc.wantKept)
				}
				if kept := h.branchExists("zr-k"); kept != tc.wantKept {
					t.Errorf("anchor branch kept = %v, want %v", kept, tc.wantKept)
				}
			})
		}
	}
}
