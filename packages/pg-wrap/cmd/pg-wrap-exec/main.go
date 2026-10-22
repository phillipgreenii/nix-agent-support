// Command pg-wrap-exec is pg-wrap for a caller that lets you replace only a
// program path. It takes the spec path from PG_WRAP_SPEC and execs the spec's
// exec prefix followed by every argument it was given; a leading run or check is
// just an argument.
package main

import (
	"os"

	"github.com/phillipgreenii/pg-wrap/internal/cli"
)

func main() {
	os.Exit(cli.ExecMain(cli.Process{
		Args:    os.Args[1:],
		Environ: os.Environ(),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}))
}
