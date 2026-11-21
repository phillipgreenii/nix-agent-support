package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router/api/participant"
	"github.com/phillipgreenii/pg-router/internal/handlers/worktree"
)

// runReclaimInterrupted is the `reclaim-interrupted` subcommand: the disk
// reclaimer's view of the "<worktreeDir>-interrupted" holding area. By default
// it only reports (a dry run); --apply removes exactly the entries the dry run
// lists as REMOVE; --size-kb prints the KiB the removal set would free.
func runReclaimInterrupted(args []string) int {
	return reclaimInterrupted(args, os.Stdout, os.Stderr)
}

func reclaimInterrupted(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reclaim-interrupted", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "the holding directory (<worktreeDir>-interrupted) to scan")
	repo := fs.String("repo", "", "the repository root the manifests' branches live in")
	apply := fs.Bool("apply", false, "remove the entries the dry run lists as REMOVE")
	sizeKB := fs.Bool("size-kb", false, "print only the KiB the removal set would free")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, helpText)
		return participant.ExitOK
	case err != nil:
		fmt.Fprintln(stderr, "reclaim-interrupted:", err)
		return participant.ExitUsage
	}
	if fs.NArg() > 0 || *dir == "" || (*apply && *sizeKB) {
		fmt.Fprintln(stderr, "reclaim-interrupted: usage: reclaim-interrupted --dir DIR --repo REPO [--apply | --size-kb]")
		return participant.ExitUsage
	}
	ctx := context.Background()
	plan, err := worktree.PlanReclaimInterrupted(ctx, *dir, *repo)
	if err != nil {
		fmt.Fprintln(stderr, "reclaim-interrupted:", err)
		return participant.ExitError
	}
	var kb int64
	removes, keeps := 0, 0
	for _, d := range plan {
		if d.Remove {
			removes++
			kb += d.SizeKB
		} else {
			keeps++
		}
	}
	if *sizeKB {
		fmt.Fprintln(stdout, kb)
		return participant.ExitOK
	}
	for _, d := range plan {
		if d.Remove {
			fmt.Fprintf(stdout, "REMOVE %s rule=%q\n", d.Entry, d.Rule)
		} else {
			fmt.Fprintf(stdout, "KEEP %s reason=%q\n", d.Entry, d.Reason)
		}
	}
	if *apply {
		if err := worktree.ApplyReclaimInterrupted(*dir, plan); err != nil {
			fmt.Fprintln(stderr, "reclaim-interrupted:", err)
			return participant.ExitError
		}
		fmt.Fprintf(stdout, "removed %d, kept %d\n", removes, keeps)
		return participant.ExitOK
	}
	fmt.Fprintf(stdout, "dry run: would remove %d, keep %d\n", removes, keeps)
	return participant.ExitOK
}
