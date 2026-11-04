package parser

import (
	"container/heap"
	"errors"
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
//   - a source without a parser (HasSource false) is left out of the sums, the
//     maximum and the oldest-unparsed value;
//   - the lag-seconds origin is the seen time of the commit that CONTAINS the
//     generation's parse offset, so after a partial parse it moves to the next
//     commit's time. Each generation keeps its unparsed commits as an ordered
//     span list (dropped as the parse offset passes them, so it is bounded by
//     the unparsed commits);
//   - the oldest-unparsed value is the minimum, over generations, of the front
//     span's seen time, kept in a lazily pruned heap: an entry is valid only
//     if its generation's front span still equals it.
//
// Both offsets only ever grow (the store's guards), so an update applies the
// larger of the old and the new value; a repeated or stale delivery changes
// nothing. The hook runs under the store's write lock and never blocks: it
// takes only mu.
type lagState struct {
	hasSource func(string) bool
	sourceOf  func(file int64) (string, bool)

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

type lagSpan struct{ from, to, seen int64 }

type lagGen struct {
	copy, parse int64
	spans       []lagSpan // unparsed commits, ordered; to > parse
	counted     bool
	lastLag     int64   // the lag last pushed on the max heap
	front       lagSpan // the front span last pushed on the old heap (zero: none)
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
	// FrontSeen is the seen time of the unparsed commit containing the parse
	// offset; the zero time when nothing is unparsed (or the commit is unknown).
	FrontSeen time.Time
}

func newLagState(hasSource func(string) bool, sourceOf func(int64) (string, bool)) *lagState {
	return &lagState{hasSource: hasSource, sourceOf: sourceOf, counted: map[int64]bool{}, gens: map[lagKey]*lagGen{}}
}

// StartLag creates the maintained lag state, subscribes it to the store's
// OnOffsets hook and seeds it from the store with one whole-table read (the
// same read UpdateGauges does, run once). The hook is subscribed first and its
// deliveries are held until the seed is loaded, then replayed over it, so no
// change between the read and the subscription is lost; deliveries are
// idempotent, so one already inside the read is harmless.
func (p *Parser) StartLag() (*Lag, error) {
	st := p.cfg.Store
	l := newLagState(p.HasSource, func(file int64) (string, bool) {
		f, err := st.File(file)
		if err != nil {
			return "", false
		}
		return f.Source, true
	})
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
		commits, err := rd.UnparsedCommits()
		if err != nil {
			return err
		}
		l.seed(files, gens, commits)
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
func (l *lagState) seed(files []store.File, gens []store.Generation, commits []store.UnparsedCommit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range files {
		if l.hasSource(f.Source) {
			l.counted[f.ID] = true
		}
	}
	for _, g := range gens {
		k := lagKey{g.File, g.Gen}
		lg := &lagGen{copy: g.CopyOffset, parse: g.ParseOffset, counted: l.counted[g.File]}
		l.gens[k] = lg
		if lg.counted {
			l.sumCopy += lg.copy
			l.sumPars += lg.parse
		}
	}
	for _, c := range commits {
		if lg := l.gens[lagKey{c.File, c.Gen}]; lg != nil {
			lg.spans = append(lg.spans, lagSpan{c.From, c.To, c.SeenNS})
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
	src, ok := l.sourceOf(file)
	if !ok {
		return false
	}
	c := l.hasSource(src)
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
	if e.copy > lg.copy {
		lg.spans = append(lg.spans, lagSpan{from: lg.copy, to: e.copy, seen: e.at})
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
	}
	l.refresh(k, lg)
}

// refresh drops the spans the parse offset has passed and pushes the changed
// lag and front span of a counted generation on the heaps.
func (l *lagState) refresh(k lagKey, lg *lagGen) {
	n := 0
	for n < len(lg.spans) && lg.spans[n].to <= lg.parse {
		n++
	}
	if n > 0 {
		lg.spans = append(lg.spans[:0:0], lg.spans[n:]...)
	}
	if !lg.counted {
		return
	}
	if lag := lg.copy - lg.parse; lag != lg.lastLag {
		lg.lastLag = lag
		if lag > 0 {
			heap.Push(&l.maxH, lagMaxEntry{lag: lag, key: k})
		}
	}
	fr := lg.frontSpan()
	if fr != lg.front {
		lg.front = fr
		if fr.seen > 0 {
			heap.Push(&l.oldH, lagOldEntry{span: fr, key: k})
		}
	}
	if len(l.maxH) > 4*len(l.gens)+64 || len(l.oldH) > 4*len(l.gens)+64 {
		l.compact()
	}
}

// frontSpan is the unparsed commit that contains the parse offset (zero when
// there is none, which the oracle skips).
func (g *lagGen) frontSpan() lagSpan {
	if g.copy <= g.parse || len(g.spans) == 0 {
		return lagSpan{}
	}
	if s := g.spans[0]; s.from <= g.parse && g.parse < s.to {
		return s
	}
	return lagSpan{}
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
		if lg.front.seen > 0 {
			l.oldH = append(l.oldH, lagOldEntry{span: lg.front, key: k})
		}
	}
	heap.Init(&l.maxH)
	heap.Init(&l.oldH)
}

// Gauges returns the figures UpdateGauges computes: the largest per-generation
// lag in bytes, the age in seconds of the oldest unparsed commit (the seen time
// of the commit containing each lagging generation's parse offset), and
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
		if lg := l.gens[top.key]; lg != nil && lg.counted && lg.frontSpan() == top.span {
			if d := float64(now.UnixNano()-top.span.seen) / 1e9; d > 0 {
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
	if fr := lg.frontSpan(); fr.seen > 0 {
		out.FrontSeen = time.Unix(0, fr.seen)
	}
	return out
}

var errLagSeeded = errors.New("parser: lag state already seeded")

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
	span lagSpan
	key  lagKey
}
type lagOldHeap []lagOldEntry

func (h lagOldHeap) Len() int           { return len(h) }
func (h lagOldHeap) Less(i, j int) bool { return h[i].span.seen < h[j].span.seen }
func (h lagOldHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *lagOldHeap) Push(x any)        { *h = append(*h, x.(lagOldEntry)) }
func (h *lagOldHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}
