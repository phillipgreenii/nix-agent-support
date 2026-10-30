// Command pg-claude-code-recorder is the recorder's command entry point. This
// scaffold carries no recorder logic: it prints its version and reports whether
// the darwin cgo toolchain was linked in.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/platform"
)

// Version is stamped by mkGoApp's default ldflag (main.Version).
var Version = "dev"

func run(args []string, out io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		_, _ = fmt.Fprintf(out, "pg-claude-code-recorder %s (cgo=%t)\n", Version, platform.CgoReady())
		return 0
	}
	_, _ = fmt.Fprintln(out, "usage: pg-claude-code-recorder --version")
	return 2
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
