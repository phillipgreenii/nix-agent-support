package store

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// The steps partition the time inside ParseAll (see obs.Steps for the names).
const (
	StepRead = iota
	StepParse
	StepInsert
	StepSavepoint
	StepCommit
	StepOther
	NumSteps
)

// The tables split the insert step (see obs.InsertTables for the names).
const (
	TableRecords = iota
	TableFields
	TableTexts
	TableFTS
	TableSessions
	TableExt
	TableOther
	NumTables
)

// StepStats is a set of step measurements: seconds and calls per step, the
// estimated seconds and the statements per insert table, and the work done
// (bytes read, lines parsed, records inserted). Durations are nanoseconds.
type StepStats struct {
	StepNS    [NumSteps]int64
	StepCalls [NumSteps]int64
	TableNS   [NumTables]int64
	TableRows [NumTables]int64
	BytesRead int64
	Lines     int64
	Records   int64
}

// Add returns a + b.
func (a StepStats) Add(b StepStats) StepStats {
	for i := range a.StepNS {
		a.StepNS[i] += b.StepNS[i]
		a.StepCalls[i] += b.StepCalls[i]
	}
	for i := range a.TableNS {
		a.TableNS[i] += b.TableNS[i]
		a.TableRows[i] += b.TableRows[i]
	}
	a.BytesRead += b.BytesRead
	a.Lines += b.Lines
	a.Records += b.Records
	return a
}

// Sub returns a - b.
func (a StepStats) Sub(b StepStats) StepStats {
	for i := range a.StepNS {
		a.StepNS[i] -= b.StepNS[i]
		a.StepCalls[i] -= b.StepCalls[i]
	}
	for i := range a.TableNS {
		a.TableNS[i] -= b.TableNS[i]
		a.TableRows[i] -= b.TableRows[i]
	}
	a.BytesRead -= b.BytesRead
	a.Lines -= b.Lines
	a.Records -= b.Records
	return a
}

// StepSec is the seconds of step i.
func (a StepStats) StepSec(i int) float64 { return float64(a.StepNS[i]) / 1e9 }

// TableSec is the estimated seconds of insert table i.
func (a StepStats) TableSec(i int) float64 { return float64(a.TableNS[i]) / 1e9 }

// MarshalJSON names the steps and tables, so the stored profile reads as
// {"step_sec":{"read":..},"insert_table_sec":{..},..}.
func (a StepStats) MarshalJSON() ([]byte, error) {
	steps := map[string]float64{}
	calls := map[string]int64{}
	for i, n := range obs.Steps {
		steps[n], calls[n] = a.StepSec(i), a.StepCalls[i]
	}
	tbl := map[string]float64{}
	rows := map[string]int64{}
	for i, n := range obs.InsertTables {
		tbl[n], rows[n] = a.TableSec(i), a.TableRows[i]
	}
	return json.Marshal(struct {
		StepSec   map[string]float64 `json:"step_sec"`
		StepCalls map[string]int64   `json:"step_calls"`
		TableSec  map[string]float64 `json:"insert_table_sec"`
		TableRows map[string]int64   `json:"insert_table_statements"`
		BytesRead int64              `json:"bytes_read"`
		Lines     int64              `json:"lines"`
		Records   int64              `json:"records_inserted"`
	}{steps, calls, tbl, rows, a.BytesRead, a.Lines, a.Records})
}

// stepEpoch anchors the default step clock; time.Since reads the monotonic
// clock, so the readings never go backwards.
var stepEpoch = time.Now()

func monotonicNow() time.Duration { return time.Since(stepEpoch) }

// stepState is the store's accumulated step statistics.
type stepState struct {
	mu  sync.Mutex
	tot StepStats
}

// StepNow returns the clock the step timers read: a monotonic duration. A test
// replaces it with Options.StepClock.
func (s *Store) StepNow() func() time.Duration { return s.stepClock }

// StepTimers reports whether the store times its own steps (false only in an
// ablation build that turns them off).
func (s *Store) StepTimers() bool { return !ablateNoStepTimers }

// AddSteps adds a delta to the totals and to the registry counters. It is safe
// to call from inside a write (it takes only the step lock).
func (s *Store) AddSteps(d StepStats) {
	s.steps.mu.Lock()
	s.steps.tot = s.steps.tot.Add(d)
	s.steps.mu.Unlock()
	reg := s.obs.Reg
	for i, n := range obs.Steps {
		if d.StepNS[i] != 0 {
			reg.Add(obs.MStepSeconds, map[string]string{"step": n}, float64(d.StepNS[i])/1e9)
		}
		if d.StepCalls[i] != 0 {
			reg.Add(obs.MStepCalls, map[string]string{"step": n}, float64(d.StepCalls[i]))
		}
	}
	for i, n := range obs.InsertTables {
		if d.TableNS[i] != 0 {
			reg.Add(obs.MInsertTableSeconds, map[string]string{"table": n}, float64(d.TableNS[i])/1e9)
		}
	}
}

// Steps returns the step statistics accumulated so far.
func (s *Store) Steps() StepStats {
	s.steps.mu.Lock()
	defer s.steps.mu.Unlock()
	return s.steps.tot
}

func (s *Store) addStep(step int, ns int64) {
	var d StepStats
	d.StepNS[step], d.StepCalls[step] = ns, 1
	s.AddSteps(d)
}
