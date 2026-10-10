// run.go: the "run" verb — the real work. Runs the three checks
// (checks.go), applies the "nothing new" dedup rule (dedup.go) against
// pg-connector's every-non-closed-escalated-bead dedup query (connector.go's
// defaultDedupQuery, NOT the ready-only escalated-work triager query), files/updates a
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
//	                 actually degraded (its dependency was unreachable for
//	                 --degraded-threshold CONSECUTIVE runs, or the
//	                 snapshot/dedup query failed), while at least one
//	                 other sub-check produced a
//	                 result; this run proceeds with whatever succeeded,
//	                 and any bead it files/updates carries a note about
//	                 which sub-check(s) were skipped or degraded [design:
//	                 same paragraph].
//
// A single sub-check failure is NOT enough for exit 4 (pg2-zzf54): a
// timed-out Grafana/status call is retried once, and a sub-check that still
// fails is logged to stderr and counted in the snapshot (snapshot.Degraded).
// Only a sub-check that has failed --degraded-threshold runs in a row
// degrades the exit code. The snapshot-persist and dedup-query failures stay
// immediate: the first disables every later comparison, and the second drops
// findings that a later run could no longer re-derive.
//
// Which sub-checks are "configured" at all (Grafana URL set,
// --queue-depth and/or --backlog/--backlog-from-status set, --binary-path set) is entirely a
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
//
// # Bead labels and the `escalation` alert label (pg2-x7ie2)
//
// Every bead this verb creates carries the label "escalated" (the dedup
// query and the triager both key on it). A Grafana alert rule MAY route its
// beads straight to the operator, skipping the triager, by setting the
// alert label escalation="human": the bead is then created with BOTH
// "escalated" (so dedup still sees it) and "human" (so the triager's
// `--exclude-label human` dispatch query does not). Recognised values:
// "human"; absent or anything else is the default, "escalated" only. The
// `escalation` label is routing metadata, not identity: it is excluded from
// the alert fingerprint (fingerprint.go), so adding it to an
// already-registered rule keeps matching that rule's open beads.
//
// For an escalation=human bead the body also carries a Remediation: section
// rendered from the alert's annotations.summary and annotations.description
// (body.go), ending with the instruction to close the bead once the alert
// clears after remediation: the probe never closes beads. Annotations are
// never part of the fingerprint.
//
// Which rule UIDs are probed at all is registeredRuleUIDs below; adding a
// rule's escalation label does not register it.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// registeredRuleUIDs are the Grafana rule UIDs this probe is scoped to
// [design: Contract's "Grafana's alerting API" bullet]. Overridable via
// --rule-uid so tests/fixtures never depend on a real Grafana deployment
// having rules by these exact names. pg2-p93c0 deleted pg-router-backlog-growing;
// pg2-o6z19 added pg-router-queue-stalled in its place (grafana/alerting/alerts.yaml
// in packages/pg-router). pg2-irowq split the budget-exceeded cause out of
// pg-router-failure-rate into pg-router-budget-stops (pg2-6k0l9 registered it
// here). pg2-fy2pm registered pg-router-upstream-killed (the sustained-only rule
// for killed gh calls, >= 10 in 30m, excluded from pg-router-failure-rate) so a
// persistent GitHub outage still escalates through this path, as the original
// pg-router-failure-rate page did (pg2-m6ei2). pg-router-triager-failures (pg2-u2yub) is deliberately NOT registered:
// the probe files escalated beads/dispatches triagers, so probing triager
// failures could feed a loop (operator decision pending).
var registeredRuleUIDs = []string{
	"pg-router-liveness-down",
	"pg-router-queue-stalled",
	"pg-router-queue-depth-growing",
	"pg-router-failure-rate",
	"pg-router-budget-stops",
	"pg-router-upstream-killed",
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
	// degradedThreshold is how many consecutive runs a sub-check must
	// degrade before the run exits 4; values below 1 act as 1.
	degradedThreshold  int
	grafanaURL         string
	grafanaToken       string
	grafanaTimeout     time.Duration
	pgConnectorTimeout time.Duration
	ruleUIDs           []string

	haveQueueDepth bool
	queueDepth     int
	haveBacklog    bool
	backlog        int

	// backlogFromStatus makes the probe read the backlog itself from
	// `<pgRouterPath> status --json` (sum of queues[].depth, pg-router's
	// MetricBacklog definition) instead of taking a bare --backlog int.
	// pg2-5g9e0: a scheduled role's argv is static, so a live reading
	// cannot be passed as a flag value.
	backlogFromStatus bool
	pgRouterPath      string
	statusTimeout     time.Duration

	binaryPath       string
	deployRecordPath string
	snapshotPath     string

	// dedupQuery is the named pg-connector query the dedup check lists
	// existing beads through; see connector.go's defaultDedupQuery.
	dedupQuery string

	// connectorBackend is the pg-connector backend instance name every
	// pg-connector call passes as --backend (connector.go).
	connectorBackend string

	// closedDedupQuery names a pg-connector query listing recently CLOSED
	// escalated beads (connector.go); "" disables the lookup. A bead
	// created with only a closed match references the newest one.
	closedDedupQuery string
	// stillFiringInterval throttles still-firing comments on an open
	// bead whose alert has not changed episode (dedup.go).
	stillFiringInterval time.Duration
}

