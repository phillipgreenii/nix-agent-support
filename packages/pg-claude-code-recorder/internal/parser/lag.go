package parser

import (
	"container/heap"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// lagState maintains the parse-lag figures (lag bytes, lag seconds, progress)
// from the store's OnOffsets hook, so a gauge read costs O(1) or O(log n)
// instead of a whole-table read. It reproduces UpdateGauges (the oracle)
// exactly:
//
//   - every generation, old ones included, stays in the sums and in the maximum
//     lag; a deleted file is not special here;
//   - a source without a parser (HasSource false) and a snapshot-mode file (parsed
//     by version, its parse offset never moves) are left out of the sums, the
//     maximum and the oldest-unparsed value;
//   - the lag-seconds origin is one in-memory value per generation: how long
//     parse has been behind WITHOUT ADVANCING (operator ruling, 2026-11-05). It
//     is the later of the time copy first got ahead of parse and the time parse
//     last advanced its offset while still behind. A partial parse that moves
//     the parse offset sets it to that time; catching up clears it; a second
//     copy while still behind does not move it. It is NOT the age since copy
//     first got ahead and NOT the age of the oldest unparsed byte. It is not in
//     the commits table: at start every generation with parse behind copy is
//     seeded to "now", so the lag is understated once after a restart and
//     correct from then on;
//   - the oldest-unparsed value is the minimum of that origin over generations,
//     kept in a lazily pruned heap: an entry is valid only while its
//     generation's origin still equals it.
//
// Both offsets only ever grow (the store's guards), so an update applies the
// larger of the old and the new value; a repeated or stale delivery changes
// nothing. The hook runs under the store's write lock and never blocks: it
// takes only mu.
type lagState struct {
	counts   func(source, mode string) bool // a file of this source and mode lags by parse offset
	sourceOf func(file int64) (source, mode string, ok bool)
	now      func() time.Time // the injected clock, for the start-up seed

	mu               sync.Mutex
	counted          map[int64]bool // file -> its source has a parser (positive lookups only)
	gens             map[lagKey]*lagGen
	sumCopy, sumPars int64
	maxH             lagMaxHeap
	oldH             lagOldHeap
	seeding          bool
	buf              []lagEvent
}

// Lag is the exported name of the maintained lag state, for the daemon.
type Lag = lagState

type lagKey struct {
	file int64
	gen  int
}

type lagGen struct {
	copy, parse int64
	since       int64 // unix ns parse was last known to advance while behind (or copy first got ahead); 0 when parse has caught up
	counted     bool
	lastLag     int64 // the lag last pushed on the max heap
	lastSince   int64 // the origin last pushed on the old heap (0: none)
}

type lagEvent struct {
	file        int64
	gen         int
	copy, parse int64
	at          int64
}

// GenLag is one generation's figures, for the status snapshot.
type GenLag struct {
	Gen         int
	CopyOffset  int64
	ParseOffset int64
	// UnparsedSince is the origin of the parse lag: the later of the time copy
	// first got ahead of parse and the time parse last advanced while behind
	// (the start-up time for a generation already behind at start); the zero
	// time when parse has caught up.
	UnparsedSince time.Time
}

func newLagState(counts func(source, mode string) bool, sourceOf func(int64) (string, string, bool), now func() time.Time) *lagState {
	return &lagState{counts: counts, sourceOf: sourceOf, now: now, counted: map[int64]bool{}, gens: map[lagKey]*lagGen{}}
}

// StartLag creates the maintained lag state, subscribes it to the store's
// OnOffsets hook and seeds it from the store with one read of the files and
// generations (every generation with parse behind copy starts its lag clock at
// the current time). The hook is subscribed first and its
// deliveries are held until the seed is loaded, then replayed over it, so no
// change between the read and the subscription is lost; deliveries are
// idempotent, so one already inside the read is harmless.
func (p *Parser) StartLag() (*Lag, error) {
	st := p.cfg.Store
	l := newLagState(p.countsLag, func(file int64) (string, string, bool) {
		f, err := st.File(file)
		if err != nil {
			return "", "", false
		}
		return f.Source, f.Mode, true
	}, p.cfg.Clock.Now)
	l.seeding = true
	st.OnOffsets(l.hook)
	err := st.ReadTx(func(rd *store.Reader) error {
		files, err := rd.AllFiles()
		if err != nil {
			return err
		}
		gens, err := rd.AllGenerations()
		if err != nil {
			return err
		}
		l.seed(files, gens)
		return nil
	})
	if err != nil {
		l.mu.Lock()
		l.seeding = false
		l.buf = nil
		l.mu.Unlock()
		return nil, err
	}
	return l, nil
}

// seed loads the read and replays the held deliveries.
func (l *lagState) seed(files []store.File, gens []store.Generation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	start := l.now().UnixNano()
	for _, f := range files {
		if l.counts(f.Source, f.Mode) {
			l.counted[f.ID] = true
		}
	}
	for _, g := range gens {
		k := lagKey{g.File, g.Gen}
		lg := &lagGen{copy: g.CopyOffset, parse: g.ParseOffset, counted: l.counted[g.File]}
		l.gens[k] = lg
		if lg.copy > lg.parse {
			lg.since = start
		}
		if lg.counted {
			l.sumCopy += lg.copy
			l.sumPars += lg.parse
		}
	}
	for k, lg := range l.gens {
		l.refresh(k, lg)
	}
	l.seeding = false
	buf := l.buf
	l.buf = nil
	for _, e := range buf {
		l.applyLocked(e)
	}
}

// hook is the store's OnOffsets subscriber, and the only way the state changes
// after the seed.
func (l *lagState) hook(file int64, gen int, copyOffset, parseOffset int64, at time.Time) {
	e := lagEvent{file: file, gen: gen, copy: copyOffset, parse: parseOffset, at: at.UnixNano()}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seeding {
		l.buf = append(l.buf, e)
		return
	}
	l.applyLocked(e)
}

func (l *lagState) fileCounted(file int64) bool {
	if c, ok := l.counted[file]; ok {
		return c
	}
	src, mode, ok := l.sourceOf(file)
	if !ok {
		return false
	}
	c := l.counts(src, mode)
	l.counted[file] = c
	return c
}

func (l *lagState) applyLocked(e lagEvent) {
	k := lagKey{e.file, e.gen}
	lg := l.gens[k]
	if lg == nil {
		lg = &lagGen{counted: l.fileCounted(e.file)}
		l.gens[k] = lg
	}
	wasBehind := lg.copy > lg.parse
	parseAdvanced := false
	if e.copy > lg.copy {
		if lg.counted {
			l.sumCopy += e.copy - lg.copy
		}
		lg.copy = e.copy
	}
	if e.parse > lg.parse {
		if lg.counted {
			l.sumPars += e.parse - lg.parse
		}
		lg.parse = e.parse
		parseAdvanced = true
	}
	switch {
	case lg.copy <= lg.parse:
		lg.since = 0
	case !wasBehind || parseAdvanced:
		lg.since = e.at
		if lg.since <= 0 {
			lg.since = l.now().UnixNano()
		}
	}
	l.refresh(k, lg)
}

// refresh pushes the changed lag and unparsed-since origin of a counted
// generation on the heaps.
func (l *lagState) refresh(k lagKey, lg *lagGen) {
	if !lg.counted {
		return
	}
	if lag := lg.copy - lg.parse; lag != lg.lastLag {
		lg.lastLag = lag
		if lag > 0 {
			heap.Push(&l.maxH, lagMaxEntry{lag: lag, key: k})
		}
	}
	if lg.since != lg.lastSince {
		lg.lastSince = lg.since
		if lg.since > 0 {
			heap.Push(&l.oldH, lagOldEntry{since: lg.since, key: k})
		}
	}
	if len(l.maxH) > 4*len(l.gens)+64 || len(l.oldH) > 4*len(l.gens)+64 {
		l.compact()
	}
}

// compact rebuilds both heaps from the generations, shedding stale entries.
func (l *lagState) compact() {
	l.maxH, l.oldH = nil, nil
	for k, lg := range l.gens {
		if !lg.counted {
			continue
		}
		if lg.lastLag > 0 {
			l.maxH = append(l.maxH, lagMaxEntry{lag: lg.lastLag, key: k})
		}
		if lg.lastSince > 0 {
			l.oldH = append(l.oldH, lagOldEntry{since: lg.lastSince, key: k})
		}
	}
	heap.Init(&l.maxH)
	heap.Init(&l.oldH)
}

// Gauges returns the figures UpdateGauges computes: the largest per-generation
// lag in bytes, the age in seconds of the oldest lag origin (per lagging
// generation, the time parse last advanced while behind or copy got ahead), and
// parsed/copied bytes (1 when nothing was copied).
func (l *lagState) Gauges(now time.Time) (lagBytes int64, lagSecs, progress float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.maxH) > 0 {
		top := l.maxH[0]
		if lg := l.gens[top.key]; lg != nil && lg.counted && lg.copy-lg.parse == top.lag {
			lagBytes = top.lag
			break
		}
		heap.Pop(&l.maxH)
	}
	for len(l.oldH) > 0 {
		top := l.oldH[0]
		if lg := l.gens[top.key]; lg != nil && lg.counted && lg.since == top.since {
			if d := float64(now.UnixNano()-top.since) / 1e9; d > 0 {
				lagSecs = d
			}
			break
		}
		heap.Pop(&l.oldH)
	}
	progress = 1.0
	if l.sumCopy > 0 {
		progress = float64(l.sumPars) / float64(l.sumCopy)
	}
	return lagBytes, lagSecs, progress
}

