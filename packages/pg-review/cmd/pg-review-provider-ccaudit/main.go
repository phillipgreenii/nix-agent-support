// Command pg-review-provider-ccaudit prints evidence rows built from the
// canned pg-ccaudit queries as one JSON array.
package main

import (
	"context"
	"io"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/ccaudit"
)

// Version is set at build time via -ldflags (mkGoApp's default target).
var Version = "dev"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return ccaudit.Main(ctx, args, stdout, stderr)
}
