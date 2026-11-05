package parser

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// sampleEvery is how often the per-statement clock runs: one line in 16, and
// the sampled seconds are scaled by the same factor.
const sampleEvery = 16

// slowestKept is how many of the slowest files a rebuild reports.
const slowestKept = 5

// stepAcc is one worker's step accumulator: plain integers, no locks, added to
// the store once per chunk (flushSteps) and never once per record. A nil
// accumulator, or one with on false, reads no clock.
type stepAcc struct {
	now      func() time.Duration
	on       bool
	st       store.StepStats
	seq      *int64 // the parser's line counter, picks the sampled lines (one parse runs at a time)
	sample   bool   // the line being inserted times its statements
	inInsert bool   // statements are attributed to a table only inside insert
}

// newAcc returns an accumulator on the store's clock.
func (p *Parser) newAcc() *stepAcc {
	return &stepAcc{now: p.cfg.Store.StepNow(), on: !ablateNoStepTimers && p.cfg.Store.StepTimers(), seq: &p.lineSeq}
}

// flushSteps adds the accumulator to the store (and so the registry) and
// returns the nanoseconds of read, parse and insert it held, the file's own
// share of the step time.
func (p *Parser) flushSteps(tx *pstx) int64 {
	a := tx.acc
	if a == nil {
		return 0
	}
	for i := range a.st.TableNS {
		a.st.TableNS[i] *= sampleEvery
	}
	own := a.st.StepNS[store.StepRead] + a.st.StepNS[store.StepParse] + a.st.StepNS[store.StepInsert]
	p.cfg.Store.AddSteps(a.st)
	a.st = store.StepStats{}
	return own
}

// ExecL runs q like Exec and attributes it to an insert table: the statement is
// counted, and timed when the line being inserted is one of the sampled ones.
func (p *pstx) ExecL(table int, q string, args ...any) (sql.Result, error) {
	a := p.acc
	if a == nil || !a.inInsert {
		return p.exec(q, args...)
	}
	a.st.TableRows[table]++
	if !a.sample {
		return p.exec(q, args...)
	}
	t0 := a.now()
	res, err := p.exec(q, args...)
	a.st.TableNS[table] += int64(a.now() - t0)
	return res, err
}

// SlowFile is one of the slowest files of a rebuild. Hash is a short hash of
// the file's relative path and size, never the path text.
type SlowFile struct {
	Hash  string
	Sec   float64
	Bytes int64
}

// slowest keeps the slowest few files by total step time.
type slowest struct {
	mu    sync.Mutex
	files []SlowFile
}

func (s *slowest) note(rel string, bytes int64, ns int64) {
	sec := float64(ns) / 1e9
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.files) >= slowestKept && sec <= s.files[len(s.files)-1].Sec {
		return
	}
	sum := sha256.Sum256([]byte(rel + "\x00" + strconv.FormatInt(bytes, 10)))
	s.files = append(s.files, SlowFile{Hash: hex.EncodeToString(sum[:6]), Sec: sec, Bytes: bytes})
	sort.SliceStable(s.files, func(i, j int) bool { return s.files[i].Sec > s.files[j].Sec })
	if len(s.files) > slowestKept {
		s.files = s.files[:slowestKept]
	}
}

// SlowestFiles returns the slowest files seen since the last ParseAll began,
// slowest first.
func (p *Parser) SlowestFiles() []SlowFile {
	p.slow.mu.Lock()
	defer p.slow.mu.Unlock()
	return append([]SlowFile(nil), p.slow.files...)
}

// noteFile records one finished generation of a parse call: its histogram
// sample and its place among the slowest files.
func (p *Parser) noteFile(w *genWork) {
	if w.stepNS <= 0 {
		return
	}
	p.cfg.Obs.Reg.Observe(obs.MParseFileDuration, float64(w.stepNS)/1e9)
	p.slow.note(w.rel, w.end0-w.start0, w.stepNS)
}