// Generations is the per-file, per-generation snapshot (every file, with or
// without a parser), ordered by file then generation.
func (l *lagState) Generations() map[int64][]GenLag {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[int64][]GenLag{}
	for k, lg := range l.gens {
		out[k.file] = append(out[k.file], l.genLag(k, lg))
	}
	for _, v := range out {
		sort.Slice(v, func(i, j int) bool { return v[i].Gen < v[j].Gen })
	}
	return out
}

// CurrentGenerations returns, for each file in cur (file -> its current
// generation), that generation's snapshot; a file with no tracked current
// generation is absent. It takes mu once and copies only the matching entries
// by value, so the result is private to the caller and the lock is held no
// longer than one pass over the generations (Generations also sorts per file).
func (l *lagState) CurrentGenerations(cur map[int64]int) map[int64]GenLag {
	out := make(map[int64]GenLag, len(cur))
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, lg := range l.gens {
		if g, ok := cur[k.file]; ok && g == k.gen {
			out[k.file] = l.genLag(k, lg)
		}
	}
	return out
}

// FileGenerations is Generations for one file.
func (l *lagState) FileGenerations(file int64) []GenLag {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []GenLag
	for k, lg := range l.gens {
		if k.file == file {
			out = append(out, l.genLag(k, lg))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Gen < out[j].Gen })
	return out
}

func (l *lagState) genLag(k lagKey, lg *lagGen) GenLag {
	out := GenLag{Gen: k.gen, CopyOffset: lg.copy, ParseOffset: lg.parse}
	if lg.since > 0 {
		out.UnparsedSince = time.Unix(0, lg.since)
	}
	return out
}

type lagMaxEntry struct {
	lag int64
	key lagKey
}
type lagMaxHeap []lagMaxEntry

func (h lagMaxHeap) Len() int           { return len(h) }
func (h lagMaxHeap) Less(i, j int) bool { return h[i].lag > h[j].lag }
func (h lagMaxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *lagMaxHeap) Push(x any)        { *h = append(*h, x.(lagMaxEntry)) }
func (h *lagMaxHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

type lagOldEntry struct {
	since int64
	key   lagKey
}
type lagOldHeap []lagOldEntry

func (h lagOldHeap) Len() int           { return len(h) }
func (h lagOldHeap) Less(i, j int) bool { return h[i].since < h[j].since }
func (h lagOldHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *lagOldHeap) Push(x any)        { *h = append(*h, x.(lagOldEntry)) }
func (h *lagOldHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}
