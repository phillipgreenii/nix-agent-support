package runner_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

func TestMain(m *testing.M) {
	// The fake command, handler and verify are internal/fakes/runnercmd, built
	// once without the race detector. Build it BEFORE testenv.Run, which
	// replaces HOME (and with it the Go build cache) with an empty temp dir.
	bin, cleanup, err := testenv.BuildHelper("github.com/phillipgreenii/pg-rescue/internal/fakes/runnercmd")
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner tests:", err)
		os.Exit(1)
	}
	helperBin = bin
	// Any race-built process the tests start sleeps 1.0 s at exit unless told
	// not to (bead pg2-jrnkva, ADR 0111). The supervised children inherit this
	// variable through the production ChainExecutor (the wrapper's environment
	// is the base of every handler's); it affects only processes the tests
	// start, never how `go test -race` runs this binary.
	setRaceExitSleepOff()
	code := testenv.Run(m)
	cleanup()
	os.Exit(code)
}

// raceExitSleepOff is the GORACE setting that stops a race-built process from
// sleeping 1.0 s at exit.
const raceExitSleepOff = "atexit_sleep_ms=0"

// setRaceExitSleepOff adds raceExitSleepOff to GORACE (keeping any options the
// caller already set) so every process the tests start inherits it. It does not
// change whether the tests run under -race.
func setRaceExitSleepOff() {
	old := os.Getenv("GORACE")
	if old == "" {
		_ = os.Setenv("GORACE", raceExitSleepOff)
		return
	}
	_ = os.Setenv("GORACE", old+" "+raceExitSleepOff)
}
