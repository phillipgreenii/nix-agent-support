//go:build contract

package watchdog

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
)

// Real-bd regression guard for the budget hard stop never reopening a closed
// bead (bead pg2-sx246).
//
// WHY THIS FILE EXISTS (global rule V-3): terminal's release is
// `bd update <id> --status=open --assignee=`, and whether that reopens a
// closed bead is a property of the REAL tool, which the fake runners in
// budgetstop_test.go only impersonate (a mock of bd does not count). These
// tests run the real Watchdog.terminal against a REAL `bd` in a throwaway
// store: the bead is claimed, closed, and only then is the hard stop
// handled; it must still be closed afterwards. A positive control (a bead
// that is still in progress IS released) keeps the guard from passing by the
// release being dead code.
//
// Hermetic store (dtest.NewRealBD): each test runs `bd init` in its own t.TempDir() and asserts
// .beads/metadata.json says "dolt_mode": "embedded", i.e. a private on-disk
// engine owned by that directory. The environment handed to bd drops every
// BEADS_*, BD_* and WORKSPACE_ROOT variable (so nothing can point at this
// machine's shared dolt server) and pins BEADS_DOLT_AUTO_START=0. No server
// is started or stopped and no other beads workspace is read or written.
//
// Behind the `contract` build tag, like the repo's other real-bd suites
// (packages/pg-desk/internal/sync/closure_realbd_contract_test.go): skipped
// when `bd` is not on PATH, and not part of the unit tier. The
// `pg-router-ccpool-handler-bd-contract-test` hook in flake.nix puts it under
// the commit-time and `pg-hooks run pre-land` runs. Run it directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run RealBD ./internal/watchdog/

// realBDActor is the identity both the worker's claim/close and the watchdog's
// own runner use, mirroring production where the handler's runner carries the
// role's ccpool actor (beads.CLIRunner.Actor).
const realBDActor = "sx246-actor"

// newRealBD returns a context and a runner rooted at a fresh private embedded
// bd store (the hermetic setup is shared: dtest.NewRealBD).
func newRealBD(t *testing.T) (context.Context, *beads.CLIRunner) {
	t.Helper()
	return dtest.NewRealBD(t, realBDActor)
}

// claimedBead creates a bead and claims it as the worker, as a dispatch does.
func claimedBead(ctx context.Context, t *testing.T, r *beads.CLIRunner) string {
	t.Helper()
	out, err := r.Run(ctx, "q", "budget hard stop subject")
	if err != nil {
		t.Fatalf("bd q: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		t.Fatalf("bd q returned no id; stdout=%q", out)
	}
	if out, err := r.Run(ctx, "update", id, "--claim"); err != nil {
		t.Fatalf("claim %s: %v\n%s", id, err, out)
	}
	return id
}

func hardStop(ctx context.Context, r *beads.CLIRunner, id string, threshold int) {
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, &fakeCC{list: []ccpool.Session{{ExternalID: "s", CWD: "/repo"}}}, &recBD{}, tokBudget(1000))
	wd.BD = r
	wd.Role = "worker"
	wd.BudgetStopEscalateAfter = threshold
	wd.terminal(ctx, "s", id, stopBE("worker"))
}

func TestTerminal_RealBD_ClosedBeadStaysClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		threshold int
		// preStops is how many OTHER sessions already recorded a budget stop,
		// so this hard stop lands at/above the escalation threshold.
		preStops int
	}{
		{"below threshold", 3, 0},
		{"counting disabled", 0, 0},
		{"at threshold", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, r := newRealBD(t)
			id := claimedBead(ctx, t, r)
			for i := 0; i < tc.preStops; i++ {
				if err := beads.RecordBudgetStop(ctx, r, id, "earlier-"+string(rune('a'+i))); err != nil {
					t.Fatalf("seed budget stop: %v", err)
				}
			}
			if out, err := r.Run(ctx, "close", id); err != nil {
				t.Fatalf("close %s: %v\n%s", id, err, out)
			}
			if st, err := beads.Status(ctx, r, id); err != nil || st != "closed" {
				t.Fatalf("precondition: bead must be closed before the hard stop; status=%q err=%v", st, err)
			}

			hardStop(ctx, r, id, tc.threshold)

			iss, err := beads.ShowObj(ctx, r, id)
			if err != nil {
				t.Fatalf("ShowObj: %v", err)
			}
			if iss.Status != "closed" {
				t.Errorf("a budget hard stop reopened a closed bead: status=%q (want closed)", iss.Status)
			}
			if iss.HasLabel(LabelNeedsSplitReview) || iss.HasLabel(labelHuman) {
				t.Errorf("a closed bead must not be relabeled for escalation; labels=%v", iss.Labels)
			}
		})
	}
}

// Positive control: a bead still held in progress IS released by the hard
// stop, so the closed-bead guard above cannot pass by the release being dead.
func TestTerminal_RealBD_InProgressBeadIsReleased(t *testing.T) {
	ctx, r := newRealBD(t)
	id := claimedBead(ctx, t, r)

	hardStop(ctx, r, id, 3)

	iss, err := beads.ShowObj(ctx, r, id)
	if err != nil {
		t.Fatalf("ShowObj: %v", err)
	}
	if iss.Status != "open" || iss.Assignee != "" {
		t.Errorf("an in-progress bead must be released by the hard stop: status=%q assignee=%q", iss.Status, iss.Assignee)
	}
}
