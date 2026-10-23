// Package cliutil holds the small CLI helpers every adapter binary of this
// module shares (bead pg2-12gr4.10.3).
package cliutil

import (
	"fmt"
	"os"
)

// PrintUsageErr writes a usage diagnostic and the adapter's short usage line to
// stderr, mirroring cmd/pg-router/main.go's own printUsageErr. Each adapter
// passes its own usage line.
func PrintUsageErr(msg, usageLine string) {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr, usageLine)
}
