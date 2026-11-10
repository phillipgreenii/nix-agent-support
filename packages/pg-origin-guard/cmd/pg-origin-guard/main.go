// Command pg-origin-guard stops unattended git-auth retries from opening
// browser windows and reports whether auth to a realm is available. See
// docs/contract.md.
package main

import (
	"context"
	"os"

	originguard "github.com/phillipgreenii/pg-origin-guard"
)

func main() {
	os.Exit(originguard.RunCLI(context.Background(), os.Args[1:], originguard.RealDeps()))
}
