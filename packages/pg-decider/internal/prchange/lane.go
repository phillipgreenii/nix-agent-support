package prchange

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The hydrate results of the run record.
const (
	hydrateOK     = "ok"
	hydrateFailed = "failed"
	hydrateNotRun = "not_run"
)

// The lane-record results of the run record.
const (
	laneRecorded = "recorded"
	laneFailed   = "failed"
	laneSkipped  = "skipped"
)

// laneInput is the JSON document `pg-desk pr lane-record` reads: pg-desk owns
// the key names, the atomic counter increments and the lane-level mode rules,
// so this command never touches the store. The shape is pg-desk's
// lanestate.RunInput (another module: this is a copy, and the unknown-member
// refusal of the verb makes a drift fail loudly).
type laneInput struct {
	PR            string         `json:"pr"`
	Mode          string         `json:"mode"`
	Events        []laneEvent    `json:"events,omitempty"`
	Residual      *int           `json:"residual,omitempty"`
	LastOutcome   string         `json:"last_outcome,omitempty"`
	Decisions     []laneDecision `json:"decisions,omitempty"`
	LookupItems   *int64         `json:"lookup_items,omitempty"`
	LookupCeiling *int64         `json:"lookup_ceiling,omitempty"`
}

type laneEvent struct {
	Stage string `json:"stage"`
	Class string `json:"class"`
}

type laneDecision struct {
	Rule    string `json:"rule"`
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	Count   int    `json:"count,omitempty"`
}

// startRecord starts the run record: who, when and in which mode.
func (r *run) startRecord() {
	r.rec = RunRecord{
		Contract:  RunRecordContract,
		RunID:     r.o.NewRunID(),
		At:        r.o.Clock().UTC().Format("2006-01-02T15:04:05Z"),
		Entity:    r.id,
		EventID:   strings.TrimSpace(r.o.RawID),
		Seq:       r.key.Value,
		SeqSource: r.key.Source,
		Mode:      r.o.Mode,
		Hydrate:   HydrateRecord{Result: hydrateNotRun},
		Lane:      LaneRecord{Result: laneSkipped},
	}
}

// laneInput builds the lane write path's document for the finished run. A run
// that ended at hydrate or decide with a failure carries one event, whose class
// is the failure's; a clean run carries none. The residual plan an apply makes
// afterwards is passed as the residual of the apply run, never as a run of its
// own.
func (r *run) laneInput(code int) laneInput {
	in := laneInput{
		PR: r.id, Mode: r.o.Mode, Residual: r.residual, LastOutcome: "ok",
		LookupItems: r.lookupItems, LookupCeiling: r.lookupCeiling,
	}
	if code != ExitOK && r.failStage != "" {
		in.Events = []laneEvent{{Stage: r.failStage, Class: r.failClass}}
		in.LastOutcome = r.failStage + "." + r.failClass
	}
	for _, d := range r.decisions {
		in.Decisions = append(in.Decisions, laneDecision{Rule: d.Rule, Action: d.Action, Outcome: d.Outcome, Reason: d.Reason, Count: d.Count})
	}
	return in
}

// callLane runs `pg-desk pr lane-record` with the document on standard input.
func (r *run) callLane(in laneInput) (ok bool, detail string) {
	b, err := json.Marshal(in)
	if err != nil {
		return false, err.Error()
	}
	res := r.call(Call{Name: "pg-desk", Args: []string{"pr", "lane-record"}, Stdin: string(b) + "\n"})
	switch {
	case res.StartErr != nil:
		return false, res.StartErr.Error()
	case res.Signaled:
		return false, "pg-desk pr lane-record was ended by a signal"
	case res.Code != 0:
		return false, fmt.Sprintf("exit %d: %s", res.Code, firstLine(res.Stderr+"\n"+res.Stdout))
	}
	return true, ""
}

// recordLane persists the run through pg-desk's lane write path: the PR's mode,
// residual and last outcome, the lane-run and decisions counters, the last-run
// time and (when the decider reported them) the lookup figures. A run that
// ended at the lock is not recorded there (it holds no lock to write the PR's
// own state under). A refusal that names a decision (a rule or reason outside
// pg-desk's closed lists, for example after the decider gained a rule) is
// retried once without the decisions, so the run's counters and state still
// land. A failure here never changes the exit code: the decider has already
// acted, and re-running it would only repeat that work.
func (r *run) recordLane(code int) LaneRecord {
	if r.failStage == StageLock {
		return LaneRecord{Result: laneSkipped}
	}
	in := r.laneInput(code)
	ok, detail := r.callLane(in)
	if ok {
		return LaneRecord{Result: laneRecorded}
	}
	if len(in.Decisions) > 0 && strings.Contains(detail, "decision") {
		in.Decisions = nil
		if ok2, detail2 := r.callLane(in); ok2 {
			r.note("lane state recorded without the decisions (pg-desk refused them: " + detail + ")")
			return LaneRecord{Result: laneRecorded, DroppedDecisions: true, Error: detail}
		} else {
			detail = detail2
		}
	}
	r.note("lane state not recorded (" + detail + "): run pg-desk pr lane-record --help")
	return LaneRecord{Result: laneFailed, Error: detail}
}

// note writes one diagnostic line to standard error. It is scrubbed of the
// router's retry words, so a note that follows a non-retryable failure line can
// never make the router re-run the delivery.
func (r *run) note(msg string) {
	fmt.Fprintf(r.o.Stderr, "pg-pr-change: note: %s\n", oneLine(scrub(msg), 400))
}

// finish completes the run record and writes it: the lane write path first (so
// the record says whether it worked), then the append to the run record file.
func (r *run) finish(code int) {
	r.rec.ExitCode = code
	r.rec.ErrorClass, r.rec.FailingStage = r.failClass, r.failStage
	if r.failStage == StageHydrate {
		r.rec.Hydrate.Class = r.failClass
	}
	r.rec.Residual = r.residual
	r.rec.Lane = r.recordLane(code)
	r.rec.FinishedAt = r.o.Clock().UTC().Format("2006-01-02T15:04:05Z")
	if r.o.RunRecordPath == "" {
		return
	}
	if err := AppendRunRecord(r.o.RunRecordPath, r.rec); err != nil {
		r.note("run record not written (" + err.Error() + "): check " + r.o.RunRecordPath)
	}
}
