package main

import (
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The focus run record (spec section 8.2): every non-dry-run select, pull,
// close and select --repair writes ONE focus_run row and prints the same
// record as ONE structured JSON stderr line, pg-desk.focus-select/v1. The row
// is the record; the stderr line is a copy for the interactive session. The
// row is inserted with exit_code NULL when the run starts and finalized LAST
// (exit_code and counts_json together), so a crash leaves exit_code NULL,
// which the run statistics count as outcome "total".
//
// Telemetry: this record is the only thing a focus verb emits. It is a
// SQLite row plus a stderr line; there is no OpenTelemetry span and no
// Prometheus series written from here (the metric families derive from the
// focus_run rows at scrape time and belong to the metrics packet).

// focusSelectContract is the contract member of the stderr line.
const focusSelectContract = "pg-desk.focus-select/v1"

// Verb words of focus_run.verb and the stderr line's `verb` member.
const (
	focusVerbSelect = "select"
	focusVerbPull   = "pull"
	focusVerbClose  = "close"
	focusVerbRepair = "repair"
)

// Annotation outcomes of counts_json.annotations[].outcome.
const (
	focusAnnotationOK     = "ok"
	focusAnnotationFailed = "failed"
)

// focusRemoved counts the plan rows a run removed, by cause.
type focusRemoved struct {
	Operator  int `json:"operator"`
	Cap       int `json:"cap"`
	Dropped   int `json:"dropped"`
	NewPeriod int `json:"new_period"`
}

// focusTierCounts counts the ranked rows by tier.
type focusTierCounts struct {
	Overdue    int `json:"overdue"`
	Started    int `json:"started"`
	NotStarted int `json:"not_started"`
}

// focusRankInputs are the five rank_inputs degradation counters.
type focusRankInputs struct {
	UnparseableDue       int `json:"unparseable_due"`
	UnmappedPriority     int `json:"unmapped_priority"`
	AgeFallback          int `json:"age_fallback"`
	UnblocksUnavailable  int `json:"unblocks_unavailable"`
	StatusCategoryAbsent int `json:"status_category_absent"`
}

// focusAnnotationOutcome is one entity's annotation result: Seq is the
// change_log sequence of the annotation_changed the write produced, 0 when
// the write failed.
type focusAnnotationOutcome struct {
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
	Seq     int64  `json:"seq"`
}

// focusCoverageSummary is the coverage state a run was computed under.
type focusCoverageSummary struct {
	Incomplete bool     `json:"incomplete"`
	Reasons    []string `json:"reasons"`
}

// focusKept counts plan rows kept although the bead is claimed or deferred.
type focusKept struct {
	Claimed  int `json:"claimed"`
	Deferred int `json:"deferred"`
}

// focusCounts is the shape of focus_run.counts_json (and of the counting
// members of the stderr line). Absent counts are written as zero or empty,
// never omitted: marshal through focusCounts.JSON, which normalizes nil
// slices to empty arrays. Key lists hold canonical entity keys.
type focusCounts struct {
	Cap             int                      `json:"cap"`
	Kept            focusKept                `json:"kept"`
	Ranked          int                      `json:"ranked"`
	Selected        int                      `json:"selected"`
	Removed         focusRemoved             `json:"removed"`
	Forced          []string                 `json:"forced"`
	Handadded       []string                 `json:"handadded"`
	Absorbed        []string                 `json:"absorbed"`
	Hydrated        []string                 `json:"hydrated"`
	Tiers           focusTierCounts          `json:"tiers"`
	RankInputs      focusRankInputs          `json:"rank_inputs"`
	Annotations     []focusAnnotationOutcome `json:"annotations"`
	DraftAgeSeconds int64                    `json:"draft_age_seconds"`
	DriftRows       int                      `json:"drift_rows"`
	FreshOrder      bool                     `json:"fresh_order"`
	Coverage        focusCoverageSummary     `json:"coverage"`
}

// normalized returns a copy with every nil slice replaced by an empty one.
func (c focusCounts) normalized() focusCounts {
	if c.Forced == nil {
		c.Forced = []string{}
	}
	if c.Handadded == nil {
		c.Handadded = []string{}
	}
	if c.Absorbed == nil {
		c.Absorbed = []string{}
	}
	if c.Hydrated == nil {
		c.Hydrated = []string{}
	}
	if c.Annotations == nil {
		c.Annotations = []focusAnnotationOutcome{}
	}
	if c.Coverage.Reasons == nil {
		c.Coverage.Reasons = []string{}
	}
	return c
}

// JSON is the counts_json document.
func (c focusCounts) JSON() (string, error) {
	b, err := json.Marshal(c.normalized())
	if err != nil {
		return "", fmt.Errorf("focus: marshal counts: %w", err)
	}
	return string(b), nil
}

// focusRun is one verb run's record. Build it with newFocusRun, call Start
// once the store is writable, fill it with SetCounts and Annotation as the
// verb works, and call Finish(exitCode) LAST: it finalizes the row and then
// prints the stderr line. A dry run never writes a row and still prints the
// line (with dry_run true).
type focusRun struct {
	w      io.Writer
	st     *store.Store
	verb   string
	runID  string
	period focusPeriod
	actor  string
	dryRun bool
	now    time.Time

	counts      focusCounts
	annotations []focusAnnotationOutcome
	rowInserted bool
	finished    bool
}

// newFocusRun starts a record for a verb run: w receives the stderr line,
// verb is one of the focusVerb* words, now is the injected start time.
func newFocusRun(w io.Writer, verb string, p focusPeriod, actor string, dryRun bool, now time.Time) *focusRun {
	return &focusRun{
		w: w, verb: verb, period: p, actor: actor, dryRun: dryRun, now: now.UTC(),
		runID: focusRunIDs.New(now),
	}
}

// RunID is the ULID the verb prints and --json carries as run_id.
func (r *focusRun) RunID() string { return r.runID }

// Start inserts the run's focus_run row with exit_code NULL, in its own
// transaction. It is a no-op for a dry run. An error means the store is not
// writable: the run goes on without a row (the record is then the stderr line
// only) and Finish will not try to finalize one.
func (r *focusRun) Start(st *store.Store) error {
	r.st = st
	if r.dryRun {
		return nil
	}
	var periodID *int64
	if p, found, err := st.FocusPeriodGet(r.period.Type, r.period.Key); err == nil && found {
		id := p.ID
		periodID = &id
	}
	if _, err := st.FocusRunInsert(store.FocusRun{
		RunID: r.runID, Verb: r.verb, FocusPeriodID: periodID,
		StartedAt: r.now.Format(time.RFC3339), Actor: r.actor, CountsJSON: "{}",
	}); err != nil {
		return err
	}
	r.rowInserted = true
	return nil
}

// SetCounts replaces the run's counts. Annotation outcomes recorded with
// Annotation are kept and appended after any carried in c.
func (r *focusRun) SetCounts(c focusCounts) { r.counts = c }

// Annotation records one entity's annotation outcome: outcome is "ok" or
// "failed" (anything else counts as failed) and seq the change_log sequence
// of the annotation_changed the write produced; a failed write records 0.
func (r *focusRun) Annotation(key, outcome string, seq int64) {
	if outcome != focusAnnotationOK {
		outcome, seq = focusAnnotationFailed, 0
	}
	r.annotations = append(r.annotations, focusAnnotationOutcome{Key: key, Outcome: outcome, Seq: seq})
}

// Counts is the run's counts with the recorded annotation outcomes merged in.
func (r *focusRun) Counts() focusCounts {
	c := r.counts
	c.Annotations = append(append([]focusAnnotationOutcome(nil), c.Annotations...), r.annotations...)
	return c.normalized()
}

// CountsJSON is the counts_json document the row is finalized with.
func (r *focusRun) CountsJSON() (string, error) { return r.Counts().JSON() }

// Line is the pg-desk.focus-select/v1 record for exitCode as ONE JSON line
// (no trailing newline): contract, verb, run_id, period, dry_run, actor,
// exit_code and every counts member at the top level. Members are additive.
func (r *focusRun) Line(exitCode int) ([]byte, error) {
	cj, err := r.CountsJSON()
	if err != nil {
		return nil, err
	}
	obj := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(cj), &obj); err != nil {
		return nil, fmt.Errorf("focus: counts round trip: %w", err)
	}
	for k, v := range map[string]any{
		"contract":  focusSelectContract,
		"verb":      r.verb,
		"run_id":    r.runID,
		"period":    r.period.Key,
		"dry_run":   r.dryRun,
		"actor":     r.actor,
		"exit_code": exitCode,
	} {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("focus: marshal %s: %w", k, err)
		}
		obj[k] = b
	}
	return json.Marshal(obj)
}

