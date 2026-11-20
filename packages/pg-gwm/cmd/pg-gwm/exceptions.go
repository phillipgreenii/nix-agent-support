package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/manager"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
)

// exceptionRow is one exception in the list --exceptions JSON output.
type exceptionRow struct {
	Path     string `json:"path"`
	Reason   string `json:"reason"`
	Owner    string `json:"owner"`
	ReviewBy string `json:"review_by"`
}

// listExceptions prints the standing exceptions configured for the repository.
func (a *app) listExceptions(format, repoPath string) int {
	ctx, stop := cmdContext()
	defer stop()
	if repoPath == "" {
		repoPath = "."
	}
	base, err := filepath.Abs(repoPath)
	if err != nil {
		return a.emitErr(format, false, manager.ErrUsage(err.Error(), "", nil))
	}
	r, err := repo.Resolve(ctx, a.git, base)
	switch {
	case errors.Is(err, repo.ErrNotARepo), errors.Is(err, repo.ErrCwdGone):
		return a.emitErr(format, false, manager.ErrUsage(err.Error(), "run inside a repository or pass --repo <path>", map[string]any{"path": base}))
	case err != nil:
		return a.emitErr(format, false, err)
	}
	file, err := config.Load(config.DefaultPath())
	if err != nil {
		return a.emitErr(format, false, manager.ErrUsage(err.Error(), "run: pg-gwm config check", nil))
	}
	rows := []exceptionRow{}
	for _, e := range file.For(r).Exceptions {
		rows = append(rows, exceptionRow{e.Path, e.Reason, e.Owner, e.ReviewBy})
	}
	if format == "json" {
		b, err := json.Marshal(rows)
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintln(a.stdout, string(b))
		return 0
	}
	w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tREASON\tOWNER\tREVIEW-BY")
	for _, e := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Path, e.Reason, e.Owner, e.ReviewBy)
	}
	_ = w.Flush()
	return 0
}
