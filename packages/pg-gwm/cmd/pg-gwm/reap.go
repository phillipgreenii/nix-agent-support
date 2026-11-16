package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/manager"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
	"github.com/phillipgreenii/x/exitcode"
)

// defaultReapDeadline bounds a reap that is given no --deadline.
const defaultReapDeadline = 5 * time.Minute

type reapOut struct {
	Results []manager.ReapResult `json:"results"`
}

// reap runs reap for one repository, or for every configured and registered
// one with --all-repos.
func (a *app) reap(args []string) int {
	fs := newFlagSet("reap")
	var c commonFlags
	c.register(fs, false)
	var (
		all, dry bool
		deadline string
	)
	fs.BoolVar(&all, "all-repos", false, "reap every configured and registered repository")
	fs.BoolVar(&dry, "dry-run", false, "report what a reap would do and change nothing")
	fs.StringVar(&deadline, "deadline", "", "stop after this long: <int><s|m|h|d> (default 5m)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return a.usageErr(err)
	}
	format, ferr := c.resolveFormat(a.stdoutIsTerminal())
	if ferr != nil {
		return a.usageErr(ferr)
	}
	switch {
	case len(pos) > 0:
		return a.emitErr(format, false, manager.ErrUsage("reap takes no arguments", "", nil))
	case all && c.repo != "":
		return a.emitErr(format, false, manager.ErrUsage("--all-repos and --repo contradict each other", "", nil))
	}
	span := defaultReapDeadline
	if deadline != "" {
		d, err := config.ParseDuration(deadline)
		if err != nil || d.None {
			return a.emitErr(format, false, manager.ErrUsage(fmt.Sprintf("--deadline %q: want <int><s|m|h|d>", deadline), "", map[string]any{"flag": "--deadline"}))
		}
		span = d.D
	}
	opts := manager.ReapOptions{DryRun: dry, Deadline: time.Now().Add(span)}

	ctx, stop := cmdContext()
	defer stop()
	var results []manager.ReapResult
	if all {
		file, err := config.Load(config.DefaultPath())
		if err != nil {
			return a.emitErr(format, false, manager.ErrUsage(err.Error(), "run: pg-gwm config check", nil))
		}
		results = manager.ReapAll(ctx, file, state.Registry{Path: state.RegistryPath()}, func(r repo.Repo) (*manager.Manager, error) {
			return a.buildManager(r, file), nil
		}, opts)
		if ctx.Err() != nil {
			return a.emitErr(format, false, manager.ErrInterrupted("interrupted during reap"))
		}
	} else {
		m, _, err := a.openManager(ctx, c.repo)
		if err != nil {
			return a.emitErr(format, false, err)
		}
		res, err := m.Reap(ctx, opts)
		if err != nil {
			return a.emitErr(format, false, err)
		}
		results = []manager.ReapResult{res}
	}
	code := a.emitReap(format, results)
	for _, r := range results {
		if r.Outcome == manager.ReapError {
			return exitcode.Error
		}
	}
	return code
}

func (a *app) emitReap(format string, results []manager.ReapResult) int {
	if results == nil {
		results = []manager.ReapResult{}
	}
	if format == "json" {
		return a.emit(format, reapOut{Results: results})
	}
	if len(results) == 0 {
		fmt.Fprintln(a.stdout, "nothing to reap")
	}
	for _, r := range results {
		fmt.Fprintf(a.stdout, "%s: %s (removed %d, kept %d, preserved %d, broken %d, created %d)\n",
			r.Repo, r.Outcome, len(r.Removed), len(r.Kept), len(r.Preserved), len(r.Broken), len(r.Created))
		for _, w := range r.Warnings {
			fmt.Fprintf(a.stdout, "  warning: %s\n", strings.TrimSpace(w))
		}
	}
	return 0
}