// runDeps is every external side effect runProbe performs, gathered into
// one struct so a test can override just the ones it cares about without
// a live Grafana server, a real pg-connector binary, or real filesystem
// snapshot I/O. newRunCmd wires the real implementations; run_test.go
// overrides them directly.
type runDeps struct {
	now            func() time.Time
	fetchAlerts    func(ctx context.Context, opts runOptions) ([]grafanaAlert, error)
	listEscalated  func(ctx context.Context, query string, warn func(string)) ([]connectorIssue, error)
	createIssue    func(ctx context.Context, title string, labels []string, metadata map[string]string, description string, warn func(string)) (connectorIssue, error)
	updateMetadata func(ctx context.Context, id string, metadata map[string]string, warn func(string)) error
	comment        func(ctx context.Context, id, body string, warn func(string)) error
	fetchBacklog   func(ctx context.Context, opts runOptions) (int, error)
}

// defaultRunDeps wires the real implementations; backend is the pg-connector
// backend instance name every connector call passes as --backend
// (runOptions.connectorBackend).
func defaultRunDeps(backend string) runDeps {
	return runDeps{
		now: time.Now,
		fetchAlerts: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			client := newGrafanaClient(opts.grafanaURL, opts.grafanaToken, &http.Client{Timeout: opts.grafanaTimeout})
			gctx, cancel := context.WithTimeout(ctx, opts.grafanaTimeout)
			defer cancel()
			return client.firingAlerts(gctx, opts.ruleUIDs)
		},
		listEscalated: func(ctx context.Context, query string, warn func(string)) ([]connectorIssue, error) {
			return listEscalated(ctx, backend, query, warn)
		},
		createIssue: func(ctx context.Context, title string, labels []string, metadata map[string]string, description string, warn func(string)) (connectorIssue, error) {
			return createIssue(ctx, backend, title, labels, metadata, description, warn)
		},
		updateMetadata: func(ctx context.Context, id string, metadata map[string]string, warn func(string)) error {
			return updateIssueMetadata(ctx, backend, id, metadata, warn)
		},
		comment: func(ctx context.Context, id, body string, warn func(string)) error {
			return commentIssue(ctx, backend, id, body, warn)
		},
		fetchBacklog: fetchBacklogFromStatus,
	}
}

// defaultDegradedThreshold is the --degraded-threshold default: three
// consecutive degraded runs of the same sub-check before exit 4.
const defaultDegradedThreshold = 3

// Sub-check keys in snapshot.Degraded.
const (
	grafanaAlertsKey = "grafana-alerts"
	backlogDriftKey  = "backlog-drift"
	binaryHashKey    = "binary-hash"
)

