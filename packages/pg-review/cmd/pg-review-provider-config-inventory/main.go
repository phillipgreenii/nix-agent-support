// Command pg-review-provider-config-inventory prints evidence rows for the
// config-health lens as one JSON array.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/configinventory"
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
	fs := flag.NewFlagSet("pg-review-provider-config-inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o configinventory.Options
	var prefixes, layers, excl list
	fs.StringVar(&o.Since, "since", "", "window start, RFC3339 with offset (required)")
	fs.StringVar(&o.Until, "until", "", "window end, RFC3339 with offset (required)")
	fs.StringVar(&o.Scope, "scope", "", "scope name written into each row (required)")
	fs.Var(&prefixes, "scope-prefix", "scope directory prefix (repeatable; the first is the scope root)")
	fs.Var(&layers, "layer", "layer that loads in the scope: user, rules or project (repeatable)")
	fs.Var(&excl, "exclude-cwd-prefix", "drop sessions under this prefix from skill use (repeatable)")
	fs.StringVar(&o.UserDir, "user-dir", "", "user configuration directory (default $CLAUDE_CONFIG_DIR, else $HOME/.claude)")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pg-review-provider-config-inventory: unexpected argument %q\n", fs.Arg(0))
		return exitcode.Usage
	}
	o.ScopePrefixes, o.Layers, o.ExcludeCwdPrefixes = prefixes, layers, excl
	if o.Since == "" || o.Until == "" || o.Scope == "" {
		fmt.Fprintln(stderr, "pg-review-provider-config-inventory: --since, --until and --scope are required")
		return exitcode.Usage
	}
	if err := configinventory.Run(ctx, o, stdout); err != nil {
		fmt.Fprintln(stderr, "pg-review-provider-config-inventory:", err)
		if errors.Is(err, configinventory.ErrUsage) {
			return exitcode.Usage
		}
		return exitcode.Failure
	}
	return exitcode.OK
}
