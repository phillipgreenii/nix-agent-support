package serve

import (
	"context"
	"iter"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// fileChunk is the row count of one chunk of a fileView. A change copies only
// the chunks it touches, so its cost is bounded by this, not by the file count.
var fileChunk = 1024

// rowsRebuildEvery is how often the status file rows are read whole again, to
// catch an update the tracker never reported. The count and maximum id check
// that catches a missed insert runs on every incremental refresh instead.
const rowsRebuildEvery = time.Hour

// fileView is the id-ascending file rows as a list of chunks. It is immutable
// once published: a change builds a new view that shares every untouched chunk
// with the old one, so a reader iterating an older view is never mutated and a
// change does not copy the whole table. No chunk is empty.
type fileView struct {
	chunks [][]store.File
	n      int
}

// viewOf wraps rows (id-ascending, not mutated afterwards) as a view without
// copying them.
func viewOf(rows []store.File) fileView {
	v := fileView{n: len(rows)}
	for i := 0; i < len(rows); i += fileChunk {
		j := min(i+fileChunk, len(rows))
		v.chunks = append(v.chunks, rows[i:j:j])
	}
	return v
}

// Len is the number of rows.
func (v fileView) Len() int { return v.n }

// All iterates the rows in ascending id order.
func (v fileView) All() iter.Seq[store.File] {
	return func(yield func(store.File) bool) {
		for _, c := range v.chunks {
			for _, f := range c {
				if !yield(f) {
					return
				}
			}
		}
	}
}

// maxID is the largest id (0 when empty).
func (v fileView) maxID() int64 {
	if len(v.chunks) == 0 {
		return 0
	}
	c := v.chunks[len(v.chunks)-1]
	return c[len(c)-1].ID
}

// get returns the row with the id.
func (v fileView) get(id int64) (store.File, bool) {
	ci := sort.Search(len(v.chunks), func(i int) bool { return v.chunks[i][len(v.chunks[i])-1].ID >= id })
	if ci == len(v.chunks) {
		return store.File{}, false
	}
	c := v.chunks[ci]
	i := sort.Search(len(c), func(i int) bool { return c[i].ID >= id })
	if i < len(c) && c[i].ID == id {
		return c[i], true
	}
	return store.File{}, false
}

// with returns a view in which each of rows (id-ascending) replaces the row of
// the same id, or is inserted in id order when there is none. v is not changed.
func (v fileView) with(rows []store.File) fileView {
	if len(rows) == 0 {
		return v
	}
	if len(v.chunks) == 0 {
		return mergeInto(nil, rows)
	}
	out := fileView{chunks: make([][]store.File, 0, len(v.chunks)+1), n: v.n}
	i := 0
	for ci, c := range v.chunks {
		j := i
		for j < len(rows) && (ci == len(v.chunks)-1 || rows[j].ID < v.chunks[ci+1][0].ID) {
			j++
		}
		if j == i {
			out.chunks = append(out.chunks, c)
			continue
		}
		m := mergeInto(c, rows[i:j])
		out.n += m.n - len(c)
		out.chunks = append(out.chunks, m.chunks...)
		i = j
	}
	return out
}

// mergeInto merges the id-ascending rows into the id-ascending chunk c (nil is
// allowed) as new storage and cuts it into chunks.
func mergeInto(c, rows []store.File) fileView {
	m := make([]store.File, 0, len(c)+len(rows))
	a, b := 0, 0
	for a < len(c) || b < len(rows) {
		switch {
		case b == len(rows) || (a < len(c) && c[a].ID < rows[b].ID):
			m = append(m, c[a])
			a++
		case a == len(c) || rows[b].ID < c[a].ID:
			m = append(m, rows[b])
			b++
		default: // the same id: the new row wins
			m = append(m, rows[b])
			a++
			b++
		}
	}
	return viewOf(m)
}

// statusRows returns the file rows and current paths. The file rows are kept
// current from the store's file tracker: only the rows a commit named are
// reread, and the old view is never changed, so a reader iterating one is safe.
// The paths, which change only with a path, path-close or tombstone record, are
// rebuilt only when the path counter (or the rollback epoch) moved.
func (d *Daemon) statusRows(ctx context.Context, rd *store.Reader) (fileView, map[int64][]string, error) {
	// Read before the queries: a commit in between leaves the cache one step
	// behind, never ahead.
	pseq, epoch := d.st.PathSeq(), d.st.RollbackEpoch()
	d.rowsMu.Lock()
	defer d.rowsMu.Unlock()
	if err := d.syncFileRows(ctx); err != nil {
		return fileView{}, nil, err
	}
	if !d.pathsOK || d.pathsSeq != pseq || d.pathsEpoch != epoch {
		paths, err := rd.AllCurrentPaths()
		if err != nil {
			return fileView{}, nil, err
		}
		d.rowsPaths, d.pathsSeq, d.pathsEpoch, d.pathsOK = paths, pseq, epoch, true
	}
	return d.rowsFiles, d.rowsPaths, nil
}

// syncFileRows brings d.rowsFiles up to date; the caller holds rowsMu.
//
// It MUST take from the tracker on every call, a cache hit included (a tracker
// that is never drained keeps every changed id forever), and MUST do so BEFORE
// it opens its own read transaction: a change committed after Take is reported
// by the next Take, and one already visible is merely read twice. Taking inside
// a snapshot opened earlier could clear an id whose newer row that snapshot
// cannot see. Any read error invalidates the cache.
func (d *Daemon) syncFileRows(ctx context.Context) error {
	if d.rowsTrk == nil {
		d.rowsTrk = d.st.TrackFiles()
	}
	ids, full := d.rowsTrk.Take()
	if d.afterTake != nil {
		d.afterTake()
	}
	now := d.clock.Now()
	if d.rowsOK && !full && now.Sub(d.rowsBuilt) < rowsRebuildEvery {
		if len(ids) == 0 {
			return nil
		}
	} else {
		ids = nil
		full = true
	}
	var view fileView
	var whole bool
	err := d.st.ReadTxCtx(ctx, func(rd *store.Reader) error {
		var e error
		view, whole, e = d.readFileRows(rd, ids, full)
		return e
	})
	if err != nil {
		d.rowsFiles, d.rowsOK = fileView{}, false
		d.covFull = true
		return err
	}
	if whole {
		d.rowsBuilt = now
		d.covFull = true
	} else {
		if d.covDirty == nil {
			d.covDirty = make(map[int64]struct{}, len(ids))
		}
		for _, id := range ids {
			d.covDirty[id] = struct{}{}
		}
	}
	d.rowsFiles, d.rowsOK = view, true
	return nil
}

// readFileRows reads the view: the whole table when full, else the cached view
// with the rows of ids replaced. The incremental result is checked against the
// table's row count and largest id (a missed insert shows there) and replaced
// by a whole read on a mismatch or when a named id has no row. whole says the
// table was read whole.
func (d *Daemon) readFileRows(rd *store.Reader, ids []int64, full bool) (view fileView, whole bool, err error) {
	all := func() (fileView, bool, error) {
		rows, err := rd.AllFiles()
		if err != nil {
			return fileView{}, false, err
		}
		return viewOf(rows), true, nil
	}
	if full {
		return all()
	}
	rows, err := rd.FilesByIDs(ids)
	if err != nil {
		return fileView{}, false, err
	}
	if len(rows) != len(ids) {
		return all()
	}
	view = d.rowsFiles.with(rows)
	var n int
	var maxID int64
	if err := rd.Q().QueryRow(`SELECT COUNT(*), COALESCE(MAX(id),0) FROM files`).Scan(&n, &maxID); err != nil {
		return fileView{}, false, err
	}
	if n != view.n || maxID != view.maxID() {
		return all()
	}
	return view, false, nil
}
