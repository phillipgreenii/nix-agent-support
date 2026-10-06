package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

// defaultServeAddr mirrors serve's own default synopsis
// [docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md's
// "pg-desk serve [--addr 127.0.0.1:9818]"], used only as doctor's fallback
// when config.Config's ServeConfig.Addr is empty.
const defaultServeAddr = "127.0.0.1:9818"

// doctorLookPath, doctorConfigValidate, and doctorServeReachable are
// injectable seams so tests can exercise every branch without a real
// pg-connector binary or a real serve process.
var doctorLookPath = func(name string) (string, error) { return exec.LookPath(name) }

// doctorConfigValidate execs `pg-connector config validate` (the D10
// literal) — its own report covers backend/auth health.
// [packages/pg-connector/cmd/pg-connector/config_validate.go] also
// computes a query-name-coverage check, but per OPERATOR DECISION
// (2026-09-18, bead pg2-rnnfz) that check is informational-only and does
// not affect config validate's exit code, so it cannot fail doctor
// either — doctor does not re-implement or surface it separately.
var doctorConfigValidate = func(ctx context.Context) error {
	return exec.CommandContext(ctx, "pg-connector", "config", "validate").Run()
}

// doctorServeReachable reports whether an HTTP round trip to addr
// succeeds — ANY status code counts as reachable (serve's own
// 503-until-first-interpretation gate still means the process answered);
// only a connection-level failure counts as unreachable.
var doctorServeReachable = func(addr string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/metrics")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// doctorWorkBeadsQuery is the same `issue list --query` name
// internal/gather's gatherWorkBeads and internal/sync's adoption sweep
// already fetch — every anchor/feedback-cycle/review-request bead
// currently on the tracker, classified by title (sync.ClassifyBead).
const doctorWorkBeadsQuery = "work-beads"

// doctorFanOutIssueList execs `pg-connector issue list --query work-beads`
// and returns its raw stdout — an injectable seam (mirrors
// doctorLookPath/doctorConfigValidate/doctorServeReachable above) so tests
// can exercise doctorStrandedCycles without a real pg-connector binary.
// Mirrors internal/gather/gather.go's own run/fanOutCall exactly, scoped
// down to this one call: exit 0 or 2 both return stdout as-is (a fan-out
// op's own per-source degradation detail lives in its "sources[]", which
// this check does not re-inspect); any other outcome is an error. Uses
// cfg's configured beads_dir the same way internal/gather's Gatherer and
// internal/sync's issueClient do (PG_CONNECTOR_ISSUE_BEADS_DIR).
var doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pg-connector", "issue", "list", "--query", doctorWorkBeadsQuery)
	if cfg != nil && len(cfg.Repos) > 0 && cfg.Repos[0].BeadsDir != "" {
		cmd.Env = append(os.Environ(), "PG_CONNECTOR_ISSUE_BEADS_DIR="+cfg.Repos[0].BeadsDir)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr == nil {
		return stdout.Bytes(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 2 {
		return stdout.Bytes(), nil // partial degradation; stdout is still usable
	}
	if errors.As(runErr, &exitErr) {
		return nil, fmt.Errorf("pg-connector issue list --query %s: exit %d: %s", doctorWorkBeadsQuery, exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
	}
	return nil, fmt.Errorf("exec pg-connector issue list --query %s: %w", doctorWorkBeadsQuery, runErr)
}

// doctorWorkBeadEntity is the minimal subset of an `issue list` entity this
// check decodes for itself — hand-decoded independently rather than
// importing packages/pg-connector/pkg/schema, mirroring
// internal/gather/gather.go's own prShowFields/workBeadEntity and
// internal/sync/adoption.go's own workBeadEntity (each package in this
// module hand-decodes its own minimal subset; see gather.go's prShowFields
// doc comment for why).
type doctorWorkBeadEntity struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	State    string            `json:"state"`
	Labels   []string          `json:"labels"`
	Metadata map[string]string `json:"metadata"`
}

type doctorWorkBeadsFanOut struct {
	Entities []doctorWorkBeadEntity `json:"entities"`
}

// doctorHasLabel mirrors internal/sync/rules.go's own hasLabel exactly
// (unexported there — this file hand-implements the one-line check rather
// than exporting it across the package boundary, per this file's own
// hand-decode convention above).
func doctorHasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// doctorStrandedCycles is the pg-desk-native reinterpretation of
// StrandedSelfCycles [recovered pre-deletion from
// packages/pg-router/internal/reconcile/reconcile.go; see docket
// pg2-2j5ac.38's "Operator decision (stranded-cycle implementation path,
// 2026-09-22)"]. pg-desk has no bd-calling capability and MUST NOT gain
// one, so this reads real bead state the same way pg-desk already does
// everywhere else — one `pg-connector issue list --query work-beads`
// fan-out call (doctorFanOutIssueList; the identical query
// internal/gather's/internal/sync's own adoption sweep already issues) —
// and resolves "self" from pg-desk's OWN PR/review-state model (the
// store's interpretation.Ownership column, internal/interpret's
// mine/co-owned values, desk.go's actsAsMine) rather than a parent bead's
// metadata.author (pg-pr's bd-only convention, unavailable here).
//
// Algorithm, ported field-for-field from StrandedSelfCycles: list every
// bead the fan-out returns; keep only OPEN ones whose title identifies a
// feedback cycle (sync.ClassifyBead, kind == sync.KindFeedbackCycle); drop
// any already labeled "mine" (not stranded); drop any this store has no
// interpretation on record for (cannot attribute self — the original
// "no parent bead or unknown self" skip, conservatively kept); for the
// rest, flag as stranded iff the stored interpretation's Ownership acts as
// mine. A fan-out or store failure PROPAGATES rather than being swallowed
// as "nothing stranded" (StrandedSelfCycles' own contract).
func doctorStrandedCycles(ctx context.Context, cfg *config.Config, st *store.Store) ([]string, error) {
	raw, err := doctorFanOutIssueList(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var fanOut doctorWorkBeadsFanOut
	if err := json.Unmarshal(raw, &fanOut); err != nil {
		return nil, fmt.Errorf("decode work-beads fan-out: %w", err)
	}

	var stranded []string
	for _, e := range fanOut.Entities {
		if e.State != "" && e.State != "open" {
			continue
		}
		kind, repo, prNumber, ok := sync.ClassifyBead(e.Title, e.Metadata)
		if !ok || kind != sync.KindFeedbackCycle {
			continue
		}
		if doctorHasLabel(e.Labels, "mine") {
			continue
		}
		entityID := fmt.Sprintf("%s#%d", repo, prNumber)
		interp, found, err := st.GetInterpretation(repo, entityTypePR, entityID)
		if err != nil {
			return nil, fmt.Errorf("read interpretation %s: %w", entityID, err)
		}
		if !found || !actsAsMine(interp.Ownership) {
			continue
		}
		stranded = append(stranded, fmt.Sprintf("%s (bead %s)", entityID, e.ID))
	}
	sort.Strings(stranded)
	return stranded, nil
}

// doctorWorkBeadsReach is the pg2-6w396 guard that the deployed `work-beads`
// query actually reaches the child beads sync adopts and cascades over. The
// query is per-machine config, not code, and the design's own example listed
// only `--type merge-request`, while cycle and review-request beads are
// created as type task (internal/sync/rules.go): with such a query the
// listing carries anchors and never a child, so adoption's crash-safety
// fallback and the closure cascade over improvised children (pg2-kftf9.7)
// silently depend on the ledger alone.
//
// It reads the same fan-out result as doctorStrandedCycles and counts the
// anchors and the feedback-cycle / review-request beads in it. It reports
// reached=false (a warning, never a gate) when the listing holds at least one
// anchor and no child while the ledger holds at least one child row: pg-desk
// has minted children, yet none appears among the open beads. That can also be
// a tracker where every child is genuinely closed, which is why this is an
// observability line and not a failure.
func doctorWorkBeadsReach(ctx context.Context, cfg *config.Config, st *store.Store) (anchors, children, ledgerChildren int, reached bool, err error) {
	raw, err := doctorFanOutIssueList(ctx, cfg)
	if err != nil {
		return 0, 0, 0, false, err
	}
	var fanOut doctorWorkBeadsFanOut
	if err := json.Unmarshal(raw, &fanOut); err != nil {
		return 0, 0, 0, false, fmt.Errorf("decode work-beads fan-out: %w", err)
	}
	for _, e := range fanOut.Entities {
		kind, _, _, ok := sync.ClassifyBead(e.Title, e.Metadata)
		if !ok {
			continue
		}
		switch kind {
		case sync.KindAnchor:
			anchors++
		case sync.KindFeedbackCycle, sync.KindReviewRequest:
			children++
		}
	}
	rows, err := st.ListLedger()
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("read ledger: %w", err)
	}
	for _, r := range rows {
		if (r.Kind == sync.KindFeedbackCycle || r.Kind == sync.KindReviewRequest) && r.BeadID != "" {
			ledgerChildren++
		}
	}
	reached = !(anchors > 0 && children == 0 && ledgerChildren > 0)
	return anchors, children, ledgerChildren, reached, nil
}

// doctorSyncErrors lists every interpretation row with a non-empty
// sync_error as "entity: error [retry indicator]" (pg2-kftf9.5; the
// indicator is bead pg2-xb6fs's, see describeSyncRetry).
func doctorSyncErrors(st *store.Store) ([]string, error) {
	interps, err := st.ListInterpretations()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, i := range interps {
		if i.SyncError == "" {
			continue
		}
		r, found, err := st.GetSyncRetry(i.EntityID)
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s: %s [%s]", i.EntityID, i.SyncError, describeSyncRetry(r, found)))
	}
	return out, nil
}

// describeSyncRetry renders a sync_error row's retry indicator so an
// operator can tell a row that is still being retried from one that no
// longer will be (bead pg2-xb6fs).
func describeSyncRetry(r store.SyncRetry, found bool) string {
	if !found {
		return "retrying: awaiting its first automatic retry"
	}
	switch r.EffectiveState() {
	case store.SyncRetryExhausted:
		return fmt.Sprintf("exhausted: %d/%d retries failed, no further automatic retry; fix the cause, then pg-desk reconcile --retry-all", r.Retries(), r.MaxRetries)
	case store.SyncRetryNonTransient:
		return "non-transient: not retried automatically; fix the cause, then pg-desk reconcile --retry-all"
	default:
		next := r.NextRetryAt
		if next == "" {
			next = "the next reconcile"
		}
		return fmt.Sprintf("retrying: %d/%d retries used, next retry at %s", r.Retries(), r.MaxRetries, next)
	}
}

// doctorCmd implements `pg-desk doctor`
// [docs/behavior/pg-desk/operator-commands.md]. Exit codes: 0 when every
// check passes; 1 when any check fails (naming which one). The
// stranded-cycle check reports real data (doctorStrandedCycles) — a
// self-owned open feedback cycle missing the "mine" label a downstream
// bd-label-based discovery would otherwise rely on; see that function's
// doc comment. A failure to compute the report (store or pg-connector
// unavailable) fails the command like every other check here; finding
// stranded cycles does not — this is an observability report, not a gate
// [pg2-2j5ac.38.6's own Contract: "READ-ONLY observability guard ...
// never changes discovery behavior"].
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check config, pg-connector, serve, and change-flow health",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDoctor(cmd)
	},
}

func init() {
	doctorCmd.Flags().String("router-config", "", "path to the pg-router config (read as a file): evaluates the sweep sizing bound, ties each consumer to its router period, and lists the decider roles bound per type")
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command) error {
	w := cmd.OutOrStdout()
	var failures []string
	ctx := cmd.Context()

	cfg, err := deskConfigLoad(ctx)
	if err != nil {
		fmt.Fprintf(w, "config: FAIL (%v)\n", err)
		failures = append(failures, "config")
	} else {
		fmt.Fprintln(w, "config: ok")
	}

	if path, err := doctorLookPath("pg-connector"); err != nil {
		fmt.Fprintf(w, "pg-connector on PATH: FAIL (%v)\n", err)
		failures = append(failures, "pg-connector on PATH")
	} else {
		fmt.Fprintf(w, "pg-connector on PATH: ok (%s)\n", path)
	}

	if err := doctorConfigValidate(ctx); err != nil {
		fmt.Fprintf(w, "pg-connector config validate: FAIL (%v)\n", err)
		failures = append(failures, "pg-connector config validate")
	} else {
		fmt.Fprintln(w, "pg-connector config validate: ok")
	}

	addr := defaultServeAddr
	if cfg != nil && cfg.Serve.Addr != "" {
		addr = cfg.Serve.Addr
	}
	if err := doctorServeReachable(addr); err != nil {
		fmt.Fprintf(w, "serve reachable (%s): FAIL (%v)\n", addr, err)
		failures = append(failures, "serve reachable")
	} else {
		fmt.Fprintf(w, "serve reachable (%s): ok\n", addr)
	}

	// Stranded-cycle report (doctorStrandedCycles's own doc comment). A
	// failure to compute it is reported like every other check above;
	// finding stranded cycles is not itself a failure (this is an
	// observability report, not a gate).
	//
	// doctor opens the store RAW (no migrations, no version gate) so it can
	// inspect a store in any schema state — including one that has not been
	// cut over yet, or has no schema at all — instead of failing to open it.
	// An unmigrated store is reported by the change_flow section below.
	if st, err := deskStoreOpenRaw(); err != nil {
		fmt.Fprintf(w, "stranded cycles: FAIL (open store: %v)\n", err)
		failures = append(failures, "stranded cycles")
	} else if version, verr := st.SchemaVersion(); verr != nil {
		_ = st.Close()
		fmt.Fprintf(w, "stranded cycles: FAIL (read schema version: %v)\n", verr)
		failures = append(failures, "stranded cycles")
	} else if version == 0 {
		// No schema at all (a store that has never been written): there are
		// no interpretation rows to be stranded or carry a sync_error, and
		// querying would fail on the missing tables.
		_ = st.Close()
		fmt.Fprintln(w, "sync_error rows: 0")
		fmt.Fprintln(w, "stranded cycles: 0")
	} else {
		stranded, err := doctorStrandedCycles(ctx, cfg, st)
		// sync_error check (pg2-kftf9.5): a row with a recorded sync_error
		// is a PR whose bead sync failed and may have leaked open beads;
		// unlike stranded cycles this IS a gate (exit 1) so an alert can
		// hang off the doctor exit code.
		syncErrs, syncErrCheck := doctorSyncErrors(st)
		reachAnchors, reachChildren, reachLedger, reachOK, reachErr := doctorWorkBeadsReach(ctx, cfg, st)
		_ = st.Close()
		switch {
		case reachErr != nil:
			fmt.Fprintf(w, "work-beads reach: skipped (%v)\n", reachErr)
		case !reachOK:
			fmt.Fprintf(w, "work-beads reach: WARN (%d anchors and no feedback-cycle or review-request bead listed, though the ledger holds %d child rows; the work-beads query probably excludes type task, so adoption and the closure cascade see only the ledger: see sync.md)\n", reachAnchors, reachLedger)
		default:
			fmt.Fprintf(w, "work-beads reach: ok (%d anchors, %d child beads listed)\n", reachAnchors, reachChildren)
		}
		if syncErrCheck != nil {
			fmt.Fprintf(w, "sync_error rows: FAIL (%v)\n", syncErrCheck)
			failures = append(failures, "sync_error rows")
		} else if len(syncErrs) > 0 {
			fmt.Fprintf(w, "sync_error rows: FAIL (%d non-empty)\n", len(syncErrs))
			for _, s := range syncErrs {
				fmt.Fprintf(w, "  - %s\n", s)
			}
			failures = append(failures, "sync_error rows")
		} else {
			fmt.Fprintln(w, "sync_error rows: 0")
		}
		if err != nil {
			fmt.Fprintf(w, "stranded cycles: FAIL (%v)\n", err)
			failures = append(failures, "stranded cycles")
		} else {
			fmt.Fprintf(w, "stranded cycles: %d\n", len(stranded))
			for _, s := range stranded {
				fmt.Fprintf(w, "  - %s\n", s)
			}
		}
	}

	// Change-flow checks [design: 11]. An unreadable --router-config is
	// itself a failed check, but the checks that do not need it still run.
	var rc *routerConfig
	if path, _ := cmd.Flags().GetString("router-config"); path != "" {
		loaded, err := loadRouterConfig(path)
		if err != nil {
			fmt.Fprintf(w, "router config: FAIL (%v)\n", err)
			failures = append(failures, "router config")
		} else {
			rc = loaded
			fmt.Fprintf(w, "router config: ok (%s)\n", path)
		}
	}
	doctorChangeFlow(ctx, w, cfg, rc, &failures)

	if len(failures) > 0 {
		return fmt.Errorf("doctor: %d check(s) failed: %v", len(failures), failures)
	}
	return nil
}
