package main

import (
	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/hygiene"
	"github.com/phillipgreenii/pg-router/internal/handlers/originguard"
)

// wiring is the command's composition root: the collaborators the subcommands
// reach, built once by newWiring and handed down as a receiver, so no
// package-level variable carries them and a test builds its own.
type wiring struct {
	// sweeper carries the hygiene passes (janitor, orphan unit, reconcile,
	// worktree sweeps) and the ports they need from this command.
	sweeper hygiene.Sweeper
	// newGuardCheck builds the pg-origin-guard Check for a config; a test
	// substitutes a fake so no binary on PATH is needed.
	newGuardCheck func(config.Config) originguard.Check
}

// newWiring is the production composition root.
func newWiring() wiring {
	return wiring{
		sweeper:       hygiene.Sweeper{Version: version, Help: helpText},
		newGuardCheck: productionGuardCheck,
	}
}
