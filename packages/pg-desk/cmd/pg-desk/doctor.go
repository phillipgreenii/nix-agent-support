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
	Short: "Check config, pg-connector, and serve health",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDoctor(cmd)
	},
}

func init() {
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
	if st, err := deskStoreOpen(); err != nil {
		fmt.Fprintf(w, "stranded cycles: FAIL (open store: %v)\n", err)
		failures = append(failures, "stranded cycles")
	} else {
		stranded, err := doctorStrandedCycles(ctx, cfg, st)
		_ = st.Close()
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

	if len(failures) > 0 {
		return fmt.Errorf("doctor: %d check(s) failed: %v", len(failures), failures)
	}
	return nil
}
