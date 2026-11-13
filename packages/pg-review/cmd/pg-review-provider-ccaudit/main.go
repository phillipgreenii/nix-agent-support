// Command pg-review-provider-ccaudit prints evidence rows built from the
// canned pg-ccaudit queries as one JSON array.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/ccaudit"
	"github.com/phillipgreenii/x/exitcode"
)

// Version is set at build time via -ldflags (mkGoApp's default target).
var Version = "dev"

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pg-review-provider-ccaudit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o ccaudit.Options
	var queries, cwd, excl list
	fs.StringVar(&o.Since, "since", "", "window start, RFC3339 with offset (required)")
	fs.StringVar(&o.Until, "until", "", "window end, RFC3339 with offset (required)")
	fs.StringVar(&o.Scope, "scope", "", "scope name written into each row (required)")
	fs.Var(&queries, "query", "query to run (repeatable; default all seven)")
	fs.Var(&cwd, "cwd-prefix", "keep sessions under this prefix (repeatable)")
	fs.Var(&excl, "exclude-cwd-prefix", "drop sessions under this prefix (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	o.Queries, o.CwdPrefixes, o.ExcludeCwdPrefixes = queries, cwd, excl
	if o.Since == "" || o.Until == "" || o.Scope == "" {
		fmt.Fprintln(stderr, "pg-review-provider-ccaudit: --since, --until and --scope are required")
		return exitcode.Usage
	}
	if err := ccaudit.Run(ctx, o, stdout); err != nil {
		fmt.Fprintln(stderr, "pg-review-provider-ccaudit:", err)
		if errors.Is(err, ccaudit.ErrUsage) {
			return exitcode.Usage
		}
		return exitcode.Error
	}
	return exitcode.OK
}
