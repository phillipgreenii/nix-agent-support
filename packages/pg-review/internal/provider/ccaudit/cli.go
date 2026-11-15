package ccaudit

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/phillipgreenii/x/exitcode"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// Main is the provider command line: it parses args, runs the canned queries
// and writes the evidence rows to stdout, returning the process exit code.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pg-review-provider-ccaudit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o Options
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
	if err := Run(ctx, o, stdout); err != nil {
		fmt.Fprintln(stderr, "pg-review-provider-ccaudit:", err)
		if errors.Is(err, ErrUsage) {
			return exitcode.Usage
		}
		return exitcode.Error
	}
	return exitcode.OK
}
