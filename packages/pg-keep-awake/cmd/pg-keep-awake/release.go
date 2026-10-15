package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

const releaseUsage = "usage: pg-keep-awake release --owner O [--key K | --all]"

// runRelease removes one lease (key default without --key) or, with --all,
// every lease of the owner. A lease that does not exist is not an error.
func runRelease(args []string, stdout, stderr io.Writer) int {
	set := newFlagSet("release", stderr)
	owner := set.String("owner", "", "lease owner (required)")
	key := set.String("key", "", "lease key (default: "+defaultKey+")")
	all := set.Bool("all", false, "remove every lease of the owner")
	if exit, done := parseFlags(set, args, stdout); done {
		return exit
	}
	if set.NArg() != 0 {
		fmt.Fprintln(stderr, releaseUsage)
		return exitUsage
	}
	if *owner == "" {
		return usageError(stderr, "release: --owner is required")
	}
	if *all && *key != "" {
		return usageError(stderr, "release: --key and --all cannot be combined")
	}
	k := *key
	if k == "" {
		k = defaultKey
	}
	if msg := checkOwnerKey(*owner, k); msg != "" {
		return usageError(stderr, "release: %s", msg)
	}

	d, err := existingStateDir()
	if errors.Is(err, fs.ErrNotExist) {
		return exitOK
	}
	if err != nil {
		return failState(stderr, "release", err)
	}
	lk, err := d.TakeLock()
	if errors.Is(err, fs.ErrNotExist) {
		return exitOK // no leases/ directory: nothing to release
	}
	if err != nil {
		return failState(stderr, "release", err)
	}
	defer lk.Unlock()

	if !*all {
		if err := lk.Remove(lease.MakeID(*owner, k)); err != nil {
			return failState(stderr, "release", err)
		}
		return exitOK
	}
	res, err := d.ListLeases()
	if err != nil {
		return failState(stderr, "release", err)
	}
	for _, l := range res.Leases {
		if l.Owner != *owner {
			continue
		}
		if err := lk.Remove(l.ID); err != nil {
			return failState(stderr, "release", err)
		}
	}
	return exitOK
}
