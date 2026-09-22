// run.go: the "run" verb — the real work. Runs the three checks
// (checks.go), applies the "nothing new" dedup rule (dedup.go) against
// pg-connector's own escalated-work query (connector.go), files/updates a
// bd issue on a genuine finding, and signals the caller via one of the
// four documented exit codes [Binding decisions: "Exit codes"
// paragraph]:
//
//	0 clean       -- every ATTEMPTED sub-check ran cleanly; nothing new to
//	                 report, or every new finding was successfully
//	                 filed/updated. A sub-check this invocation was never
//	                 CONFIGURED to run (see below) does not by itself
//	                 block a clean exit.
//	2 usage error -- a cobra-level flag/arg problem (see main.go's run()).
//	3 total failure -- every sub-check was unreachable/unconfigured (none
//	                 of the three produced a result); this run writes
//	                 nothing at all (no bd issue is filed on a
//	                 total-failure run) [design: "Exit codes" paragraph].
//	4 partial     -- at least one CONFIGURED sub-check was attempted and
//	                 actually degraded (its dependency was unreachable),
//	                 while at least one other sub-check produced a
//	                 result; this run proceeds with whatever succeeded,
//	                 and any bead it files/updates carries a note about
//	                 which sub-check(s) were skipped or degraded [design:
//	                 same paragraph].
//
// Which sub-checks are "configured" at all (Grafana URL set,
// --queue-depth/--backlog both set, --binary-path set) is entirely a
// function of this run invocation's own flags — wiring REAL values into
// those flags in production is the out-of-scope sibling scheduling
// packet's job [Files: "Scheduling this probe through pg-router" Out of
// scope bullet]. Within runProbe below, "skipped" (every reason a
// sub-check contributed nothing, used only for the informational note on
// a filed bead) is intentionally a SUPERSET of "degraded" (a configured
// sub-check that was actually attempted and failed, which is what alone
// drives the partial-vs-clean choice above) — an invocation that simply
// never wires --binary-path, say, has not had that dependency go
// unreachable; it was never asked to check it, so that omission alone
// must not turn an otherwise-clean run into a reported partial failure.
// This packet's own implementation choice; no design citation for the
// configured/attempted distinction itself.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// registeredRuleUIDs are the 4 Grafana rule UIDs this probe is scoped to
// [design: Contract's "Grafana's alerting API" bullet]. Overridable via
// --rule-uid so tests/fixtures never depend on a real Grafana deployment
// having rules by these exact names.
var registeredRuleUIDs = []string{
	"pg-router-liveness-down",
	"pg-router-backlog-growing",
	"pg-router-queue-depth-growing",
	"pg-router-failure-rate",
}

func defaultSnapshotPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".pg-router-probe-snapshot.json"
	}
	return filepath.Join(home, ".local", "state", "pg-router-probe", "snapshot.json")
}

// runOptions is every "run" flag value, gathered before runProbe is
// called.
type runOptions struct {
	grafanaURL     string
	grafanaToken   string
	grafanaTimeout time.Duration
	ruleUIDs       []string

	haveQueueDepth bool
	queueDepth     int
	haveBacklog    bool
	backlog        int

	binaryPath       string
	deployRecordPath string
	snapshotPath     string
}

// runDeps is every external side effect runProbe performs, gathered into
// one struct so a test can override just the ones it cares about without
// a live Grafana server, a real pg-connector binary, or real filesystem
// snapshot I/O. newRunCmd wires the real implementations; run_test.go
// overrides them directly.
type runDeps struct {
	now            func() time.Time
	fetchAlerts    func(ctx context.Context, opts runOptions) ([]grafanaAlert, error)
	listEscalated  func(ctx context.Context, warn func(string)) ([]connectorIssue, error)
	createIssue    func(ctx context.Context, title string, labels []string, metadata map[string]string, description string, warn func(string)) (connectorIssue, error)
	updateMetadata func(ctx context.Context, id string, metadata map[string]string, warn func(string)) error
	comment        func(ctx context.Context, id, body string, warn func(string)) error
}

