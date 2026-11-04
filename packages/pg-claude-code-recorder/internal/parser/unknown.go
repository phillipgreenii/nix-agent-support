package parser

import (
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// unknownState maintains the unknown_kinds input: the distinct sources that
// have a file but no parser, a deleted file not counting. It reproduces the
// kinds map of UpdateGauges (the oracle) and is fed by the store's
// OnFileState hook, so the store's file-record apply (a TFile record) adds a
// source and a state change to deleted removes it, with no whole-table read
// after the seed. A source never gains a parser at run time. The hook runs
// under the store's write lock and takes only mu.
type unknownState struct {
	hasSource func(string) bool

	mu      sync.Mutex
	live    map[int64]string // file -> source, for live files whose source has no parser
	bySrc   map[string]int   // source -> live file count
	seeding bool
	buf     []unknownEvent
}

// Unknown is the exported name of the maintained unknown-kinds state.
type Unknown = unknownState

type unknownEvent struct {
	file          int64
	source, state string
}

// StartUnknown creates the maintained unknown-kinds set, subscribes it to the
// store's OnFileState hook and seeds it with one whole-table read of the files
// (the same read the oracle does for kinds), run once. Deliveries are held
// until the seed is loaded and then replayed; they are idempotent.
func (p *Parser) StartUnknown() (*Unknown, error) {
	u := &unknownState{hasSource: p.HasSource, live: map[int64]string{}, bySrc: map[string]int{}, seeding: true}
	st := p.cfg.Store
	st.OnFileState(u.hook)
	err := st.ReadTx(func(rd *store.Reader) error {
		files, err := rd.AllFiles()
		if err != nil {
			return err
		}
		u.seed(files)
		return nil
	})
	if err != nil {
		u.mu.Lock()
		u.seeding = false
		u.buf = nil
		u.mu.Unlock()
		return nil, err
	}
	return u, nil
}

func (u *unknownState) seed(files []store.File) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, f := range files {
		u.applyLocked(unknownEvent{f.ID, f.Source, f.State})
	}
	u.seeding = false
	buf := u.buf
	u.buf = nil
	for _, e := range buf {
		u.applyLocked(e)
	}
}

func (u *unknownState) hook(file int64, source, state string) {
	e := unknownEvent{file, source, state}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.seeding {
		u.buf = append(u.buf, e)
		return
	}
	u.applyLocked(e)
}

func (u *unknownState) applyLocked(e unknownEvent) {
	want := e.state != "deleted" && !u.hasSource(e.source)
	old, have := u.live[e.file]
	if have && (!want || old != e.source) {
		delete(u.live, e.file)
		if u.bySrc[old]--; u.bySrc[old] <= 0 {
			delete(u.bySrc, old)
		}
		have = false
	}
	if want && !have {
		u.live[e.file] = e.source
		u.bySrc[e.source]++
	}
}

// Count is the number of distinct unknown sources: the unknown_kinds value.
func (u *unknownState) Count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bySrc)
}
