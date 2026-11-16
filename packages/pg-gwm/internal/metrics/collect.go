package metrics

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/provision"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// PreservedPrefix is the ref prefix of preserved branches.
const PreservedPrefix = "refs/heads/pg-gwm/dirty-reaped-"

const gitTimeout = 30 * time.Second

// Collector reads the files the commands leave and builds a Snapshot. The
// zero value of an optional field means the production default.
type Collector struct {
	Version      string
	ConfigPath   string
	RegistryPath string
	CountersPath string
	ReaperPath   string
	Git          gitrun.Git
	Now          func() time.Time
	FreeBytes    func(string) (uint64, error)
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// existingAncestor is the nearest ancestor of p (or p) that exists.
func existingAncestor(p string) string {
	for p != "/" && p != "." {
		if _, err := provision.FreeBytes(p); err == nil {
			return p
		}
		p = filepath.Dir(p)
	}
	return p
}

// Collect gathers one scrape. It never takes a repository lock and never
// fails: what cannot be read is omitted.
func (c Collector) Collect(ctx context.Context) Snapshot {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	free := c.FreeBytes
	if free == nil {
		free = provision.FreeBytes
	}
	snap := Snapshot{Version: c.Version, ConfigValid: true}
	cfg, err := config.Load(c.ConfigPath)
	if err != nil {
		cfg, snap.ConfigValid = config.File{}, false
	}
	paths := map[string]bool{}
	for k := range cfg.Repos {
		paths[realPath(k)] = true
	}
	if entries, err := (state.Registry{Path: c.RegistryPath}).List(); err == nil {
		for _, e := range entries {
			paths[realPath(e.Main)] = true
		}
	}
	order := make([]string, 0, len(paths))
	for p := range paths {
		order = append(order, p)
	}
	sort.Strings(order)

	rec, rerr := state.ReadReaperRecord(c.ReaperPath)
	if rerr == nil && rec.LastSuccess != nil {
		a := now.Sub(*rec.LastSuccess).Seconds()
		snap.ReaperLastSuccessAge = &a
	}
	counters, cerr := state.Counters{Path: c.CountersPath}.Read()
	reapAge := func(label string) *float64 {
		if rerr != nil {
			return nil
		}
		e, ok := rec.Repos[label]
		if !ok {
			return nil
		}
		a := now.Sub(e.At).Seconds()
		return &a
	}

	for _, p := range order {
		rp, err := repo.FromMain(p)
		if err != nil {
			label := repo.PathLabel(p)
			snap.Repos = append(snap.Repos, Repo{Label: label, Unreachable: true, LastReapAge: reapAge(label)})
			continue
		}
		r := Repo{Label: rp.Label, LastReapAge: reapAge(rp.Label)}
		if cerr == nil {
			r.Counters = counters.Repos[rp.Label]
		}
		rc := cfg.For(rp)
		if rc.FreeSpaceFloor != nil {
			f := *rc.FreeSpaceFloor
			r.FloorBytes = &f
		}
		if b, err := free(existingAncestor(rc.WorktreeRoot)); err == nil {
			r.FreeBytes = &b
		}
		r.Preserved = c.preserved(ctx, rp.Main)
		if st, err := state.Open(rp.CommonDir).ReadUnlocked(); err == nil {
			r.HaveState = true
			r.Worktrees = map[string]int{}
			for _, w := range st.Worktrees {
				eff := w.Effective(now)
				r.Worktrees[string(eff)]++
				if eff == state.Broken {
					r.Broken++
				}
				if w.Lease == nil {
					continue
				}
				switch {
				case eff == state.LeaseExpired && w.Lease.Expiry != nil:
					if a := now.Sub(*w.Lease.Expiry).Seconds(); a > r.OldestExpired {
						r.OldestExpired = a
					}
				case eff == state.Leased && w.Lease.Expiry == nil:
					if a := now.Sub(w.Lease.Acquired).Seconds(); a > r.OldestNoExpiry {
						r.OldestNoExpiry = a
					}
				}
			}
		}
		snap.Repos = append(snap.Repos, r)
	}
	return snap
}

// preserved counts the preserved branches by their refs; nil when git fails.
func (c Collector) preserved(ctx context.Context, main string) *int {
	out, err := c.Git.Exec(ctx, main, gitTimeout, "for-each-ref", "--format=%(refname)", "refs/heads/pg-gwm/")
	if err != nil {
		return nil
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), PreservedPrefix) {
			n++
		}
	}
	return &n
}
