// Command pg-caffeinate-shim stands in for /usr/bin/caffeinate. It accepts the
// caffeinate command line
//
//	caffeinate [-disu] [-t timeout] [-w pid] [utility arguments...]
//
// and turns it into a lease file bound to the shim process, so the keeper (not
// the caller) holds the one real assertion. The shim is the lease's client: the
// lease lives exactly as long as the shim, and a caller that backgrounds it and
// later kills its pid releases the lease with no further protocol.
//
// The shim never depends on argv[0], never reads the configuration and never
// initializes OTel; it needs no keeper to be running.
package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Version is stamped by mkGoApp's default ldflag target (-X main.Version=...).
var Version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("pg-caffeinate-shim %s\n", Version)
		return
	}
	run(os.Args[1:], os.Stderr).finish()
}

// result is how the shim ends: with an exit code, or by re-raising a signal so
// the wait status the caller sees is the signal's (143, 129, 130, ...).
type result struct {
	code int
	sig  syscall.Signal
}

// finish ends the process. A signal result re-raises the default action; the
// 128+n exit is only a fallback for a signal that somehow did not kill us.
func (r result) finish() {
	if r.sig != 0 {
		signal.Reset(r.sig)
		_ = syscall.Kill(os.Getpid(), r.sig)
		time.Sleep(time.Second)
		os.Exit(128 + int(r.sig))
	}
	os.Exit(r.code)
}

// Exit codes beyond caffeinate's own 0 and 1.
const (
	exitLeaseWrite = 3   // the pure form could not write its lease
	exitNotFound   = 127 // utility not found (shell convention)
	exitNotRunable = 126 // utility found but not executable
)

// run is the whole program, with its stderr injected.
func run(args []string, stderr io.Writer) result {
	req, out := parseArgs(args)
	if out != nil {
		_, _ = io.WriteString(stderr, out.Stderr)
		return result{code: out.ExitCode}
	}
	if len(req.Utility) > 0 {
		return runUtility(req, args, stderr)
	}
	return runPure(req, args, stderr)
}
