package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// Per-run bounds of the janitor's passes: the once-per-run trash purge and the
// closed-bead reconcile resume cursor. Both ride on the pass context, so the
// passes keep their signatures and a caller without them (a dispatch, the inline
// sweeps) behaves as before.

// trashPurged is the set of worktree directories whose trash this janitor run
// has already purged. Every role that shares one worktree directory would
// otherwise repeat the same drain inside its own share.
type trashPurged struct {
	mu   sync.Mutex
	dirs map[string]bool
}

type trashPurgedKey struct{}

// withTrashPurged returns ctx carrying an empty set.
func withTrashPurged(ctx context.Context) context.Context {
	return context.WithValue(ctx, trashPurgedKey{}, &trashPurged{dirs: map[string]bool{}})
}

// firstPurge reports whether dir has not been claimed in this run yet and claims
// it. With no set on ctx every call is a first one.
func firstPurge(ctx context.Context, dir string) bool {
	t, _ := ctx.Value(trashPurgedKey{}).(*trashPurged)
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dirs[dir] {
		return false
	}
	t.dirs[dir] = true
	return true
}

// trashShareDivisor is the part of the pass context's remaining time one trash
// purge may spend: 1/2, so the list, probe and scan stages of the sweep that
// follow it still get time inside the same role share.
const trashShareDivisor = 2

// trashBudget is the drain budget of a trash purge under ctx: base (the
// removal's own budget) bounded by 1/trashShareDivisor of the time ctx has
// left. A context with no deadline leaves base unchanged. A non-positive result
// means no time is left.
func trashBudget(ctx context.Context, base time.Duration) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return base
	}
	if b := time.Until(dl) / trashShareDivisor; b < base {
		return b
	}
	return base
}

// Closed-bead reconcile resume cursor. The pass walks its candidates in
// ExternalID order and, when its context ends between candidates, the next run
// continues after the last candidate it finished (and clears the cursor when it
// walks them all). The cursor is one small file per pool in the handler lock
// directory:
//
//	<lock dir>/closedbead-cursor--<hash>.cursor
const (
	cursorFilePrefix = "closedbead-cursor--"
	cursorFileSuffix = ".cursor"
)

type resumeKey struct{}

type resumeSpec struct{ dir, pool string }

// withResume returns ctx asking the closed-bead reconcile to resume across runs
// through a cursor file in dir, keyed by pool. An empty dir disables it.
func withResume(ctx context.Context, dir, pool string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, resumeKey{}, resumeSpec{dir: dir, pool: pool})
}

// resumeCursor is one pool+prefix's cursor file.
type resumeCursor struct{ path string }

// resumeFrom returns the cursor ctx asks for, for the given session prefix.
func resumeFrom(ctx context.Context, prefix string) (resumeCursor, bool) {
	spec, ok := ctx.Value(resumeKey{}).(resumeSpec)
	if !ok {
		return resumeCursor{}, false
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(spec.pool + "|" + prefix))
	return resumeCursor{path: keepLikeFile(spec.dir, cursorFilePrefix, cursorFileSuffix, fmt.Sprintf("%016x", h.Sum64()))}, true
}

// load returns the id of the last finished candidate, or "".
func (r resumeCursor) load() string {
	b, err := os.ReadFile(r.path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// save records id as the last finished candidate. Best effort: a failure only
// means the next run starts from the beginning.
func (r resumeCursor) save(id string) {
	if err := writeFileAtomic(r.path, []byte(id+"\n")); err != nil {
		slog.Warn("reconcile: cannot write the resume cursor", "err", err)
	}
}

// clear forgets the cursor (the whole pool was walked).
func (r resumeCursor) clear() { _ = os.Remove(r.path) }

// rotateAfter orders cands by ExternalID and keeps those after cursor (a resumed
// walk finishes the cycle and the next run starts from the beginning). An empty
// cursor, or one past every candidate (its candidates are gone), keeps them all.
func rotateAfter(cands []ccpool.Session, cursor string) []ccpool.Session {
	out := append([]ccpool.Session(nil), cands...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ExternalID < out[j].ExternalID })
	if cursor == "" {
		return out
	}
	i := sort.Search(len(out), func(i int) bool { return out[i].ExternalID > cursor })
	if i >= len(out) {
		return out
	}
	return out[i:]
}
