// run.go: the "run" verb — the real work. Runs the two checks
// (checks.go), applies the "nothing new" dedup rule (dedup.go) against
// pg-connector's own escalated-work query (connector.go), files/updates a
// bd issue on a genuine finding, and signals the caller via one of the
// four documented exit codes [Binding decisions: "Exit codes"
// paragraph]:
//
//	0 clean       -- every sub-check ran cleanly; nothing new to report,
//	                 or every new finding was successfully filed/updated.
//	2 usage error -- a cobra-level flag/arg problem (see main.go's run()).
//	3 total failure -- every sub-check's own ccpool dependency was
//	                 unreachable (neither produced a result); this run
//	                 writes nothing at all -- no snapshot update, no bd
//	                 call [design: "Exit codes" paragraph].
//	4 partial     -- at least one sub-check was attempted and actually
//	                 degraded (its ccpool dependency was unreachable, or
//	                 the pg-connector dedup query itself failed), while at
//	                 least one other sub-check (or the dedup query, when
//	                 reached) produced a result; this run proceeds with
//	                 whatever succeeded, and any bead it files/updates
//	                 carries a note about which sub-check was skipped or
//	                 degraded [design: same paragraph].
//
// Unlike pg-router-probe's own run.go, this binary's two sub-checks are
// never individually "unconfigured" -- both always attempt a ccpool call
// on every invocation, so there is no skipped/degraded split here: a
// sub-check either ran (ranAny=true, no entry in degraded) or it failed
// outright (an entry in degraded). This packet's own implementation
// choice; no design citation for the configured/attempted distinction
// itself (mirroring pg-router-probe's own disclaimer on the same point).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func defaultSnapshotPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".ccpool-probe-snapshot.json"
	}
	return filepath.Join(home, ".local", "state", "ccpool-probe", "snapshot.json")
}

// runOptions is every "run" flag value, gathered before runProbe is
// called.
type runOptions struct {
	ccpoolTimeout      time.Duration
	pgConnectorTimeout time.Duration
	snapshotPath       string
}

// runDeps is every external side effect runProbe performs, gathered into
// one struct so a test can override just the ones it cares about without
// a real ccpool/pg-connector binary or real filesystem snapshot I/O.
// newRunCmd wires the real implementations; run_test.go overrides them
// directly.
type runDeps struct {
	now                 func() time.Time
	listNeedsInput      func(ctx context.Context, warn func(string)) ([]ccpoolSessionRow, error)
	listAllPoolSessions func(ctx context.Context, warn func(string)) ([]ccpoolSessionRow, error)
	listEscalated       func(ctx context.Context, warn func(string)) ([]connectorIssue, error)
	createIssue         func(ctx context.Context, title string, labels []string, metadata map[string]string, description string, warn func(string)) (connectorIssue, error)
	updateMetadata      func(ctx context.Context, id string, metadata map[string]string, warn func(string)) error
	comment             func(ctx context.Context, id, body string, warn func(string)) error
}

func defaultRunDeps() runDeps {
	return runDeps{
		now: time.Now,
		listNeedsInput: func(ctx context.Context, warn func(string)) ([]ccpoolSessionRow, error) {
			return listCcpoolSessions(ctx, "needs_input", warn)
		},
		listAllPoolSessions: func(ctx context.Context, warn func(string)) ([]ccpoolSessionRow, error) {
			return listCcpoolSessions(ctx, "", warn)
		},
		listEscalated:  listEscalated,
		createIssue:    createIssue,
		updateMetadata: updateIssueMetadata,
		comment:        commentIssue,
	}
}

func newRunCmd() *cobra.Command {
	opts := runOptions{
		ccpoolTimeout:      10 * time.Second,
		pgConnectorTimeout: 30 * time.Second,
		snapshotPath:       defaultSnapshotPath(),
	}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the two health checks, filing/updating an escalated bd issue on a real finding",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().DurationVar(&opts.ccpoolTimeout, "ccpool-timeout", opts.ccpoolTimeout, "explicit timeout for each ccpool subprocess call (list)")
	cmd.Flags().DurationVar(&opts.pgConnectorTimeout, "pg-connector-timeout", opts.pgConnectorTimeout, "explicit timeout for each pg-connector subprocess call (list/create/update/comment)")
	cmd.Flags().StringVar(&opts.snapshotPath, "snapshot-path", opts.snapshotPath, "path to this probe's own persisted last-run snapshot")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runProbe(cmd, opts, defaultRunDeps())
	}
	return cmd
}

