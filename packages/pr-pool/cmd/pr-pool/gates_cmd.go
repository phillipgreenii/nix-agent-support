package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

// gateQuotaPaused / gateCICDDown are the two named gates INV-LIFE-2 defines
// (docs/behavior/invariants.md's "Gate identity"): quota-paused is ACTOR-OP's
// own to set and clear; cicd-down belongs to an automation actor. Omitting a
// gate name to `pause`/`resume` defaults to quota-paused.
const (
	gateQuotaPaused = "quota-paused"
	gateCICDDown    = "cicd-down"
)

// validGate reports whether name is one of the two named gates.
func validGate(name string) bool {
	return name == gateQuotaPaused || name == gateCICDDown
}

// gatePath resolves an already-validated gate name to its file path.
func gatePath(gate, quotaPaused, cicdDown string) string {
	if gate == gateCICDDown {
		return cicdDown
	}
	return quotaPaused
}

// runPause implements `pr-pool pause [<gate>]` against the real process
// stdout/stderr; pauseGate below carries the testable logic.
func runPause(gate string) int {
	return pauseGate(os.Stdout, os.Stderr, gate)
}

// runResume implements `pr-pool resume [<gate>] | --all` against the real
// process stdout/stderr; resumeGate below carries the testable logic.
func runResume(gate string, allGates bool) int {
	return resumeGate(os.Stdout, os.Stderr, gate, allGates)
}

// pauseGate is runPause's testable body (interfaces.md's "Operator
// pause/resume", INV-LIFE-2): it sets gate's file-backed state directly.
//
// It deliberately uses config.GatePaths(), never config.Load(): Load()'s
// Validate() hard-fails on an unrunnable backing command (INV-WORKFLOW-1
// check 5), which would break the MUST that pause succeeds even against a
// config that could never itself Load() — pause/resume act on gate-file state
// alone and never need the rest of the configuration to be valid. This is
// also what makes the "exits 0 even with no core running" MUST possible:
// nothing here Discovers or Dials a core (ADR 0036 / core.ErrNoRunningCore).
func pauseGate(stdout, stderr io.Writer, gate string) int {
	quotaPaused, cicdDown := config.GatePaths()
	path := gatePath(gate, quotaPaused, cicdDown)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(stderr, "pause:", err)
		return exitGeneric
	}
	// Re-pause is idempotent-visible but MUST NOT touch an already-set gate's
	// mtime: a second `pause` must report the ORIGINAL set time, never reset it.
	if fi, err := os.Stat(path); err == nil {
		fmt.Fprintf(stdout, "pr-pool: already paused (%s since %s)\n", gate, fi.ModTime().Format("15:04"))
		return exitOK
	}
	now := time.Now()
	body := fmt.Sprintf("paused by `pr-pool pause %s` at %s\n", gate, now.UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		fmt.Fprintln(stderr, "pause:", err)
		return exitGeneric
	}
	fmt.Fprintf(stdout, "pr-pool: paused (%s since %s) — takes effect at the next start; a currently running \"run\" picks it up on its next tick\n",
		gate, now.Format("15:04"))
	return exitOK
}

// resumeGate is runResume's testable body: it clears gate's file-backed
// state, or — with allGates — every gate outstanding in one call. Same
// file-direct, no-core-required, never-Discover/Dial mechanics as pauseGate;
// see its doc comment for why config.GatePaths() and not config.Load().
func resumeGate(stdout, stderr io.Writer, gate string, allGates bool) int {
	quotaPaused, cicdDown := config.GatePaths()
	if allGates {
		var cleared []string
		for _, g := range []struct{ name, path string }{
			{gateQuotaPaused, quotaPaused},
			{gateCICDDown, cicdDown},
		} {
			removed, err := removeGateFile(g.path)
			if err != nil {
				fmt.Fprintln(stderr, "resume:", err)
				return exitGeneric
			}
			if removed {
				cleared = append(cleared, g.name)
			}
		}
		if len(cleared) == 0 {
			fmt.Fprintln(stdout, "pr-pool: already resumed (no gate was set)")
		} else {
			fmt.Fprintf(stdout, "pr-pool: resumed (cleared %s)\n", strings.Join(cleared, ", "))
		}
		return exitOK
	}
	path := gatePath(gate, quotaPaused, cicdDown)
	removed, err := removeGateFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "resume:", err)
		return exitGeneric
	}
	if removed {
		fmt.Fprintf(stdout, "pr-pool: resumed (%s cleared)\n", gate)
	} else {
		fmt.Fprintf(stdout, "pr-pool: already resumed (%s was not set)\n", gate)
	}
	return exitOK
}

// removeGateFile removes path if present, reporting whether it actually
// existed — so the caller can distinguish "cleared" from "already resumed"
// (both exit 0; this is reporting only, never a failure).
func removeGateFile(path string) (removed bool, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, statErr
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