func defaultRunDeps() runDeps {
	return runDeps{
		now: time.Now,
		fetchAlerts: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			client := newGrafanaClient(opts.grafanaURL, opts.grafanaToken, &http.Client{Timeout: opts.grafanaTimeout})
			gctx, cancel := context.WithTimeout(ctx, opts.grafanaTimeout)
			defer cancel()
			return client.firingAlerts(gctx, opts.ruleUIDs)
		},
		listEscalated:  listEscalated,
		createIssue:    createIssue,
		updateMetadata: updateIssueMetadata,
		comment:        commentIssue,
	}
}

func newRunCmd() *cobra.Command {
	opts := runOptions{
		grafanaTimeout: 10 * time.Second,
		ruleUIDs:       append([]string{}, registeredRuleUIDs...),
		snapshotPath:   defaultSnapshotPath(),
	}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the three health checks, filing/updating an escalated bd issue on a real finding",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&opts.grafanaURL, "grafana-url", "", "Grafana base URL; unset skips the Grafana alerts sub-check")
	cmd.Flags().StringVar(&opts.grafanaToken, "grafana-token", os.Getenv("PG_ROUTER_PROBE_GRAFANA_TOKEN"), "Grafana bearer token (default from PG_ROUTER_PROBE_GRAFANA_TOKEN)")
	cmd.Flags().DurationVar(&opts.grafanaTimeout, "grafana-timeout", opts.grafanaTimeout, "explicit timeout for the Grafana HTTP call")
	cmd.Flags().StringSliceVar(&opts.ruleUIDs, "rule-uid", opts.ruleUIDs, "Grafana rule UID to check (repeatable); defaults to the 4 registered rule UIDs")
	cmd.Flags().IntVar(&opts.queueDepth, "queue-depth", 0, "current queue depth reading; unset (with --backlog) skips the queue/backlog drift sub-check")
	cmd.Flags().IntVar(&opts.backlog, "backlog", 0, "current backlog reading; unset (with --queue-depth) skips the queue/backlog drift sub-check")
	cmd.Flags().StringVar(&opts.binaryPath, "binary-path", "", "path to the daemon/handler binary to hash; unset skips the binary hash sanity sub-check")
	cmd.Flags().StringVar(&opts.deployRecordPath, "deploy-record-file", "", "optional file of known-expected binary hashes, one per line")
	cmd.Flags().StringVar(&opts.snapshotPath, "snapshot-path", opts.snapshotPath, "path to this probe's own persisted last-run snapshot")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		opts.haveQueueDepth = cmd.Flags().Changed("queue-depth")
		opts.haveBacklog = cmd.Flags().Changed("backlog")
		return runProbe(cmd, opts, defaultRunDeps())
	}
	return cmd
}