// runProbe is the orchestration entry point, kept separate from RunE so
// run_test.go can drive it directly with a fake runDeps.
func runProbe(cmd *cobra.Command, opts runOptions, deps runDeps) error {
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()
	warn := func(msg string) { fmt.Fprintln(stderr, "ccpool-probe: "+msg) }

	prevSnap, hadPrev := loadSnapshot(opts.snapshotPath)
	if !hadPrev {
		warn(fmt.Sprintf("no usable prior snapshot at %s; treating as no baseline", opts.snapshotPath))
	}

	// withCcpoolTimeout derives a FRESH, independent deadline for each
	// individual ccpool subprocess call -- "every external call in run
	// MUST carry an explicit timeout" [Binding decisions].
	withCcpoolTimeout := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, opts.ccpoolTimeout)
	}

	var findings []finding
	var degraded []string
	ranAny := false

	// Sub-check 1: ccpool sessions stuck in needs_input.
	niCtx, cancel := withCcpoolTimeout()
	niRows, err := deps.listNeedsInput(niCtx, warn)
	cancel()
	if err != nil {
		degraded = append(degraded, fmt.Sprintf("needs-input: %v", err))
	} else {
		ranAny = true
		findings = append(findings, checkNeedsInput(niRows)...)
	}

	// Sub-check 2: errored/working zombie-count drift.
	zCtx, cancel := withCcpoolTimeout()
	allRows, err := deps.listAllPoolSessions(zCtx, warn)
	cancel()
	// zombieCount/consecutiveGrowth default to the PREVIOUS snapshot's own
	// values -- if this sub-check degrades, the persisted snapshot below
	// must not clobber the last real baseline with a zero it never
	// observed [design: "Snapshot robustness" paragraph, generalized to
	// every run, not only the broken-snapshot-file path].
	zombieCount := prevSnap.ZombieCount
	consecutiveGrowth := prevSnap.ZombieConsecutiveGrowth
	if err != nil {
		degraded = append(degraded, fmt.Sprintf("zombie-drift: %v", err))
	} else {
		ranAny = true
		zombieCount = countZombieSessions(allRows)
		var f *finding
		f, consecutiveGrowth = checkZombieDrift(hadPrev, prevSnap.ZombieCount, zombieCount, prevSnap.ZombieConsecutiveGrowth)
		if f != nil {
			findings = append(findings, *f)
		}
	}

	if !ranAny {
		// Total failure: every sub-check unreachable. Writes nothing --
		// no snapshot update, no bd call [design: "Exit codes" paragraph,
		// "writes nothing"].
		return totalFailureErrorf("run: every sub-check unreachable: %s", strings.Join(degraded, "; "))
	}

	if err := saveSnapshot(opts.snapshotPath, snapshot{
		ZombieCount:             zombieCount,
		ZombieConsecutiveGrowth: consecutiveGrowth,
		CheckedAt:               deps.now().UTC().Format(time.RFC3339),
	}); err != nil {
		warn(fmt.Sprintf("failed to persist snapshot: %v", err))
	}

	skippedNote := strings.Join(degraded, "; ")

	// withPgTimeout derives a FRESH, independent deadline for each
	// individual pg-connector subprocess call below -- same requirement as
	// withCcpoolTimeout above, just applied at the call site here instead
	// of a shared helper, since there are 1..N such calls per run (one
	// list, then one create/update+comment PER finding) rather than one
	// call per sub-check.
	withPgTimeout := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, opts.pgConnectorTimeout)
	}

	if len(findings) > 0 {
		listCtx, cancel := withPgTimeout()
		existing, err := deps.listEscalated(listCtx, warn)
		cancel()
		if err != nil {
			// Not added to `skippedNote` (already computed) -- this run is
			// returning immediately, no bead is filed/updated on this
			// path.
			degraded = append(degraded, fmt.Sprintf("dedup query: %v", err))
			return partialErrorf("run: partial (%s)", strings.Join(degraded, "; "))
		}
		for _, f := range findings {
			action, match := decideAction(f, existing)
			body := renderBody(f, deps.now(), skippedNote)
			switch action {
			case actionCreate:
				createCtx, cancel := withPgTimeout()
				_, err := deps.createIssue(createCtx, escalationTitle(f), []string{"escalated"}, trackedMetadata(f), body, warn)
				cancel()
				if err != nil {
					warn(fmt.Sprintf("failed to create bd issue for %s: %v", f.Fingerprint, err))
				}
			case actionUpdate:
				updateCtx, cancel := withPgTimeout()
				err := deps.updateMetadata(updateCtx, match.ID, trackedMetadata(f), warn)
				cancel()
				if err != nil {
					warn(fmt.Sprintf("failed to update bd issue %s for %s: %v", match.ID, f.Fingerprint, err))
				}
				commentCtx, cancel2 := withPgTimeout()
				err = deps.comment(commentCtx, match.ID, body, warn)
				cancel2()
				if err != nil {
					warn(fmt.Sprintf("failed to comment on bd issue %s for %s: %v", match.ID, f.Fingerprint, err))
				}
			case actionSkip:
				// Nothing new -- no write [Binding decisions: "'Nothing
				// new' rule"].
			}
		}
	}

	if len(degraded) > 0 {
		return partialErrorf("run: partial (%s)", strings.Join(degraded, "; "))
	}
	return nil
}

// escalationTitle renders a short, deterministic title for a brand-new
// bead — no design citation for the exact title text (the body template
// is what design specifies; the title is this packet's own choice).
func escalationTitle(f finding) string {
	return fmt.Sprintf("ccpool-probe: %s", f.Summary)
}
