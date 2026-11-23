package serve

import (
	"context"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// The status loop's coverage pass used to walk every file row each refresh, to
// count the files and find the worst lag, although a file whose bytes are all
// copied and parsed adds only to the counts and those change only with its row,
// its path or its offsets. covCache keeps each file's share of the counts and
// redoes only the files that can have changed since the last pass:
//
//   - a file the file tracker named (a row or path record committed),
//   - a file whose offsets changed (the store's OnOffsets hook, covTouched),
//   - a "volatile" file: one that was fresh, lagging or Lstat'ed last time, or
//     is fresh now. Its figures depend on the clock and the disk, so it is
//     redone on every pass, and a file that stops being fresh is redone once
//     more on the pass that finds it no longer fresh.
//
// A file that is none of these cannot have changed any figure, because a lag
// figure moves only through an offsets delivery and the counts only through the
// row, the path and the configured roots. Its lag figures are all zero, so it
// takes no part in the maxima. The result is identical to computeCoverageOver
// in statLazy mode (the differential test pins that).
type covCache struct {
	ok         bool
	roots      []string
	paths      map[int64][]string
	contrib    map[int64]fileContrib
	files      int
	removed    int
	kinds      map[string]int
	rootCounts map[string]int
	volatile   map[int64]struct{}
	// evals is how many file figures were computed over the cache's life (a
	// test reads it to see which files a pass redid).
	evals int
}

// fileContrib is one file's share of the counts.
type fileContrib struct {
	counted bool // a live file with a path (Files)
	removed bool // a deleted original (Removed)
	kind    string
	root    string
	hasRoot bool
}

// volFile is a volatile file's figures of the current pass.
type volFile struct {
	f  store.File
	p  string
	fc wire.FileCoverage
	gl parser.GenLag
}

func (c *covCache) reset(roots []string) {
	*c = covCache{
		evals:      c.evals,
		ok:         true,
		roots:      slices.Clone(roots),
		contrib:    map[int64]fileContrib{},
		kinds:      map[string]int{},
		rootCounts: map[string]int{},
		volatile:   map[int64]struct{}{},
	}
}

func (c *covCache) add(k fileContrib, sign int) {
	if k.counted {
		c.files += sign
	}
	if k.removed {
		c.removed += sign
	}
	if k.kind != "" {
		if c.kinds[k.kind] += sign; c.kinds[k.kind] == 0 {
			delete(c.kinds, k.kind)
		}
	}
	if k.hasRoot {
		if c.rootCounts[k.root] += sign; c.rootCounts[k.root] == 0 {
			delete(c.rootCounts, k.root)
		}
	}
}

// coverageIncremental is the status loop's coverage pass (summary view, lazy
// stat mode, no summary aggregates) over the incremental state.
func (d *Daemon) coverageIncremental(ctx context.Context) (wire.Coverage, error) {
	d.covMu.Lock()
	defer d.covMu.Unlock()
	c := &d.covc
	if err := ctx.Err(); err != nil {
		return wire.Coverage{}, err
	}
	d.rowsMu.Lock()
	if err := d.syncFileRows(ctx); err != nil {
		d.rowsMu.Unlock()
		c.ok = false
		return wire.Coverage{}, err
	}
	view := d.rowsFiles
	dirty, full := d.covDirty, d.covFull
	d.covDirty, d.covFull = nil, false
	d.rowsMu.Unlock()
	d.touchedMu.Lock()
	touched := d.covTouched
	d.covTouched = nil
	d.touchedMu.Unlock()

	now := d.clock.Now()
	roots := d.cfg.Roots()
	if !c.ok || !slices.Equal(roots, c.roots) {
		full = true
	}
	fresh := d.freshTouched(now)
	stages, stagesOK := d.cachedStages()

	var paths map[int64][]string
	if full || len(dirty) > 0 || !stagesOK {
		err := d.st.ReadTxCtx(ctx, func(rd *store.Reader) error {
			var e error
			switch {
			case full:
				paths, e = rd.AllCurrentPaths()
			case len(dirty) > 0:
				ids := make([]int64, 0, len(dirty))
				for id := range dirty {
					ids = append(ids, id)
				}
				paths, e = rd.CurrentPathsByIDs(ids)
			}
			if e != nil {
				return e
			}
			if !stagesOK {
				if rows, serr := rd.Stages(); serr == nil {
					stages, stagesOK = rows, true
				}
			}
			return nil
		})
		if err != nil {
			// The ids taken are gone from the trackers: rebuild on the next pass.
			c.ok = false
			d.rowsMu.Lock()
			d.covFull = true
			d.rowsMu.Unlock()
			return wire.Coverage{}, err
		}
	}

	prefixes := make([]string, len(roots))
	for i, root := range roots {
		prefixes[i] = root + string(os.PathSeparator)
	}
	vols := map[int64]volFile{}
	exceptions := 0
	eval := func(f store.File) {
		id := f.ID
		c.evals++
		k := fileContrib{}
		delete(c.volatile, id)
		switch ps := c.paths[id]; {
		case f.State == "deleted":
			k.removed = true
		case len(ps) > 0:
			p := ps[0]
			k.counted = true
			if !d.prs.HasSource(f.Source) {
				k.kind = f.Source
			}
			for i, root := range roots {
				if strings.HasPrefix(p, prefixes[i]) {
					k.root, k.hasRoot = root, true
					break
				}
			}
			gl, _ := d.lag.Generation(id, f.CurGen)
			fc, stat := d.lazyFileCoverage(f, p, gl, fresh[id], now)
			if stat {
				exceptions++
			}
			if stat || fc.ParseLagBytes > 0 || fc.CopyLagBytes > 0 || fc.CopyLagS > 0 {
				c.volatile[id] = struct{}{}
				vols[id] = volFile{f: f, p: p, fc: fc, gl: gl}
			}
		}
		if old, had := c.contrib[id]; had {
			c.add(old, -1)
		}
		c.contrib[id] = k
		c.add(k, 1)
	}

	if full {
		c.reset(roots)
		c.paths = paths
		if c.paths == nil {
			c.paths = map[int64][]string{}
		}
		for f := range view.All() {
			eval(f)
		}
	} else {
		for id := range dirty {
			if ps, ok := paths[id]; ok {
				c.paths[id] = ps
			} else {
				delete(c.paths, id)
			}
		}
		redo := make(map[int64]struct{}, len(dirty)+len(touched)+len(c.volatile)+len(fresh))
		for id := range dirty {
			redo[id] = struct{}{}
		}
		for id := range touched {
			redo[id] = struct{}{}
		}
		for id := range c.volatile {
			redo[id] = struct{}{}
		}
		for id := range fresh {
			redo[id] = struct{}{}
		}
		for id := range redo {
			if f, ok := view.get(id); ok {
				eval(f)
			}
		}
	}

	var cov wire.Coverage
	cov.Files, cov.Removed = c.files, c.removed
	cov.Roots = make([]wire.RootCoverage, 0, len(roots))
	ids := make([]int64, 0, len(c.volatile))
	for id := range c.volatile {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		v, ok := vols[id]
		if !ok { // a row that is gone: it takes no part
			delete(c.volatile, id)
			continue
		}
		accumulateLag(&cov, v.f, v.p, v.fc, v.gl, now)
	}
	d.finishCoverage(&cov, roots, c.rootCounts, c.kinds, nil, stages, stagesOK, statLazy, exceptions)
	return cov, nil
}
