package projection

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// liveEvent is an event that counts: not retracted, with the live corrections
// applied. Corrected is set when a correction changed it, and pos is its
// position in the log (not Line, which an unappended event does not have).
// Only the events that describe the domain are live; corrections, retractions
// and batch markers are consumed by the overlay.
type liveEvent struct {
	event.Event
	Corrected bool
	pos       int
}

// identityKeys are the data fields a correction can never change, because they
// say which entity or batch an event belongs to or what kind of event it is.
// A cycle's own `type` is not among them.
var identityKeys = map[string]bool{
	"cycle_id": true, "task_id": true, "target": true, "target_batch": true,
	"batch": true, "interrupts": true, "definition": true, "kind": true,
	"start": true, "cadence": true, "period": true, "profile": true,
}

// envelopeKeys are the fields outside an event's data. effective_at is the one
// of them a correction may replace; the envelope type is another, and it is
// named like a data field only on an event that has no data field of that name.
var envelopeKeys = map[string]bool{
	"v": true, "id": true, "at": true, "req_hash": true, "data": true,
}

// overlay applies the corrections and retractions of a log, in log order, and
// returns the live events (in log order, corrected) and the view of every
// event except batch.committed. It does not check the timeline; the stages
// after it do. A correction or retraction that the log rules forbid is an
// *Invalid.
//
// Rule: a correction or retraction is live unless a later live retraction
// names it, so undoing an undo restores the original, and the stack is simply
// recomputed from the end of the log backwards. A correction replaces whole
// keys; of several live corrections of one key the last wins.
func overlay(events []event.Event) ([]liveEvent, map[event.ID]EventView, error) {
	return overlayFrom(events, len(events))
}

// overlayFrom is overlay for a log whose events from index firstAdded on are
// the ones a request would add: a finding about one of them calls it "the new
// event" and lists only stored events.
func overlayFrom(events []event.Event, firstAdded int) ([]liveEvent, map[event.ID]EventView, error) {
	r := &overlayRun{
		events:     events,
		firstAdded: firstAdded,
		index:      make(map[event.ID]int, len(events)),
		members:    map[event.ID][]int{},
	}
	for i, e := range events {
		if _, dup := r.index[e.ID]; !dup {
			r.index[e.ID] = i
		}
		if b := e.Payload.BatchID(); b != "" && e.Payload.EventType() != event.TypeBatchCommitted {
			r.members[b] = append(r.members[b], i)
		}
	}
	targets := make([]int, len(events)) // the index each correction or retraction by id names
	for i, e := range events {
		var inv *Invalid
		switch p := e.Payload.(type) {
		case event.EventCorrected:
			targets[i], inv = r.checkCorrection(i, p)
		case event.EventRetracted:
			targets[i], inv = r.checkRetraction(i, p)
		}
		if inv != nil {
			return nil, nil, inv
		}
	}

	// Walking from the end, an event is live unless a later live retraction
	// names it, and every retraction that could name it is later.
	retractedBy := map[event.ID]int{}
	batchRetractions := map[event.ID][]int{}
	corrections := map[int][]int{} // target index -> live corrections, descending
	for i := len(events) - 1; i >= 0; i-- {
		if _, undone := retractedBy[events[i].ID]; undone {
			continue
		}
		switch p := events[i].Payload.(type) {
		case event.EventCorrected:
			corrections[targets[i]] = append(corrections[targets[i]], i)
		case event.EventRetracted:
			if p.TargetBatch != "" {
				batchRetractions[p.TargetBatch] = append(batchRetractions[p.TargetBatch], i)
			} else {
				retractedBy[p.Target] = i // walking backwards, the earliest wins
			}
		}
	}

	var live []liveEvent
	views := make(map[event.ID]EventView, len(events))
	for i, e := range events {
		typ := e.Payload.EventType()
		if typ == event.TypeBatchCommitted {
			continue
		}
		v := EventView{Original: e, Corrected: e}
		if cs := corrections[i]; len(cs) > 0 {
			slices.Reverse(cs)
			merged := map[string]json.RawMessage{}
			for _, c := range cs {
				for k, raw := range events[c].Payload.(event.EventCorrected).Fields {
					merged[k] = raw
				}
				v.CorrectedBy = append(v.CorrectedBy, events[c].ID)
			}
			corrected, err := applyFields(e, merged)
			if err != nil {
				// Each correction was checked alone on the original event, and
				// the merge of their keys differs from none of them, so this is
				// reached only by a payload whose keys depend on one another.
				return nil, nil, r.invalidCorrection(cs[len(cs)-1], i,
					fmt.Sprintf("the live corrections of event %s together make it invalid: %v", e.ID, err))
			}
			v.Corrected = corrected
		}
		if by, ok := r.retractor(i, retractedBy, batchRetractions); ok {
			v.Retracted, v.RetractedBy = true, events[by].ID
		}
		views[e.ID] = v
		if !v.Retracted && typ != event.TypeEventCorrected && typ != event.TypeEventRetracted {
			live = append(live, liveEvent{Event: v.Corrected, Corrected: len(v.CorrectedBy) > 0, pos: i})
		}
	}
	return live, views, nil
}

