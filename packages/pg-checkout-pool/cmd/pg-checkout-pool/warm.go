package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
	"github.com/phillipgreenii/pg-checkout-pool/internal/provision"
)

const defaultPrewarmDelay = 30 * time.Second

type prewarmOut struct {
	Target  int      `json:"target"`
	Active  int      `json:"active"`
	Created []string `json:"created"`
	Stopped string   `json:"stopped,omitempty"`
}

// activeSlots counts the slots that are not retired.
func activeSlots(st *pool.Store) (int, error) {
	ids, err := st.Slots()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		r, err := st.ReadRecord(id)
		if err != nil {
			return 0, err
		}
		if r.State != pool.Retired {
			n++
		}
	}
	return n, nil
}

// prewarm grows a cold pool toward --count slots, one slot at a time with a
// delay between creations. Each creation goes through grow, so it is
// serialised by the pool lock and refused below the free-space floor.
func (a *app) prewarm(args []string) int {
	fs, root := a.newFlags("prewarm")
	repo := fs.String("repo", "", "absolute path of the canonical clone")
	ref := fs.String("ref", "", "commit reference the new slots are created at")
	count := fs.Int("count", 0, "number of slots the pool should hold")
	delay := fs.Duration("delay", defaultPrewarmDelay, "pause between two slot creations")
	maxSlots := fs.Int("max-slots", defaultMaxSlots, "pool size cap")
	minFree := fs.Uint64("min-free-bytes", defaultMinFree, "free-space floor below which the pool will not grow")
	var keep multiFlag
	fs.Var(&keep, "keep", "ignored path that survives a reset (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	switch {
	case *repo == "" || !filepath.IsAbs(*repo):
		return a.usageErr("prewarm: --repo must be an absolute path")
	case *ref == "":
		return a.usageErr("prewarm: --ref is required")
	case *count < 1 || *delay < 0 || *maxSlots < 1:
		return a.usageErr("prewarm: --count and --max-slots must be at least 1 and --delay non-negative")
	}
	prov, err := a.newProv(*repo, keep)
	if err != nil {
		return a.usageErr("prewarm: %v", err)
	}
	ctx := context.Background()
	commit, err := prov.Resolve(ctx, *ref)
	if err != nil {
		return a.fail(err)
	}
	st := pool.NewStore(*root, a.clock)
	out := prewarmOut{Target: min(*count, *maxSlots), Created: []string{}}
	created := map[string]bool{}
	for {
		n, err := activeSlots(st)
		if err != nil {
			return a.fail(err)
		}
		out.Active = n
		if n >= out.Target {
			return a.emit(out)
		}
		if len(out.Created) > 0 {
			a.sleep(*delay)
		}
		before := len(created)
		grew, err := a.grow(ctx, prov, st, *root, commit, *maxSlots, *minFree, created)
		if err != nil {
			out.Stopped = err.Error()
			if code := a.emit(out); code != 0 {
				return code
			}
			return a.fail(err)
		}
		if !grew {
			out.Stopped = "pool is at its maximum size"
			return a.emit(out)
		}
		for id := range created {
			if len(created) > before && !contains(out.Created, id) {
				out.Created = append(out.Created, id)
			}
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

type revalidateEntry struct {
	Slot   string `json:"slot"`
	Action string `json:"action"`
	Error  string `json:"error,omitempty"`
}

// revalidate checks every idle slot (warm-free) against the repository, for
// use after the tooling or the repository setup changed (a deployment apply).
// A slot that is no longer a healthy checkout is quarantined. Leased,
// resetting and held slots are left alone.
func (a *app) revalidate(args []string) int {
	fs, root := a.newFlags("revalidate")
	repo := fs.String("repo", "", "absolute path of the canonical clone")
	var keep multiFlag
	fs.Var(&keep, "keep", "ignored path that survives a reset (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *repo == "" || !filepath.IsAbs(*repo) {
		return a.usageErr("revalidate: --repo must be an absolute path")
	}
	prov, err := a.newProv(*repo, keep)
	if err != nil {
		return a.usageErr("revalidate: %v", err)
	}
	st := pool.NewStore(*root, a.clock)
	ids, err := st.Slots()
	if err != nil {
		return a.fail(err)
	}
	ctx := context.Background()
	entries := []revalidateEntry{}
	bad := false
	for _, id := range ids {
		h, err := st.Lock(id)
		if err != nil {
			if errors.Is(err, pool.ErrBusy) {
				entries = append(entries, revalidateEntry{Slot: id, Action: "skipped-busy"})
				continue
			}
			return a.fail(err)
		}
		e := revalidateEntry{Slot: id, Action: "ok"}
		if h.Record().State != pool.WarmFree {
			e.Action = "skipped-" + string(h.Record().State)
		} else if verr := prov.Verify(ctx, treePath(*root, id)); verr != nil {
			e.Action, e.Error = "quarantined", verr.Error()
			if qerr := quarantineSlot(h, fmt.Errorf("revalidate: %w", verr)); qerr != nil && h.Record().State != pool.Quarantined {
				e.Error = qerr.Error()
			}
			bad = true
		}
		h.Unlock()
		entries = append(entries, e)
	}
	if code := a.emit(entries); code != 0 {
		return code
	}
	if bad {
		return exitInternal
	}
	return 0
}

var _ = provision.ErrUnhealthy
