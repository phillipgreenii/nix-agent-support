// Command fake-bd is the fake bd of the fast lane test (internal/fakebd). The
// test builds it as `bd` into a directory of its own and puts that directory on
// the PATH of the tools under test. It is never shipped: the pg-decider package
// builds only cmd/pg-decider, cmd/pg-pr-change and cmd/pg-pr-backfill.
package main

import (
	"os"

	"github.com/phillipgreenii/pg-decider/internal/fakebd"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	os.Exit(fakebd.Main(fakebd.Env{Args: os.Args[1:], Dir: dir, Stdout: os.Stdout, Stderr: os.Stderr}))
}
