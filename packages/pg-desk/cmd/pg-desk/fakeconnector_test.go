package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/connector"
)

// useFakeConnector installs an in-process pg-connector for one test: every
// call any pg-desk code makes through internal/connector is answered by fn
// (stdout and exit code from the call's arguments) with no child process and
// no script on PATH.
func useFakeConnector(t *testing.T, fn func(args []string) (stdout string, code int)) {
	t.Helper()
	restore := connector.Use(func(_ context.Context, c cmdrun.Call) cmdrun.Result {
		out, code := fn(c.Args)
		return cmdrun.Result{Stdout: []byte(out), Code: code}
	})
	t.Cleanup(restore)
}

// connectorCallLog appends one "args joined by spaces" line per call to
// dir/calls.log, the record the script fakes kept with `echo "$*"`.
type connectorCallLog struct {
	mu   sync.Mutex
	path string
}

func (l *connectorCallLog) record(t *testing.T, args []string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Errorf("record connector call: %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(strings.Join(args, " ") + "\n")
}

// argAt returns args[i], or "" when absent (the shell's "$N").
func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// flagValue returns the argument following name, "" when absent.
func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func readFileOr(p, def string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return def
	}
	return string(b)
}

// fileExitCode reads an exit code a test wrote into p ("0" when p is absent,
// the shell fakes' `if [ -f p ]; then code=$(cat p)` default).
func fileExitCode(p string) int {
	n, err := strconv.Atoi(strings.TrimSpace(readFileOr(p, "0")))
	if err != nil {
		return 0
	}
	return n
}
