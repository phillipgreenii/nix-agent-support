package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	// Blank-imported (Task 4.2): this packet's job is to add these three
	// pinned deps to go.mod/gomod2nix.toml before any real consumer exists —
	// Task 4.3 (internal/tui/render) is the first consumer of
	// lipgloss/colorprofile, Task 4.5 (internal/tui) of bubbletea. With
	// nothing importing them yet, a plain `go mod tidy` (Global Constraints'
	// "New Go deps" step, run as part of this packet's own validation) would
	// immediately prune them again; a blank import pins the exact version
	// without pretending this stub consumes it.
	_ "github.com/charmbracelet/bubbletea"
	_ "github.com/charmbracelet/colorprofile"
	_ "github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pr-pool/conformance"
)

// envTUIInterval is PR_POOL_TUI_INTERVAL, spelled out as its own constant
// (mirroring envSocket/envToken in ingest_event.go) since runTUI is its only
// reader.
const envTUIInterval = "PR_POOL_TUI_INTERVAL"

// tuiIntervalDefault is the TUI poller's built-in refresh interval absent any
// PR_POOL_TUI_INTERVAL override (spec §11, comp-8's env-vs-default half of
// the precedence: CLI flag (none exists yet) > env > this default).
const tuiIntervalDefault = 1 * time.Second

// tuiIntervalFloor is the fastest poll interval resolveTUIInterval will ever
// return. A faster interval would hammer the core's status/mon.read
// admission semaphore (Task 3.10) for no operator-visible benefit, so a
// too-small override is silently clamped UP to this floor rather than
// rejected as a usage error.
const tuiIntervalFloor = 250 * time.Millisecond

// runTUI implements `pr-pool tui [--socket ...] [--token ...]` (Task 4.2):
// the operator front door onto the continuous-interactive view
// interfaces.md's "tui is not a sixth affordance" describes — polling the
// same status read and offering pause/resume from one screen, never a third
// distinct affordance.
//
// It follows status_cmd.go's runStatus shape exactly (flag parsing,
// --socket/--token overrides, locateCore), with ONE deliberate divergence:
// unlike every other operator subcommand, it NEVER fails when no core can be
// located (ADR 0036). With none discoverable, internal/tui renders its own
// dedicated no-core screen and keeps polling — the poller decides when/
// whether a core is up, not this route — so locateCore's error is
// intentionally left unchecked here.
//
// Since internal/tui does not exist yet (the sibling packet covering Task
// 4.5 builds it), the hand-off to tui.Run is stubbed with a TODO and this
// always returns success once argument/interval parsing succeeds.
func runTUI(args []string) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	socket := fs.String("socket", "", "path to the running core's socket (overrides discovery)")
	token := fs.String("token", "", "auth token for the running core (with --socket)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println(helpText)
		return exitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "tui:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "tui: unexpected argument:", fs.Arg(0))
		return conformance.ExitUsage
	}

	interval, err := resolveTUIInterval(os.Getenv(envTUIInterval))
	if err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		return conformance.ExitUsage
	}

	// locateCore resolves the same injected/discovered ref every other
	// operator subcommand uses, but — deliberately, per this function's own
	// doc — its error is NOT checked: a "no running core" outcome is
	// unremarkable input to the poller, not a CLI failure.
	ref, _ := locateCore(*socket, *token)

	// TODO(Task 4.5): construct the poller from ref/interval and hand off to
	// tui.Run (internal/tui), which renders the loading/no-core/quiescing/
	// main screens and keeps polling on ErrNoRunningCore (ADR 0036) instead
	// of exiting. internal/tui does not exist yet, so this is a stub that
	// always succeeds.
	_ = ref
	_ = interval
	return exitOK
}

// resolveTUIInterval implements spec §11 (comp-8)'s env-vs-default half of
// the TUI poll interval precedence (the CLI-flag half is not this function's
// concern — none exists yet): PR_POOL_TUI_INTERVAL, if set and parseable,
// wins over tuiIntervalDefault; a parsed value under tuiIntervalFloor is
// floor-clamped up rather than rejected (too-fast is a footgun against the
// core's read admission semaphore, not an operator mistake worth failing on);
// a value that fails to parse as a duration IS a usage error, naming the bad
// value so the operator can see exactly what environment content produced
// it.
func resolveTUIInterval(envVal string) (time.Duration, error) {
	if envVal == "" {
		return tuiIntervalDefault, nil
	}
	d, err := time.ParseDuration(envVal)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", envTUIInterval, envVal, err)
	}
	if d < tuiIntervalFloor {
		return tuiIntervalFloor, nil
	}
	return d, nil
}
