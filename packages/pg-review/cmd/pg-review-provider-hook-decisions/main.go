// Command pg-review-provider-hook-decisions prints evidence rows (a JSON array)
// from the approval hook's decision database for one window and scope.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/hookdecisions"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
)

// Version is set at build time via -ldflags (mkGoApp's default target).
var Version = "dev"

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}

// parseBound normalises an RFC3339 bound (any offset) to UTC.
func parseBound(name, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q: %v", name, v, err)
	}
	return t.UTC(), nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pg-review-provider-hook-decisions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var queries, inc, exc listFlag
	since := fs.String("since", "", "window start (RFC3339)")
	until := fs.String("until", "", "window end (RFC3339, exclusive)")
	scope := fs.String("scope", "", "scope name echoed into every row")
	db := fs.String("db", hookdecisions.DefaultDBPath(), "decision database path")
	fs.Var(&queries, "query", "query name (repeatable; default all)")
	fs.Var(&inc, "cwd-prefix", "keep decisions under this directory (repeatable)")
	fs.Var(&exc, "exclude-cwd-prefix", "drop decisions under this directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	s, err := parseBound("--since", *since)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	u, err := parseBound("--until", *until)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	rows, err := hookdecisions.Run(ctx, hookdecisions.Options{
		DBPath: *db, Since: s, Until: u, Scope: *scope,
		Queries: queries, CwdPrefixes: inc, ExcludeCwdPrefixes: exc,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		var ue *hookdecisions.UsageError
		if errors.As(err, &ue) {
			return 2
		}
		return 1
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := schema.Validate("evidence-rows", b); err != nil {
		fmt.Fprintf(stderr, "internal error: output does not conform: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(append(b, '\n'))
	return 0
}