// runProbe is the orchestration entry point, kept separate from RunE so
// run_test.go can drive it directly with a fake runDeps.
func runProbe(cmd *cobra.Command, opts runOptions, deps runDeps) error {
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()
	warn := func(msg string) { fmt.Fprintln(stderr, "pg-router-probe: "+msg) }

	prevSnap, hadPrev := loadSnapshot(opts.snapshotPath)
	if !hadPrev {
		warn(fmt.Sprintf("no usable prior snapshot at %s; treating as no baseline", opts.snapshotPath))
	}

	// skipped is EVERY reason a sub-check did not contribute a result this
	// run -- both "never configured" and "configured but degraded" -- and
	// is used only for the informational note attached to a filed/updated
	// bead. degraded is the STRICT SUBSET that were actually attempted and
	// failed; only degraded (not mere non-configuration) drives the
	// partial-vs-clean exit code below. This split exists because
	// wiring REAL argv for every sub-check is an out-of-scope sibling
	// packet's job [Files: "Scheduling this probe through pg-router" Out
	// of scope bullet] -- an invocation that simply omits a flag has not
	// had that sub-check's dependency go unreachable, it was never asked
	// to run one, so it must not by itself turn an otherwise-clean run
	// into a reported partial failure.
	var findings []finding
	var skipped []string
	var degraded []string
	ranAny := false

	// Sub-check 1: Grafana firing alerts.
	if opts.grafanaURL == "" {
		skipped = append(skipped, "grafana-alerts: not configured (--grafana-url unset)")
	} else {
		alerts, err := deps.fetchAlerts(ctx, opts)
		if err != nil {
			msg := fmt.Sprintf("grafana-alerts: %v", err)
			skipped = append(skipped, msg)
			degraded = append(degraded, msg)
		} else {
			ranAny = true
			findings = append(findings, checkGrafanaAlerts(alerts)...)
		}
	}

	// Sub-check 2: queue/backlog drift.
	if !opts.haveQueueDepth || !opts.haveBacklog {
		skipped = append(skipped, "queue-backlog-drift: not configured (--queue-depth/--backlog unset)")
	} else {
		ranAny = true
		if f := checkQueueGrowth("queue-depth", hadPrev, prevSnap.QueueDepth, opts.queueDepth); f != nil {
			findings = append(findings, *f)
		}
		if f := checkQueueGrowth("backlog", hadPrev, prevSnap.Backlog, opts.backlog); f != nil {
			findings = append(findings, *f)
		}
	}

	// Sub-check 3: binary hash sanity.
	var currentHash string
	var haveHash bool
	if opts.binaryPath == "" {
		skipped = append(skipped, "binary-hash: not configured (--binary-path unset)")
	} else if h, err := hashFile(opts.binaryPath); err != nil {
		msg := fmt.Sprintf("binary-hash: %v", err)
		skipped = append(skipped, msg)
		degraded = append(degraded, msg)
	} else {
		ranAny = true
		haveHash = true
		currentHash = h
		deployExpected := deployRecordAllows(opts.deployRecordPath, h)
		if f := checkBinaryHash(hadPrev && prevSnap.BinaryHash != "", prevSnap.BinaryHash, h, deployExpected); f != nil {
			findings = append(findings, *f)
		}
	}

	if !ranAny {
		// Total failure: every sub-check unreachable/unconfigured. Writes
		// nothing -- no snapshot update, no bd call [design: "Exit codes"
		// paragraph, "writes nothing"].
		return totalFailureErrorf("run: every sub-check unreachable: %s", strings.Join(skipped, "; "))
	}

	// Persist a fresh snapshot, merging (never clobbering) whatever this
	// run did NOT observe -- an unconfigured sub-check must not erase a
	// value a DIFFERENT invocation is tracking [design: "Snapshot
	// robustness" paragraph, generalized to every successful run, not
	// only the broken-snapshot path].
	next := snapshot{QueueDepth: prevSnap.QueueDepth, Backlog: prevSnap.Backlog, BinaryHash: prevSnap.BinaryHash, CheckedAt: deps.now().UTC().Format(time.RFC3339)}
	if opts.haveQueueDepth {
		next.QueueDepth = opts.queueDepth
	}
	if opts.haveBacklog {
		next.Backlog = opts.backlog
	}
	if haveHash {
		next.BinaryHash = currentHash
	}
	if err := saveSnapshot(opts.snapshotPath, next); err != nil {
		warn(fmt.Sprintf("failed to persist snapshot: %v", err))
	}

	skippedNote := strings.Join(skipped, "; ")

	if len(findings) > 0 {
		existing, err := deps.listEscalated(ctx, warn)
		if err != nil {
			msg := fmt.Sprintf("dedup query: %v", err)
			skipped = append(skipped, msg)
			degraded = append(degraded, msg)
			return partialErrorf("run: partial (%s)", strings.Join(degraded, "; "))
		}
		for _, f := range findings {
			action, match := decideAction(f, existing)
			body := renderBody(f, deps.now(), skippedNote)
			switch action {
			case actionCreate:
				if _, err := deps.createIssue(ctx, escalationTitle(f), []string{"escalated"}, trackedMetadata(f), body, warn); err != nil {
					warn(fmt.Sprintf("failed to create bd issue for %s: %v", f.Fingerprint, err))
				}
			case actionUpdate:
				if err := deps.updateMetadata(ctx, match.ID, trackedMetadata(f), warn); err != nil {
					warn(fmt.Sprintf("failed to update bd issue %s for %s: %v", match.ID, f.Fingerprint, err))
				}
				if err := deps.comment(ctx, match.ID, body, warn); err != nil {
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
	return fmt.Sprintf("pg-router-probe: %s", f.Summary)
}
