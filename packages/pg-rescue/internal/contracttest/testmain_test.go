package contracttest

import (
	"fmt"
	"os"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

// helperBin is the fake handler, internal/fakes/contractfake, built once
// without the race detector (a race-built child sleeps 1.0 s at exit).
var helperBin string

// TestMain isolates the test process like every other package. The fake
// handler is built BEFORE testenv.Run, which replaces HOME (and with it the Go
// build cache) with an empty temp dir.
func TestMain(m *testing.M) {
	bin, cleanup, err := testenv.BuildHelper("github.com/phillipgreenii/pg-rescue/internal/fakes/contractfake")
	if err != nil {
		fmt.Fprintln(os.Stderr, "contracttest tests:", err)
		os.Exit(1)
	}
	helperBin = bin
	code := testenv.Run(m)
	cleanup()
	os.Exit(code)
}
