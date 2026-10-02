// Command pg-rescue-bead is a pg-rescue failure handler that defers the work
// by filing a self-contained bead, or annotating an existing one, through
// pg-connector's beads issue backend. It exits 3 (deferred) on success.
package main

import (
	"os"

	"github.com/phillipgreenii/pg-rescue/internal/beadhandler"
)

func main() {
	os.Exit(beadhandler.Main(beadhandler.DefaultRuntime(), os.Args[1:], os.Stdout, os.Stderr))
}
