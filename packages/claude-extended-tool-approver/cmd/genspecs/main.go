// Command genspecs is Phase 1 packet 1.2's (tc-o14i5.2.2) re-runnable
// generator: regenerate internal/embeddedspecs/data/*.json from the live
// cmddesc.DefaultRegistry() whenever the registry changes. Equivalent to
// `CETA_EMBEDDEDSPECS_WRITE=1 go test ./internal/embeddedspecs/... -run
// TestGenerate` (see that test's own env-gate doc comment for why a bare
// `go test` must not write); this binary exists so the same regeneration
// can run without invoking the test runner, and so `go build ./...` has a
// real command to compile it into. Either way, run this repo's formatter/
// prek over internal/embeddedspecs/data/ afterward before committing.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/embeddedspecs"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "genspecs:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("genspecs", flag.ContinueOnError)
	dir := fs.String("dir", "internal/embeddedspecs/data", "directory to (re)write the embedded command-spec JSON files into (default assumes running from the claude-extended-tool-approver module root)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	specs, err := embeddedspecs.BuildSpecs(cmddesc.DefaultRegistry())
	if err != nil {
		return err
	}
	if err := embeddedspecs.WriteSpecs(*dir, specs); err != nil {
		return err
	}
	fmt.Printf("genspecs: wrote %d spec files to %s\n", len(specs), *dir)
	return nil
}
