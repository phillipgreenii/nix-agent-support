package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
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

// doctorCmd implements `pg-desk doctor`
// [docs/behavior/pg-desk/operator-commands.md]. Exit codes: 0 when every
// check passes; 1 when any check fails (naming which one). The
// stranded-cycle report has nothing to find this phase (sync — the stage
// that would create the beads it looks for — does not run yet), so it is
// printed but never fails the command [that doc's "Out of scope"].
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

	cfg, err := deskConfigLoad(cmd.Context())
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

	if err := doctorConfigValidate(cmd.Context()); err != nil {
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

	// Stranded-cycle report: always 0 by construction this phase (sync does
	// not run yet), never a failure — see this function's doc comment.
	fmt.Fprintln(w, "stranded cycles: 0 (sync not active in Phase 9)")

	if len(failures) > 0 {
		return fmt.Errorf("doctor: %d check(s) failed: %v", len(failures), failures)
	}
	return nil
}
