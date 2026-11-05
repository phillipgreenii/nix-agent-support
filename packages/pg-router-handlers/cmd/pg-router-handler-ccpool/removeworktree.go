package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
	"github.com/phillipgreenii/pg-router/conformance"
)

// runRemoveWorktree implements the `remove-worktree` subcommand: not part of
// the wire contract, run by the nix worktree sweep (the pg-disk-reclaimer
// registry entry) and by hand. It removes ONE clean linked worktree through the
// same paced path every teardown of this handler uses (worktree.GitOpener's
// RemoveWorktree): the worktree's fsmonitor daemon is stopped, the tree is
// renamed into the trash beside it, the dangling registration is pruned, and the
// trash is deleted in rate-limited batches. It never forces: a dirty, locked or
// unregistered worktree is refused and left in place (exit 1). core.fsmonitor is
// left on.
//
// Exit codes: 0 removed, 1 refused or failed, 2 usage.
func runRemoveWorktree(args []string) int {
	return serveRemoveWorktree(context.Background(), os.Stdout, os.Stderr, args, worktree.GitOpener)
}

func serveRemoveWorktree(ctx context.Context, stdout, stderr io.Writer, args []string, open worktree.Opener) int {
	if len(args) != 1 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(stderr, "remove-worktree: exactly one argument is required: the worktree path")
		return conformance.ExitUsage
	}
	path := args[0]
	wm, err := open(ctx, path)
	if err != nil {
		fmt.Fprintf(stderr, "remove-worktree: cannot open %s: %v\n", path, err)
		return 1
	}
	if err := wm.RemoveWorktree(ctx, path, false); err != nil {
		fmt.Fprintf(stderr, "remove-worktree: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed: %s\n", path)
	return conformance.ExitOK
}