// overlayRun is the state of one overlay call.
type overlayRun struct {
	events     []event.Event
	firstAdded int
	index      map[event.ID]int   // first position of each event id
	members    map[event.ID][]int // positions of each batch's members, ascending
}

// retractor is the position of the earliest live retraction naming event i, by
// id or through its batch.
func (r *overlayRun) retractor(i int, byID map[event.ID]int, byBatch map[event.ID][]int) (int, bool) {
	best := -1
	if by, ok := byID[r.events[i].ID]; ok {
		best = by
	}
	if b := r.events[i].Payload.BatchID(); b != "" && r.events[i].Payload.EventType() != event.TypeBatchCommitted {
		for _, by := range byBatch[b] {
			if by > i && (best < 0 || by < best) {
				best = by
			}
		}
	}
	return best, best >= 0
}

// checkCorrection applies the log rules to the correction at position i and
// returns the position of its target.
func (r *overlayRun) checkCorrection(i int, p event.EventCorrected) (int, *Invalid) {
	j, inv := r.resolve(i, p.Target, "corrects")
	if inv != nil {
		return -1, inv
	}
	pr := CheckCorrection(r.events[j], p.Fields)
	if pr == nil {
		return j, nil
	}
	head := r.subject(i) + " corrects " + r.describe(j)
	var msg string
	switch {
	case pr.Identity && pr.Field == "":
		msg = head + ", and a correction never targets event.corrected, event.retracted or batch.committed."
	case pr.Identity && identityKeys[pr.Field]:
		msg = fmt.Sprintf("%s with the identity field %s, and an identity field can never be corrected.", head, pr.Field)
	case pr.Identity:
		msg = fmt.Sprintf("%s with %s, a field of its envelope, and only effective_at and the data fields can be corrected.", head, pr.Field)
	default:
		// The command layer refuses a breaking replacement as invalid_request
		// through CheckCorrection before candidate replay gets here; this
		// finding is the stored-log re-check.
		msg = fmt.Sprintf("%s with replacement values that make it invalid: %v.", head, pr.Err)
	}
	return j, r.invalidCorrection(i, j, msg)
}

// CorrectionProblem is why a correction cannot apply to its target. An
// Identity problem is one the log rules forbid whatever the values: the target
// is an event.corrected, event.retracted or batch.committed (Field is empty),
// or Field is an identity field (target_batch among them) or a field of the
// envelope, the envelope type included. Otherwise the replacement values make
// the corrected event fail its schema, and Err says how.
type CorrectionProblem struct {
	Identity bool
	Field    string
	Err      error
}

// CheckCorrection applies the correction rules to replacing fields of target,
// the event as appended: nil when the correction can apply. Identity problems
// are found first, in key order, and a schema problem only when there is none.
// Replay refuses either as invalid_correction; a command can refuse a schema
// problem as a malformed request instead.
func CheckCorrection(target event.Event, fields map[string]json.RawMessage) *CorrectionProblem {
	typ := target.Payload.EventType()
	switch typ {
	case event.TypeEventCorrected, event.TypeEventRetracted, event.TypeBatchCommitted:
		return &CorrectionProblem{Identity: true}
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if identityKeys[k] || envelopeKeys[k] || (k == "type" && typ != event.TypeCycleStarted) {
			return &CorrectionProblem{Identity: true, Field: k}
		}
	}
	if _, err := applyFields(target, fields); err != nil {
		return &CorrectionProblem{Err: err}
	}
	return nil
}

// checkRetraction applies the log rules to the retraction at position i. A
// retraction by batch returns -1 for the target position.
func (r *overlayRun) checkRetraction(i int, p event.EventRetracted) (int, *Invalid) {
	if p.TargetBatch != "" {
		if m := r.members[p.TargetBatch]; len(m) == 0 || m[0] >= i {
			return -1, r.unknown(i, -1, fmt.Sprintf(
				"%s retracts batch %s, which no earlier event is a member of.", r.subject(i), p.TargetBatch,
			))
		}
		return -1, nil
	}
	j, inv := r.resolve(i, p.Target, "retracts")
	if inv != nil {
		return -1, inv
	}
	target := r.events[j]
	typ := target.Payload.EventType()
	switch {
	case typ == event.TypeBatchCommitted:
		return j, r.invalidCorrection(i, j, fmt.Sprintf(
			"%s retracts %s, and a retraction never targets a batch marker.", r.subject(i), r.describe(j),
		))
	case target.Payload.BatchID() != "":
		return j, r.invalidCorrection(i, j, fmt.Sprintf(
			"%s retracts %s, which is a member of batch %s and can be retracted only through its batch.",
			r.subject(i), r.describe(j), target.Payload.BatchID(),
		))
	case typ == event.TypeTaskMaterialized || typ == event.TypePeriodChanged:
		return j, r.invalidCorrection(i, j, fmt.Sprintf(
			"%s retracts %s, and a task.materialized or period.changed is never retracted alone, which would leave a period and its tasks out of step; this one is in no batch, so it cannot be retracted.", r.subject(i), r.describe(j),
		))
	}
	return j, nil
}

