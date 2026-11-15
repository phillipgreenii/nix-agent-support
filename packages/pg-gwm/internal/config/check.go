package config

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
)

// Finding levels.
const (
	LevelError   = "error"
	LevelWarning = "warning"
)

// Finding is one result of a configuration check. Repo is the configuration
// key, or empty for a finding about no single repository.
type Finding struct {
	Level, Repo, Message string
}

// sampleIndexes is how many slot indexes a repository with no max renders.
const sampleIndexes = 9

func sampleInput(name string, i int, c RepoConfig) TemplateInput {
	in := TemplateInput{
		Index: i, Repo: name, Name: "sample", LeaseID: "l-sample", Owner: "owner",
		Metadata: map[string]string{}, Count: i + 1,
	}
	if c.Min != nil {
		in.Min = *c.Min
	}
	if c.Max != nil {
		in.Max = *c.Max
	}
	return in
}

func sampleCount(c RepoConfig) int {
	if c.Max != nil && *c.Max >= 0 {
		return *c.Max + 1
	}
	return sampleIndexes
}

// Check validates the file without touching any repository.
func Check(f File) []Finding {
	var out []Finding
	keys := make([]string, 0, len(f.Repos))
	for k := range f.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type rendered struct {
		key  string
		root string
		dirs map[string]bool
	}
	var all []rendered
	for _, k := range keys {
		c := withDefaults(f.Repos[k], k)
		errf := func(format string, a ...any) {
			out = append(out, Finding{LevelError, k, fmt.Sprintf(format, a...)})
		}
		if c.Min != nil && c.Max != nil && *c.Min > *c.Max {
			errf("min %d is greater than max %d", *c.Min, *c.Max)
		}
		if c.ReuseHookPolicy != "run" && c.ReuseHookPolicy != "skip" {
			errf("reuse-hook-policy %q: want run or skip", c.ReuseHookPolicy)
		}
		if _, err := provision.ValidateKeep(c.KeepList); err != nil {
			errf("%v", err)
		}
		dirs := map[string]bool{}
		name := filepath.Base(k)
		for i := 0; i < sampleCount(c); i++ {
			d, err := RenderDir(c.WorktreeDirTemplate, sampleInput(name, i, c))
			if err != nil {
				errf("worktree-dir-template: %v", err)
				break
			}
			dirs[d] = true
		}
		all = append(all, rendered{k, filepath.Clean(c.WorktreeRoot), dirs})
		if c.Reuse && !c.Clean {
			out = append(out, Finding{
				LevelWarning, k,
				"reuse-reclaimed-worktrees is on but clean-reused-worktree is off: a reused worktree keeps the previous lease's files",
			})
		}
	}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			a, b := all[i], all[j]
			if a.root != b.root {
				continue
			}
			shared := make([]string, 0, len(a.dirs))
			for d := range a.dirs {
				shared = append(shared, d)
			}
			sort.Strings(shared)
			for _, d := range shared {
				if b.dirs[d] {
					out = append(out, Finding{
						LevelError, b.key,
						fmt.Sprintf("worktree-dir-template can render %q under %s, as it can for %s", d, b.root, a.key),
					})
					break
				}
			}
		}
	}
	return out
}

// CheckLive is Check plus the findings that need the machine: a key that does
// not resolve to a main checkout, and a current count above max.
func CheckLive(ctx context.Context, f File, resolve func(string) (repo.Repo, error), count func(repo.Repo) (int, error)) []Finding {
	out := Check(f)
	keys := make([]string, 0, len(f.Repos))
	for k := range f.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ctx.Err() != nil {
			break
		}
		warn := func(format string, a ...any) {
			out = append(out, Finding{LevelWarning, k, fmt.Sprintf(format, a...)})
		}
		r, err := resolve(k)
		if err != nil {
			warn("does not resolve to a repository: %v", err)
			continue
		}
		if realPath(r.Main) != realPath(k) {
			warn("is not a main checkout; the repository's main checkout is %s", r.Main)
			continue
		}
		c := f.Repos[k]
		if c.Max == nil {
			continue
		}
		n, err := count(r)
		if err != nil {
			warn("cannot count managed worktrees: %v", err)
			continue
		}
		if n > *c.Max {
			warn("%d managed worktrees exceed max %d", n, *c.Max)
		}
	}
	return out
}
