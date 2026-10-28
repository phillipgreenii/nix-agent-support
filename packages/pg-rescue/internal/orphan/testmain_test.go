package orphan

import (
	"fmt"
	"os"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

// helperBin is internal/fakes/orphanwatch, built once without the race
// detector (a race-built child sleeps 1.0 s at exit).
var helperBin string

func TestMain(m *testing.M) {
	// Build the helper BEFORE testenv.Run, which replaces HOME (and with it the
	// Go build cache) with an empty temp dir.
	bin, cleanup, err := testenv.BuildHelper("github.com/phillipgreenii/pg-rescue/internal/fakes/orphanwatch")
	if err != nil {
		fmt.Fprintln(os.Stderr, "orphan tests:", err)
		os.Exit(1)
	}
	helperBin = bin
	code := testenv.Run(m)
	cleanup()
	os.Exit(code)
}
