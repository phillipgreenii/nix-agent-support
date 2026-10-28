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
	code := testenv.Run(m)
	cleanup()
	os.Exit(code)
}
