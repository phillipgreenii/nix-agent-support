// Command pg-probe is a read-only probe for unattended agent sessions.
//
// It has exactly two jobs, each narrower than the shell command it replaces:
//
//	pg-probe pprof TARGET      GET one allowlisted /debug/pprof path over a
//	                           daemon's pprof unix socket
//	pg-probe wait-log FILE RE  return the first line of a log that matches RE
//
// It is NOT a generic curl, unix-socket client or Prometheus/Grafana client,
// and it never writes anything: output goes to stdout and the caller decides
// where it lands. See the package README for the full contract.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-probe/internal/pprofget"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-probe/internal/waitlog"
)

// Version is stamped by mkGoApp's default ldflag target (-X main.Version=...).
var Version = "dev"

// Exit codes. 1 is reserved for generic runtime errors; every branchable
// meaning is a distinct value >= 2.
const (
	exitOK      = 0
	exitError   = 1
	exitUsage   = 2
	exitRefused = 3
	// exitTimeout is wait-log's deadline, matching timeout(1) so a script that
	// used `timeout N tail -f | grep -m1` keeps its meaning.
	exitTimeout = 124
)

const usageText = `pg-probe: read-only probes for unattended sessions

Usage:
  pg-probe pprof list
  pg-probe pprof TARGET [--seconds N] [--debug N] [-X GET]
  pg-probe wait-log [--timeout DUR] [--lines N | --from-start] FILE REGEX
  pg-probe --version | --help

pprof: GET one allowlisted /debug/pprof path over the pa-monitor pprof socket
and write the body to stdout (redirect it yourself for a binary profile).
TARGET is a name such as heap, goroutine, profile or index, or a full allowed
path. Anything else, and any method but GET, is refused (exit 3).
Socket: $PG_PROBE_PPROF_SOCKET, else $XDG_STATE_HOME/pa-monitor/pprof.sock.

wait-log: print the first line of FILE (existing last --lines, default 10,
then new ones; --from-start for the whole file) matching the RE2 REGEX. FILE
MUST resolve to a regular file under an allowed log root (exit 3 otherwise).
Roots: $PG_PROBE_WAIT_LOG_ROOTS (colon list), else a built-in list.

Exit codes: 0 ok, 1 error, 2 usage, 3 refused by policy, 124 wait-log timeout.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usageText)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		_, _ = io.WriteString(stdout, usageText)
		return exitOK
	case "--version", "version":
		_, _ = fmt.Fprintf(stdout, "pg-probe %s\n", Version)
		return exitOK
	case "pprof":
		return runPprof(ctx, args[1:], getenv, stdout, stderr)
	case "wait-log":
		return runWaitLog(ctx, args[1:], getenv, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "pg-probe: unknown command %q\n\n%s", args[0], usageText)
		return exitUsage
	}
}

// parseInterspersed parses flags that may appear before or after positional
// arguments, returning the positionals.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func defaultSocket(getenv func(string) string) string {
	if s := getenv("PG_PROBE_PPROF_SOCKET"); s != "" {
		return s
	}
	state := getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(getenv("HOME"), ".local", "state")
	}
	return filepath.Join(state, "pa-monitor", pprofget.SocketBase)
}

func runPprof(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := newFlagSet("pg-probe pprof", stderr)
	seconds := fs.Int("seconds", 0, "profile/trace duration in seconds (1..300)")
	debug := fs.Int("debug", -1, "debug level for a named runtime profile (0, 1 or 2)")
	method := fs.String("X", http.MethodGet, "request method; only GET is accepted (kept so a curl habit gets a clear refusal)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) != 1 {
		_, _ = fmt.Fprint(stderr, "pg-probe pprof: want exactly one TARGET (or `list`)\n")
		return exitUsage
	}
	if pos[0] == "list" {
		for _, p := range pprofget.AllowedPaths() {
			_, _ = fmt.Fprintln(stdout, p)
		}
		return exitOK
	}

	path, err := pprofget.ResolveTarget(pos[0])
	if err != nil {
		return report(stderr, err)
	}
	var q pprofget.Query
	if *seconds != 0 {
		q.Seconds = seconds
	}
	if *debug >= 0 {
		q.Debug = debug
	}

	// A profile or trace holds the connection open for its whole duration.
	limit := 30 * time.Second
	if q.Seconds != nil && *q.Seconds > 0 {
		limit += time.Duration(*q.Seconds) * time.Second
	} else if strings.HasSuffix(path, "/profile") {
		limit += 30 * time.Second // the daemon's default CPU profile is 30s
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	c := &pprofget.Client{Socket: defaultSocket(getenv)}
	if err := c.Do(ctx, strings.ToUpper(*method), path, q, stdout); err != nil {
		return report(stderr, err)
	}
	return exitOK
}

func report(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "pg-probe: %v\n", err)
	var pr *pprofget.RefusedError
	var wr *waitlog.RefusedError
	if errors.As(err, &pr) || errors.As(err, &wr) {
		return exitRefused
	}
	return exitError
}

// defaultWaitLogRoots is the built-in list of directories wait-log may read.
// It mirrors the pool-only read roots the operator approved for log waits:
// temp directories, system logs and the router and monitor state directories.
func defaultWaitLogRoots(getenv func(string) string) []string {
	roots := []string{"/tmp", "/private/tmp", "/var/log", "/private/var/log"}
	if t := getenv("TMPDIR"); t != "" {
		roots = append(roots, t)
	}
	state := getenv("XDG_STATE_HOME")
	if state == "" && getenv("HOME") != "" {
		state = filepath.Join(getenv("HOME"), ".local", "state")
	}
	if state != "" {
		roots = append(roots, filepath.Join(state, "pa-monitor"))
		if m, err := filepath.Glob(filepath.Join(state, "pg-router*")); err == nil {
			roots = append(roots, m...)
		}
	}
	return roots
}

func waitLogRoots(getenv func(string) string) []string {
	if v := getenv("PG_PROBE_WAIT_LOG_ROOTS"); v != "" {
		var out []string
		for _, r := range strings.Split(v, string(os.PathListSeparator)) {
			if r != "" {
				out = append(out, r)
			}
		}
		return out
	}
	return defaultWaitLogRoots(getenv)
}

func runWaitLog(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := newFlagSet("pg-probe wait-log", stderr)
	timeout := fs.Duration("timeout", 60*time.Second, "give up after this long (exit 124); max 1h")
	lines := fs.Int("lines", 10, "existing trailing lines to test first, as tail -n; 0 = only new lines")
	fromStart := fs.Bool("from-start", false, "test the whole existing file first")
	poll := fs.Duration("poll", 100*time.Millisecond, "how often to re-read the file")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) != 2 {
		_, _ = fmt.Fprint(stderr, "pg-probe wait-log: want FILE and REGEX\n")
		return exitUsage
	}
	re, err := regexp.Compile(pos[1])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pg-probe wait-log: bad regex: %v\n", err)
		return exitUsage
	}
	line, err := waitlog.Wait(ctx, waitlog.Options{
		Path:      pos[0],
		Pattern:   re,
		Roots:     waitLogRoots(getenv),
		Timeout:   *timeout,
		Poll:      *poll,
		Lines:     *lines,
		FromStart: *fromStart,
	})
	switch {
	case err == nil:
		_, _ = fmt.Fprintln(stdout, line)
		return exitOK
	case errors.Is(err, waitlog.ErrTimeout):
		_, _ = fmt.Fprintf(stderr, "pg-probe wait-log: timed out after %s without a line matching %q\n", *timeout, pos[1])
		return exitTimeout
	case errors.Is(err, context.Canceled):
		_, _ = fmt.Fprintln(stderr, "pg-probe wait-log: interrupted")
		return exitError
	default:
		return report(stderr, err)
	}
}
