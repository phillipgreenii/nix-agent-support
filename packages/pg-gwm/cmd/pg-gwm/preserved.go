package main

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/manager"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// preservedEntry is one preserved branch in the JSON output; the name and
// lease id are null for a branch the state does not know.
type preservedEntry struct {
	Branch  string  `json:"branch"`
	Name    *string `json:"name"`
	LeaseID *string `json:"lease_id"`
	At      string  `json:"at"`
}

type preservedListOut struct {
	Preserved []preservedEntry `json:"preserved"`
}

type preservedPruneOut struct {
	Deleted []string `json:"deleted"`
	DryRun  bool     `json:"dry_run"`
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// preserved runs preserved list and preserved prune.
func (a *app) preserved(args []string) int {
	if len(args) == 0 || (args[0] != "list" && args[0] != "prune") {
		fmt.Fprintln(a.stderr, "pg-gwm: preserved needs a subcommand: list, or prune [--older-than D] [--dry-run]")
		return exitUsage
	}
	sub := args[0]
	fs := newFlagSet("preserved " + sub)
	var c commonFlags
	c.register(fs, false)
	var (
		olderThan string
		dryRun    bool
	)
	if sub == "prune" {
		fs.StringVar(&olderThan, "older-than", "", "also delete preserved branches older than this: <int><s|m|h|d>")
		fs.BoolVar(&dryRun, "dry-run", false, "list what would be deleted and delete nothing")
	}
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return a.usageErr(err)
	}
	format, ferr := c.resolveFormat(a.stdoutIsTerminal())
	if ferr != nil {
		return a.usageErr(ferr)
	}
	if len(pos) > 0 {
		return a.emitErr(format, false, manager.ErrUsage("preserved "+sub+" takes no arguments", "", nil))
	}
	var age *time.Duration
	if olderThan != "" {
		d, err := config.ParseDuration(olderThan)
		if err != nil || d.None {
			return a.emitErr(format, false, manager.ErrUsage(fmt.Sprintf("--older-than %q: want <int><s|m|h|d>", olderThan), "", map[string]any{"flag": "--older-than"}))
		}
		age = &d.D
	}
	ctx, stop := cmdContext()
	defer stop()
	m, _, err := a.openManager(ctx, c.repo)
	if err != nil {
		return a.emitErr(format, false, err)
	}
	if sub == "list" {
		list, err := m.PreservedList(ctx)
		if err != nil {
			return a.emitErr(format, false, err)
		}
		return a.emitPreservedList(format, list)
	}
	deleted, err := m.PreservedPrune(ctx, age, dryRun)
	if err != nil {
		return a.emitErr(format, false, err)
	}
	if format == "json" {
		return a.emit(format, preservedPruneOut{Deleted: deleted, DryRun: dryRun})
	}
	verb := "deleted"
	if dryRun {
		verb = "would delete"
	}
	if len(deleted) == 0 {
		fmt.Fprintln(a.stdout, "nothing to delete")
	}
	for _, b := range deleted {
		fmt.Fprintf(a.stdout, "%s %s\n", verb, b)
	}
	return 0
}

func (a *app) emitPreservedList(format string, list []state.Preserved) int {
	if format == "json" {
		out := preservedListOut{Preserved: []preservedEntry{}}
		for _, p := range list {
			out.Preserved = append(out.Preserved, preservedEntry{
				Branch: p.Branch, Name: nullable(p.Name), LeaseID: nullable(p.LeaseID), At: p.At.UTC().Format(time.RFC3339),
			})
		}
		return a.emit(format, out)
	}
	fmt.Fprintf(a.stdout, "%-20s %-16s %-14s %s\n", "AT", "NAME", "LEASE", "BRANCH")
	for _, p := range list {
		name, lease := p.Name, p.LeaseID
		if name == "" {
			name = "-"
		}
		if lease == "" {
			lease = "-"
		}
		fmt.Fprintf(a.stdout, "%-20s %-16s %-14s %s\n", p.At.UTC().Format(time.RFC3339), name, lease, p.Branch)
	}
	return 0
}