// describe names the event at position j: a stored event by id, type and
// entity, the new event never by id.
func (r *overlayRun) describe(j int) string {
	if j >= r.firstAdded {
		return "the new event"
	}
	return Describe(r.events[j])
}

// resolve finds the target of the correction or retraction at position i. A
// target that is not in the log, or that comes at or after position i, is
// unknown_event.
func (r *overlayRun) resolve(i int, target event.ID, verb string) (int, *Invalid) {
	j, ok := r.index[target]
	switch {
	case !ok:
		return -1, r.unknown(i, -1, fmt.Sprintf("%s %s event %s, which is not in the log.", r.subject(i), verb, target))
	case j == i:
		return -1, r.unknown(i, -1, fmt.Sprintf("%s %s itself, and a target must come earlier in the log.", r.subject(i), verb))
	case j > i && j >= r.firstAdded:
		return -1, r.unknown(i, -1, fmt.Sprintf("%s %s a new event that comes later in the log, and a target must come earlier.", r.subject(i), verb))
	case j > i:
		return -1, r.unknown(i, -1, fmt.Sprintf("%s %s event %s, which comes later in the log, and a target must come earlier.", r.subject(i), verb, target))
	}
	return j, nil
}

// subject is the start of a sentence about the event at position i, with the
// instant it takes effect: the new event is never cited by id.
func (r *overlayRun) subject(i int) string {
	at := r.events[i].EffectiveAt.Time().Format(instantLayout)
	if i >= r.firstAdded {
		return fmt.Sprintf("The new event, effective at %s,", at)
	}
	return fmt.Sprintf("Event %s, effective at %s,", r.events[i].ID, at)
}

const instantLayout = "2006-01-02T15:04:05.000Z"

func (r *overlayRun) unknown(i, target int, msg string) *Invalid {
	return r.invalid(codeUnknownEvent, i, target, msg)
}

func (r *overlayRun) invalidCorrection(i, target int, msg string) *Invalid {
	return r.invalid(codeInvalidCorrection, i, target, msg)
}

// invalid builds the finding for the event at position i and its target at
// position target (-1 for none). Only stored events are listed, and the
// entity is the stored target's.
func (r *overlayRun) invalid(code Code, i, target int, msg string) *Invalid {
	inv := &Invalid{
		Code:     code,
		Message:  strings.Join(strings.Fields(msg), " "),
		Instants: []time.Time{r.events[i].EffectiveAt.Time()},
	}
	if i < r.firstAdded {
		inv.Events = append(inv.Events, r.events[i].ID)
	}
	if target >= 0 && target < r.firstAdded {
		inv.Events = append(inv.Events, r.events[target].ID)
		inv.Entity = entityOf(r.events[target].Payload)
	}
	return inv
}

// entityOf is the task or cycle id, or the period kind, an event is about.
func entityOf(p event.Payload) string {
	switch p := p.(type) {
	case event.PeriodChanged:
		return p.Kind
	case event.TaskMaterialized:
		return string(p.TaskID)
	case event.TaskCompleted:
		return string(p.TaskID)
	case event.TaskSkipped:
		return string(p.TaskID)
	case event.TaskMissed:
		return string(p.TaskID)
	case event.TaskWithdrawn:
		return string(p.TaskID)
	case event.TaskReinstated:
		return string(p.TaskID)
	case event.CycleStarted:
		return string(p.CycleID)
	case event.CyclePaused:
		return string(p.CycleID)
	case event.CycleResumed:
		return string(p.CycleID)
	case event.CycleBoosted:
		return string(p.CycleID)
	case event.CycleStopped:
		return string(p.CycleID)
	case event.CycleAnnotated:
		return string(p.CycleID)
	}
	return ""
}

// applyFields returns orig with the replacement fields applied: effective_at
// replaces the envelope's instant and every other key replaces that whole key
// of the data. The result must be an event the codec accepts, so a key the
// event does not have, or a value that breaks its schema, is an error. The
// envelope's id, at and req_hash are kept.
func applyFields(orig event.Event, fields map[string]json.RawMessage) (event.Event, error) {
	raw := orig.Data
	if len(raw) == 0 {
		var err error
		if raw, err = json.Marshal(orig.Payload); err != nil {
			return event.Event{}, fmt.Errorf("data: %w", err)
		}
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return event.Event{}, fmt.Errorf("data: %w", err)
	}
	env := orig.Envelope
	if env.Type == "" {
		env.Type = orig.Payload.EventType()
	}
	for k, v := range fields {
		if k != "effective_at" {
			data[k] = v
			continue
		}
		var at event.Instant
		if err := json.Unmarshal(v, &at); err != nil {
			return event.Event{}, fmt.Errorf("effective_at: %w", err)
		}
		env.EffectiveAt = at
	}
	var err error
	if env.Data, err = json.Marshal(data); err != nil {
		return event.Event{}, fmt.Errorf("data: %w", err)
	}
	line, err := event.Encode(event.Event{Envelope: env})
	if err != nil {
		return event.Event{}, err
	}
	out, err := event.Decode(line)
	if err != nil {
		return event.Event{}, err
	}
	out.Line = orig.Line
	return out, nil
}