// isTimeout reports whether err is a deadline expiry: the transient,
// host-load-shaped failure runProbe retries once (pg2-zzf54).
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func newRunCmd() *cobra.Command {
	opts := runOptions{
		degradedThreshold:  defaultDegradedThreshold,
		grafanaTimeout:     10 * time.Second,
		pgConnectorTimeout: 30 * time.Second,
		ruleUIDs:           append([]string{}, registeredRuleUIDs...),
		snapshotPath:       defaultSnapshotPath(),
		pgRouterPath:       "pg-router",
		statusTimeout:      10 * time.Second,
		dedupQuery:         defaultDedupQuery,
		connectorBackend:   defaultPgConnectorBackend,

		stillFiringInterval: 6 * time.Hour,
	}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the three health checks, filing/updating an escalated bd issue on a real finding",
		Long: `Run the three health checks, filing/updating an escalated bd issue on a real finding.

Beads are created with the label "escalated". A firing Grafana alert whose rule
sets the label escalation="human" is filed with labels "escalated" AND "human"
instead: it skips the escalation triager and lands in the operator's queue, with
the alert's summary/description rendered as a Remediation: section. The probe
never closes beads; close one once its alert clears after remediation.`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&opts.grafanaURL, "grafana-url", "", "Grafana base URL; unset skips the Grafana alerts sub-check")
	cmd.Flags().StringVar(&opts.grafanaToken, "grafana-token", os.Getenv("PG_ROUTER_PROBE_GRAFANA_TOKEN"), "Grafana bearer token (default from PG_ROUTER_PROBE_GRAFANA_TOKEN)")
	cmd.Flags().IntVar(&opts.degradedThreshold, "degraded-threshold", opts.degradedThreshold, "consecutive runs a sub-check must degrade (after one retry of a timed-out call) before the run exits 4; an earlier failure is only logged to stderr")
	cmd.Flags().DurationVar(&opts.grafanaTimeout, "grafana-timeout", opts.grafanaTimeout, "explicit timeout for the Grafana HTTP call")
	cmd.Flags().DurationVar(&opts.pgConnectorTimeout, "pg-connector-timeout", opts.pgConnectorTimeout, "explicit timeout for each pg-connector subprocess call (list/create/update/comment)")
	cmd.Flags().StringSliceVar(&opts.ruleUIDs, "rule-uid", opts.ruleUIDs, "Grafana rule UID to check (repeatable); defaults to the registered rule UIDs")
	cmd.Flags().IntVar(&opts.queueDepth, "queue-depth", 0, "current queue depth reading; unset skips the queue-depth drift check (the backlog check is independent)")
	cmd.Flags().IntVar(&opts.backlog, "backlog", 0, "current backlog reading; unset skips the backlog drift check (the queue-depth check is independent)")
	cmd.Flags().BoolVar(&opts.backlogFromStatus, "backlog-from-status", false, "read the backlog from `pg-router status --json` (sum of queues[].depth) instead of --backlog")
	cmd.Flags().StringVar(&opts.pgRouterPath, "pg-router-path", opts.pgRouterPath, "pg-router binary used by --backlog-from-status")
	cmd.Flags().DurationVar(&opts.statusTimeout, "status-timeout", opts.statusTimeout, "explicit timeout for the `pg-router status --json` call")
	cmd.MarkFlagsMutuallyExclusive("backlog", "backlog-from-status")
	cmd.Flags().StringVar(&opts.binaryPath, "binary-path", "", "path to the daemon/handler binary to hash; unset skips the binary hash sanity sub-check")
	cmd.Flags().StringVar(&opts.deployRecordPath, "deploy-record-file", "", "optional file of known-expected binary hashes, one per line")
	cmd.Flags().StringVar(&opts.dedupQuery, "dedup-query", opts.dedupQuery, "named pg-connector query listing every non-closed escalated bead (open, in_progress, blocked, deferred, human-labeled) for the dedup check; MUST NOT be the ready-only triager dispatch query")
	cmd.Flags().StringVar(&opts.closedDedupQuery, "closed-dedup-query", opts.closedDedupQuery, "named pg-connector query listing recently CLOSED escalated beads; a new bead for a re-firing alert whose only match is closed references the newest one. Unset disables the lookup; a failing query degrades the run (exit 4) but the bead is still filed")
	cmd.Flags().DurationVar(&opts.stillFiringInterval, "still-firing-interval", opts.stillFiringInterval, "minimum time between still-firing comments on an open bead for an alert that has not changed episode")
	cmd.Flags().StringVar(&opts.connectorBackend, "connector-backend", opts.connectorBackend, "pg-connector backend instance name passed as --backend on every pg-connector call (a suffixed registration of the beads backend, e.g. pg-connector-issue-beads-zr, once the unsuffixed one is dropped)")
	cmd.Flags().StringVar(&opts.snapshotPath, "snapshot-path", opts.snapshotPath, "path to this probe's own persisted last-run snapshot")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if opts.degradedThreshold < 1 {
			return usageErrorf("run: --degraded-threshold must be at least 1")
		}
		if opts.stillFiringInterval < 0 {
			return usageErrorf("run: --still-firing-interval must not be negative")
		}
		if opts.connectorBackend == "" {
			return usageErrorf("run: --connector-backend must not be empty")
		}
		opts.haveQueueDepth = cmd.Flags().Changed("queue-depth")
		opts.haveBacklog = cmd.Flags().Changed("backlog")
		return runProbe(cmd, opts, defaultRunDeps(opts.connectorBackend))
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
	// retryOnTimeout runs call and, when it failed with a deadline expiry
	// (and the run itself was not cancelled), runs it exactly ONCE more --
	// each call carries its own fresh deadline, so the retry is not starved.
	retryOnTimeout := func(what string, call func() error) error {
		err := call()
		if err != nil && isTimeout(err) && ctx.Err() == nil {
			warn(fmt.Sprintf("%s: timed out; retrying once", what))
			err = call()
		}
		return err
	}

	var findings []finding
	var skipped []string
	// degraded holds failures that exit 4 immediately (snapshot persist,
	// dedup query) plus, once counted below, sub-check failures that reached
	// the consecutive threshold. failures are this run's sub-check failures
	// still to be counted against the threshold.
	var degraded []string
	type subcheckFailure struct{ key, msg string }
	var failures []subcheckFailure
	ranAny := false

	// Sub-check 1: Grafana firing alerts.
	if opts.grafanaURL == "" {
		skipped = append(skipped, "grafana-alerts: not configured (--grafana-url unset)")
	} else {
		var alerts []grafanaAlert
		err := retryOnTimeout(grafanaAlertsKey, func() (e error) {
			alerts, e = deps.fetchAlerts(ctx, opts)
			return e
		})
		if err != nil {
			msg := fmt.Sprintf("grafana-alerts: %v", err)
			skipped = append(skipped, msg)
			failures = append(failures, subcheckFailure{grafanaAlertsKey, msg})
		} else {
			ranAny = true
			findings = append(findings, checkGrafanaAlerts(alerts)...)
		}
	}

	// Sub-check 2: queue/backlog drift.
	// The two readings are independent: either flag alone configures its
	// own check (pg2-5g9e0 -- only --backlog is wired in production; there
	// is no defined single-queue reading for --queue-depth).
	if opts.backlogFromStatus {
		var n int
		err := retryOnTimeout(backlogDriftKey, func() (e error) {
			n, e = deps.fetchBacklog(ctx, opts)
			return e
		})
		if err != nil {
			msg := fmt.Sprintf("backlog-drift: %v", err)
			skipped = append(skipped, msg)
			failures = append(failures, subcheckFailure{backlogDriftKey, msg})
		} else {
			opts.haveBacklog = true
			opts.backlog = n
		}
	}
	if !opts.haveQueueDepth && !opts.haveBacklog {
		if !opts.backlogFromStatus {
			skipped = append(skipped, "queue-backlog-drift: not configured (--queue-depth/--backlog unset)")
		}
	} else {
		ranAny = true
		if opts.haveQueueDepth {
			if f := checkQueueGrowth("queue-depth", hadPrev, prevSnap.QueueDepth, opts.queueDepth); f != nil {
				findings = append(findings, *f)
			}
		}
		if opts.haveBacklog {
			if f := checkQueueGrowth("backlog", hadPrev, prevSnap.Backlog, opts.backlog); f != nil {
				findings = append(findings, *f)
			}
		}
	}

	// Sub-check 3: binary hash sanity.
	var currentHash string
	var currentPath string
	var haveHash bool
	if opts.binaryPath == "" {
		skipped = append(skipped, "binary-hash: not configured (--binary-path unset)")
	} else if h, err := hashFile(opts.binaryPath); err != nil {
		msg := fmt.Sprintf("binary-hash: %v", err)
		skipped = append(skipped, msg)
		failures = append(failures, subcheckFailure{binaryHashKey, msg})
	} else {
		ranAny = true
		haveHash = true
		currentHash = h
		currentPath = resolveBinaryPath(opts.binaryPath)
		deployExpected := deployRecordAllows(opts.deployRecordPath, h)
		prevID := binaryIdentity{Hash: prevSnap.BinaryHash, Path: prevSnap.BinaryPath}
		if f := checkBinaryHash(hadPrev && prevSnap.BinaryHash != "", prevID, binaryIdentity{Hash: h, Path: currentPath}, deployExpected); f != nil {
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
	// Consecutive-degraded bookkeeping (pg2-zzf54): a sub-check that failed
	// this run is one higher than last run's count; one that was attempted
	// and succeeded drops out; one that was not attempted keeps its count
	// (like every other field here, an invocation that never ran a sub-check
	// must not erase what a different invocation tracks). Only counts that
	// reached the threshold degrade the exit code; the rest are logged.
	threshold := max(opts.degradedThreshold, 1)
	nextDegraded := make(map[string]int, len(prevSnap.Degraded)+len(failures))
	for k, n := range prevSnap.Degraded {
		nextDegraded[k] = n
	}
	attempted := []string{}
	if opts.grafanaURL != "" {
		attempted = append(attempted, grafanaAlertsKey)
	}
	if opts.backlogFromStatus {
		attempted = append(attempted, backlogDriftKey)
	}
	if opts.binaryPath != "" {
		attempted = append(attempted, binaryHashKey)
	}
	for _, k := range attempted {
		delete(nextDegraded, k)
	}
	for _, f := range failures {
		n := prevSnap.Degraded[f.key] + 1
		nextDegraded[f.key] = n
		if n >= threshold {
			degraded = append(degraded, f.msg+fmt.Sprintf(" (degraded %d runs in a row)", n))
		} else {
			warn(fmt.Sprintf("sub-check degraded, not yet reported (%d of %d consecutive runs): %s", n, threshold, f.msg))
		}
	}
	if len(nextDegraded) == 0 {
		nextDegraded = nil
	}

	next := snapshot{Degraded: nextDegraded, QueueDepth: prevSnap.QueueDepth, Backlog: prevSnap.Backlog, BinaryHash: prevSnap.BinaryHash, BinaryPath: prevSnap.BinaryPath, CheckedAt: deps.now().UTC().Format(time.RFC3339), Alerts: pruneAlertStates(prevSnap.Alerts, deps.now())}
	if opts.haveQueueDepth {
		next.QueueDepth = opts.queueDepth
	}
	if opts.haveBacklog {
		next.Backlog = opts.backlog
	}
	if haveHash {
		next.BinaryHash = currentHash
		next.BinaryPath = currentPath
	}
	persistFailed := false
	if err := saveSnapshot(opts.snapshotPath, next); err != nil {
		persistFailed = true
		// A snapshot that cannot be persisted silently disables the drift
		// checks (every later run sees "no baseline"; pg2-3gqtw), so this
		// is a degraded sub-check -- exit 4 after the run's findings are
		// filed -- not merely a stderr warning nobody reads.
		warn(fmt.Sprintf("failed to persist snapshot: %v", err))
		degraded = append(degraded, fmt.Sprintf("snapshot-persist: %v", err))
	}

	skippedNote := strings.Join(skipped, "; ")

	// withPgTimeout derives a FRESH, independent deadline for each
	// individual pg-connector subprocess call below -- "every external
	// call in run MUST carry an explicit timeout" [Binding decisions],
	// same requirement as the Grafana HTTP call above, just applied at
	// the call site here instead of inside defaultRunDeps, since there
	// are 1..N such calls per run (one list, then one create/update+
	// comment PER finding) rather than Grafana's single call -- a single
	// shared deadline across all of them would let an early finding's
	// call eat into a later finding's own budget.
	withPgTimeout := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, opts.pgConnectorTimeout)
	}

	if len(findings) > 0 {
		listCtx, cancel := withPgTimeout()
		existing, err := deps.listEscalated(listCtx, opts.dedupQuery, warn)
		cancel()
		if err != nil {
			// Not added to `skipped` -- this run is returning immediately
			// (no bead is filed/updated on this path), so `skippedNote`
			// above (already computed) is never used again either.
			degraded = append(degraded, fmt.Sprintf("dedup query: %v", err))
			return partialErrorf("run: partial (%s)", strings.Join(degraded, "; "))
		}

		// Closed predecessors are looked up lazily, at most once, and only
		// when some finding is about to create a bead. A failure degrades
		// the run (exit 4) but never blocks the create: the bead is filed
		// without the reference.
		var closed []connectorIssue
		closedLoaded := false
		loadClosed := func() []connectorIssue {
			if closedLoaded || opts.closedDedupQuery == "" {
				return closed
			}
			closedLoaded = true
			closedCtx, cancel := withPgTimeout()
			defer cancel()
			list, err := deps.listEscalated(closedCtx, opts.closedDedupQuery, warn)
			if err != nil {
				warn(fmt.Sprintf("closed-dedup query %q failed: %v", opts.closedDedupQuery, err))
				degraded = append(degraded, fmt.Sprintf("closed-dedup query: %v", err))
				return nil
			}
			closed = list
			return closed
		}

		now := deps.now()
		alertStates := make(map[string]alertState, len(next.Alerts))
		for fp, st := range next.Alerts {
			alertStates[fp] = st
		}
		alertsChanged := false
		// noted records a successful note about a grafana fingerprint.
		noted := func(f finding) {
			alertStates[f.Fingerprint] = recordEpisode(alertStates[f.Fingerprint], f.StartsAt, now)
			alertsChanged = true
		}

		for _, f := range findings {
			st, haveState := alertStates[f.Fingerprint]
			action, match := decideAction(f, existing, dedupContext{state: st, haveState: haveState, now: now, stillFiringInterval: opts.stillFiringInterval})
			switch action {
			case actionCreate:
				predecessor := ""
				if c := newestClosedMatch(f, loadClosed()); c != nil {
					predecessor = c.ID
				}
				body := renderBody(f, now, skippedNote, predecessor)
				createCtx, cancel := withPgTimeout()
				_, err := deps.createIssue(createCtx, escalationTitle(f), escalationLabels(f), trackedMetadata(f), body, warn)
				cancel()
				if err != nil {
					warn(fmt.Sprintf("failed to create bd issue for %s: %v", f.Fingerprint, err))
				} else if f.Kind == kindGrafanaAlert {
					noted(f)
				}
			case actionUpdate:
				body := renderBody(f, now, skippedNote, "")
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
			case actionSeed:
				// First tick with no state for an already-open bead
				// (rollout, or a lost snapshot): remember the episode
				// WITHOUT writing to the bead, so deploying this logic
				// cannot burst a comment onto every open escalation.
				alertStates[f.Fingerprint] = recordEpisode(st, f.StartsAt, now)
				alertsChanged = true
			case actionRecurrence, actionStillFiring:
				after := recordEpisode(st, f.StartsAt, now)
				commentCtx, cancel := withPgTimeout()
				err := deps.comment(commentCtx, match.ID, stillFiringComment(f, after, now), warn)
				cancel()
				if err != nil {
					// State stays put, so the next tick retries the note.
					warn(fmt.Sprintf("failed to comment on bd issue %s for %s: %v", match.ID, f.Fingerprint, err))
					continue
				}
				noted(f)
				if action == actionRecurrence {
					updateCtx, cancel2 := withPgTimeout()
					err := deps.updateMetadata(updateCtx, match.ID, recurrenceMetadata(f, match), warn)
					cancel2()
					if err != nil {
						warn(fmt.Sprintf("failed to update bd issue %s for %s: %v", match.ID, f.Fingerprint, err))
					}
				}
			case actionSkip:
				// Nothing new -- no write [Binding decisions: "'Nothing
				// new' rule"].
			}
		}

		// Persist the alert state AFTER the writes it describes, so a
		// failed comment is retried rather than recorded as noted.
		if alertsChanged && !persistFailed {
			next.Alerts = pruneAlertStates(alertStates, now)
			if err := saveSnapshot(opts.snapshotPath, next); err != nil {
				warn(fmt.Sprintf("failed to persist snapshot: %v", err))
				degraded = append(degraded, fmt.Sprintf("snapshot-persist: %v", err))
			}
		}
	}

	if len(degraded) > 0 {
		return partialErrorf("run: partial (%s)", strings.Join(degraded, "; "))
	}
	return nil
}

// escalationLabels returns the labels a brand-new bead is created with.
// Every bead carries "escalated": it is what the dedup query
// (connector.go's defaultDedupQuery, `list --label escalated ...`) matches
// on, so a bead without it would be invisible to dedup and re-created on
// every tick (the pg2-dvkbh/pg2-imr6o duplicate class). A Grafana alert
// whose rule sets the label escalation="human" additionally gets "human",
// which keeps the bead out of the triager's `ready --label escalated
// --exclude-label human` dispatch query and puts it straight in the
// operator's queue (pg2-x7ie2). Any other escalation value (or none) is the
// default, "escalated" only.
func escalationLabels(f finding) []string {
	if f.Kind == kindGrafanaAlert && f.Escalation == escalationHuman {
		return []string{"escalated", "human"}
	}
	return []string{"escalated"}
}

// escalationTitle renders a short, deterministic title for a brand-new
// bead — no design citation for the exact title text (the body template
// is what design specifies; the title is this packet's own choice).
func escalationTitle(f finding) string {
	return fmt.Sprintf("pg-router-probe: %s", f.Summary)
}
