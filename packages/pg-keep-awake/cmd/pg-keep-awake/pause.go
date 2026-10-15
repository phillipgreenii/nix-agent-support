package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// runPause writes pause.json: the keeper drops the assertion while it
// exists. Without --for the pause has no end (until null). It touches no
// lease.
func runPause(args []string, _, stderr io.Writer) int {
	set := newFlagSet("pause", stderr)
	forStr := set.String("for", "", "end the pause after this duration (for example 30m)")
	if exit, done := parseFlags(set, args); done {
		return exit
	}
	if set.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: pg-keep-awake pause [--for DUR]")
		return exitUsage
	}
	now := time.Now().UTC()
	p := &lease.PauseFile{CreatedAt: now, ByPID: os.Getppid()}
	if *forStr != "" {
		dur, err := parseDuration(*forStr)
		if err != nil {
			return usageError(stderr, "pause: --for: %v", err)
		}
		until := now.Add(dur)
		p.Until = &until
	}
	d, err := ensureStateDir()
	if err != nil {
		return failState(stderr, "pause", err)
	}
	if err := d.WritePause(p); err != nil {
		return failState(stderr, "pause", err)
	}
	return exitOK
}

// runResume removes pause.json. No pause, or no state directory, is not an
// error.
func runResume(args []string, _, stderr io.Writer) int {
	set := newFlagSet("resume", stderr)
	if exit, done := parseFlags(set, args); done {
		return exit
	}
	if set.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: pg-keep-awake resume")
		return exitUsage
	}
	d, err := existingStateDir()
	if errors.Is(err, fs.ErrNotExist) {
		return exitOK
	}
	if err != nil {
		return failState(stderr, "resume", err)
	}
	if err := d.RemovePause(); err != nil {
		return failState(stderr, "resume", err)
	}
	return exitOK
}
