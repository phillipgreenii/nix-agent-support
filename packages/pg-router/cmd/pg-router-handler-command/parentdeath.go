package main

import (
	"context"
	"io"
	"os"
	"strconv"
)

// parentDeathFDEnv names the environment variable pg-router's core sets to the
// file descriptor number of the parent-death pipe's read end. The core holds the
// write end until it exits; when it dies without a graceful cancel (SIGKILL, the
// watchdog's hard exit, a crash) the kernel closes it and the read end reads EOF.
// The variable is absent for a survives_shutdown role, whose dispatch is meant to
// outlive the daemon, and the ccpool adapter never reads it.
const parentDeathFDEnv = "PG_ROUTER_PARENT_DEATH_FD"

// withParentDeath returns ctx cancelled when r reads EOF or fails, i.e. when the
// parent died. The returned stop releases the watcher's context only: the
// goroutine ends when r closes or ctx is done, whichever the caller arranges.
// A nil r returns ctx unchanged.
func withParentDeath(ctx context.Context, r io.Reader) (context.Context, context.CancelFunc) {
	if r == nil {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		// Nothing is ever written to the pipe: Read returns only on EOF (parent
		// gone) or an error, both of which mean the parent is no longer there.
		_, _ = io.Copy(io.Discard, r)
		cancel()
	}()
	return ctx, cancel
}

// parentDeathReader opens the pipe named by parentDeathFDEnv, or returns nil when
// the variable is unset or malformed (the adapter then runs as it always did).
func parentDeathReader(getenv func(string) string) io.ReadCloser {
	v := getenv(parentDeathFDEnv)
	if v == "" {
		return nil
	}
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 3 {
		return nil
	}
	return os.NewFile(uintptr(fd), "pg-router-parent-death")
}
