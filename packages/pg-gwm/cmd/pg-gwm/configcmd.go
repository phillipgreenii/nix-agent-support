package main

import (
	"fmt"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/x/exitcode"
)

// configCmd runs "pg-gwm config check [--config <path>]".
//
// With --config the file is checked on its own: only the pure config.Check
// findings, which is what the home-manager build needs (a sandbox has neither
// the repositories nor git state). Without it the file at config.DefaultPath
// is read and config.CheckLive adds the findings that need this machine (a key
// that is no main checkout, a count above max). Any error finding exits 2.
func (a *app) configCmd(args []string) int {
	if len(args) == 0 || args[0] != "check" {
		return a.usageErr(fmt.Errorf("usage: pg-gwm config check [--config <path>]"))
	}
	fs := newFlagSet("config check")
	var path string
	fs.StringVar(&path, "config", "", "check this file alone, with no repository lookups (default: the configuration file, with lookups)")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return a.usageErr(err)
	}
	if len(pos) > 0 {
		return a.usageErr(fmt.Errorf("config check takes no arguments"))
	}
	live := path == ""
	if live {
		path = config.DefaultPath()
	}
	file, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(a.stderr, "pg-gwm: %v\n", err)
		return exitcode.Usage
	}
	var findings []config.Finding
	if live {
		ctx, stop := cmdContext()
		defer stop()
		findings = config.CheckLive(ctx, file,
			func(key string) (repo.Repo, error) { return repo.Resolve(ctx, a.git, key) },
			func(r repo.Repo) (int, error) {
				recs, err := a.buildManager(r, file).List(ctx, false)
				return len(recs), err
			})
	} else {
		findings = config.Check(file)
	}
	code := 0
	for _, f := range findings {
		where := f.Repo
		if where == "" {
			where = "config"
		}
		fmt.Fprintf(a.stdout, "%s: %s: %s\n", f.Level, where, f.Message)
		if f.Level == config.LevelError {
			code = exitcode.Usage
		}
	}
	if len(findings) == 0 {
		fmt.Fprintf(a.stdout, "ok: %s\n", path)
	}
	return code
}
