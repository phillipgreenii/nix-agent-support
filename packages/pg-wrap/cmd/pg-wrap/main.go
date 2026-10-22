// Command pg-wrap applies a wrap spec around a payload: run, check and show.
package main

import (
	"os"

	"github.com/phillipgreenii/pg-wrap/internal/cli"
)

func main() {
	os.Exit(cli.Main(cli.Process{
		Args:    os.Args[1:],
		Environ: os.Environ(),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}))
}
