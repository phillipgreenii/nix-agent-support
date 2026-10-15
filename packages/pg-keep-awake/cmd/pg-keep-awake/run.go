package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

const runUsage = "usage: pg-keep-awake run --owner O [--key K] [-d] [-i] [-m] [-s] -- command [args...]"

// Shell conventions for a command that could not be started.
const (
	exitNotFound   = 127
	exitNotRunable = 126
)

// runCommand holds a lease for the life of a command. The lease is bound to
// this process (key <pid>-<start_us> unless --key is given) and removed on
// every exit path. SIGINT, SIGTERM and SIGHUP are forwarded to the command;
// the command's exit status passes through, a signal death gives 128+n, and
// a command that is not found gives 127.
func runCommand(args []string, stdout, stderr io.Writer) int {
	set := newFlagSet("run", stderr)
	owner := set.String("owner", "", "lease owner (required)")
	key := set.String("key", "", "lease key (default: <pid>-<start_us> of this process)")
	var flags flagSet
	flags.register(set)
	if exit, done := parseFlags(set, args); done {
		return exit
	}
	if *owner == "" {
		return usageError(stderr, "run: --owner is required")
	}
	argv := set.Args()
	if len(argv) == 0 {
		fmt.Fprintln(stderr, runUsage)
		return exitUsage
	}
	if *key != "" {
		if msg := checkOwnerKey(*owner, *key); msg != "" {
			return usageError(stderr, "run: %s", msg)
		}
	} else if msg := checkOwnerKey(*owner, defaultKey); msg != "" {
		return usageError(stderr, "run: %s", msg)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	if cmd.Err != nil { // not found on PATH: nothing to hold a lease for
		fmt.Fprintf(stderr, "pg-keep-awake: run: %v\n", cmd.Err)
		return exitNotFound
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr

	self, err := lease.Ref(os.Getpid())
	if err != nil {
		fmt.Fprintf(stderr, "pg-keep-awake: run: cannot identify this process: %v\n", err)
		return exitUnexpected
	}
	k := *key
	if k == "" {
		k = fmt.Sprintf("%d-%d", self.PID, self.StartUS)
	}
	l := &lease.Lease{
		Owner:     *owner,
		Key:       k,
		Kind:      lease.KindRun,
		Client:    &self,
		Flags:     flags.list(),
		Argv:      argv,
		CreatedAt: time.Now().UTC(),
	}
	d, err := ensureStateDir()
	if err != nil {
		return failState(stderr, "run", err)
	}
	if err := d.PutLease(l); err != nil {
		return failState(stderr, "run", err)
	}
	defer func() {
		if err := d.RemoveLease(l.ID); err != nil {
			fmt.Fprintf(stderr, "pg-keep-awake: run: could not remove lease %s: %v\n", l.ID, err)
		}
	}()

	// Subscribe before starting so a signal that arrives early is held, not
	// lost to the default action.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "pg-keep-awake: run: %v\n", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return exitNotFound
		}
		return exitNotRunable
	}
	done := make(chan struct{})
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	waitErr := cmd.Wait()
	close(done)
	<-forwarded

	if waitErr == nil {
		return exitOK
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		fmt.Fprintf(stderr, "pg-keep-awake: run: %v\n", waitErr)
		return exitUnexpected
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ee.ExitCode()
}