// Finish ends the run with exitCode. Order: finalize the row (exit_code and
// counts_json together; skipped for a dry run and when Start wrote no row),
// THEN print the stderr line, so a crash before the finalize leaves
// exit_code NULL. The line is printed even when the finalize fails; the
// finalize error is returned so the caller can report it (the exit code is
// the verb's to decide). A second Finish is a no-op.
func (r *focusRun) Finish(exitCode int) error {
	if r.finished {
		return nil
	}
	r.finished = true
	var ferr error
	if r.rowInserted && !r.dryRun {
		cj, err := r.CountsJSON()
		if err != nil {
			ferr = err
		} else {
			ferr = r.st.FocusRunFinalize(r.runID, exitCode, cj)
		}
	}
	line, err := r.Line(exitCode)
	if err != nil {
		return err
	}
	if r.w != nil {
		if _, werr := fmt.Fprintf(r.w, "%s\n", line); werr != nil && ferr == nil {
			ferr = werr
		}
	}
	return ferr
}

// ---------------------------------------------------------------- run ids

// focusRunIDs generates the run ids of this process: a ULID, monotonic within
// a millisecond so ids from one process sort in creation order.
var focusRunIDs = &ulidGen{}

const crockfordBase32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ulidGen produces ULIDs with the standard library alone (pg-desk has no
// ULID dependency and gomod2nix pins its module set): 48 bits of Unix
// milliseconds followed by 80 random bits, Crockford base32 (26 characters).
// Within one millisecond the random part is incremented, so ids from one
// generator are strictly increasing; time going backwards keeps the last
// timestamp, and a counter that wraps moves to the next millisecond.
type ulidGen struct {
	mu     sync.Mutex
	rand   io.Reader // nil means crypto/rand; tests inject a fixed reader
	lastMS uint64
	last   [10]byte
	used   bool
}

// New returns a new ULID for time t.
func (g *ulidGen) New(t time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := uint64(t.UnixMilli()) & (1<<48 - 1)
	if g.used && ms <= g.lastMS {
		ms = g.lastMS
		if !incrementBigEndian(g.last[:]) {
			// The 80-bit counter wrapped: move to the next millisecond
			// and reseed, so the id still sorts after its predecessors.
			ms++
			g.lastMS = ms
			g.fillRandom()
		}
	} else {
		g.fillRandom()
		g.lastMS = ms
	}
	g.used = true
	var b [16]byte
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	copy(b[6:], g.last[:])
	return crockfordEncode(b)
}

func (g *ulidGen) fillRandom() {
	src := g.rand
	if src == nil {
		src = crand.Reader
	}
	if _, err := io.ReadFull(src, g.last[:]); err != nil {
		// crypto/rand does not fail in practice; fall back to the runtime
		// generator rather than fail a verb over its run id.
		for i := range g.last {
			g.last[i] = byte(mrand.Uint32())
		}
	}
}

// incrementBigEndian adds one to b, reporting false when it wrapped to zero.
func incrementBigEndian(b []byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return true
		}
	}
	return false
}

// crockfordEncode renders 128 bits as 26 Crockford base32 characters (the
// top character carries 3 bits).
func crockfordEncode(b [16]byte) string {
	var out [26]byte
	for i := range out {
		var v byte
		for j := 0; j < 5; j++ {
			pos := i*5 + j - 2 // bit index into the 128-bit big-endian value
			v <<= 1
			if pos >= 0 {
				v |= (b[pos/8] >> (7 - pos%8)) & 1
			}
		}
		out[i] = crockfordBase32[v]
	}
	return string(out[:])
}
