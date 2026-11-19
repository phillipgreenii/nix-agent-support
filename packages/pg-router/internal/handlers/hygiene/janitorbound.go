package hygiene

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
)

// Per-run bound of the janitor's closed-bead reconcile: its resume cursor. It
// rides on the pass context, so the pass keeps its signature and a caller
// without it (a dispatch, the inline sweeps) behaves as before.

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
