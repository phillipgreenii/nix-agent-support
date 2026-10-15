package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

const acquireUsage = "usage: pg-keep-awake acquire --owner O [--key K] [--pid P] [--for DUR] [-d] [-i] [-m] [-s]"

// runAcquire creates or refreshes a lease and prints its id. With neither
// --pid nor --for the lease is bound to the caller's parent pid; --for alone
// is a pure timer with no client. A dead --pid creates no lease, warns, and
// still exits 0.
func runAcquire(args []string, stdout, stderr io.Writer) int {
	set := newFlagSet("acquire", stderr)
	owner := set.String("owner", "", "lease owner (required)")
	key := set.String("key", defaultKey, "lease key")
	pid := set.Int("pid", 0, "bind the lease to this process (default: the caller's parent pid unless --for is given)")
	forStr := set.String("for", "", "expire the lease after this duration (for example 30m)")
	var flags flagSet
	flags.register(set)
	if exit, done := parseFlags(set, args); done {
		return exit
	}
	if set.NArg() != 0 {
		fmt.Fprintln(stderr, acquireUsage)
		return exitUsage
	}
	if *owner == "" {
		return usageError(stderr, "acquire: --owner is required")
	}
	if msg := checkOwnerKey(*owner, *key); msg != "" {
		return usageError(stderr, "acquire: %s", msg)
	}
	pidSet, forSet := false, *forStr != ""
	set.Visit(func(f *flag.Flag) {
		if f.Name == "pid" {
			pidSet = true
		}
	})
	if pidSet && *pid <= 0 {
		return usageError(stderr, "acquire: --pid must be a positive process id")
	}
	var dur time.Duration
	if forSet {
		d, err := parseDuration(*forStr)
		if err != nil {
			return usageError(stderr, "acquire: --for: %v", err)
		}
		dur = d
	}

	now := time.Now().UTC()
	l := &lease.Lease{
		Owner:     *owner,
		Key:       *key,
		Kind:      lease.KindAcquire,
		Flags:     flags.list(),
		Argv:      append([]string{"pg-keep-awake", "acquire"}, args...),
		CreatedAt: now,
	}
	if forSet {
		exp := now.Add(dur)
		l.ExpiresAt = &exp
	}
	clientPID := *pid
	if !pidSet && !forSet {
		clientPID = os.Getppid()
	}
	if pidSet || !forSet {
		st := lease.SysProber{}.Probe(clientPID)
		if !st.Exists || st.Zombie {
			fmt.Fprintf(stderr, "pg-keep-awake: acquire: pid %d is not running; no lease created\n", clientPID)
			return exitOK
		}
		l.Client = &lease.ProcRef{PID: clientPID, StartUS: st.StartUS}
	}

	d, err := ensureStateDir()
	if err != nil {
		return failState(stderr, "acquire", err)
	}
	if err := d.PutLease(l); err != nil {
		return failState(stderr, "acquire", err)
	}
	fmt.Fprintln(stdout, l.ID)
	return exitOK
}
