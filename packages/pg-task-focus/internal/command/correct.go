package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// Correct replaces fields of the stored event Target, effective now: its
// effective_at and any data field that is not an identity field, each value
// whole. The original stays in the log, and the correction can be retracted
// like any event. A cycle.started's type may be corrected: the new type MUST
// be a configured cycle type, and the system fills the title from the
// configuration as it is now, so the client never supplies a title for a
// cycle.started and replay never reads the configuration. A correction never
// changes the envelope type or an identity field and never targets an
// event.corrected, an event.retracted or a batch.committed (invalid_correction);
// a replacement value that would break the event is a malformed request
// (invalid_request), with the path of the field. Reason, optional, says why.
type Correct struct {
	ID     event.ID
	Target event.ID
	Fields map[string]json.RawMessage
	Reason string
}

// Name implements Command.
func (Correct) Name() string { return "events/correct" }

// ClientID implements Command.
func (c Correct) ClientID() event.ID { return c.ID }

// IsDryRun implements Command: a correction has no dry run.
func (Correct) IsDryRun() bool { return false }

// ReqHash implements Command. The fields are hashed as the client sent them,
// canonically, so a title the system fills never enters it.
func (c Correct) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		Target event.ID                   `json:"target"`
		Fields map[string]json.RawMessage `json:"fields"`
		Reason string                     `json:"reason,omitempty"`
	}{c.Target, c.Fields, c.Reason})
}

// The data fields the Go checks of a command bound by name, whatever event
// they replace.
const (
	fieldEffectiveAt    = "effective_at"
	fieldReason         = "reason"
	fieldMinutes        = "minutes"
	fieldPlannedMinutes = "planned_minutes"
	fieldType           = "type"
	fieldTitle          = "title"
)

// fieldPath is where a correction field lands in the target event:
// effective_at in the envelope, every other key in its data.
func fieldPath(k string) string {
	if k == fieldEffectiveAt {
		return k
	}
	return "data." + k
}

// plan judges a correction in this order: (1) the request's own validity
// (invalid_request): a target, at least one field, text that can be stored,
// and the Go checks a command applies to the same values (a reason that is not
// blank, minutes from 1 to 525600, an instant), then future_effective_at for a
// replacement effective_at; (2) the target (unknown_event) and, for a
// cycle.started, the client's title (invalid_request) and the new type
// (unknown_cycle_type); (3) the correction rules (invalid_correction), and the
// target's schema with the replacement values (invalid_request); (4) the
// candidate replay of the log with the correction.
func (c Correct) plan(b *builder) (Plan, error) {
	fields, err := c.validate(b)
	if err != nil {
		return Plan{}, err
	}
	if raw, ok := fields[fieldEffectiveAt]; ok {
		var at event.Instant
		if err := json.Unmarshal(raw, &at); err == nil {
			if err := b.notFutureAs(at.Time(), "The replacement effective_at"); err != nil {
				return Plan{}, err
			}
		}
	}
	target, ok := b.logged(c.Target)
	if !ok {
		return Plan{}, b.unknownEvent(c.Target, "correct")
	}
	if _, ok := target.Payload.(event.CycleStarted); ok {
		if err := b.cycleType(fields); err != nil {
			return Plan{}, err
		}
	}
	if pr := projection.CheckCorrection(target, fields); pr != nil {
		return Plan{}, b.correctionProblem(target, fields, pr)
	}
	return b.finish([]event.Event{b.lone(b.at, event.EventCorrected{Target: c.Target, Fields: fields, Reason: c.Reason})}, "")
}

// validate is step 1 of a correction: it returns the fields as they will be
// stored, a replacement reason trimmed.
func (c Correct) validate(b *builder) (map[string]json.RawMessage, error) {
	if c.Target == "" {
		return nil, b.invalid("A correction needs target, the id of the event it corrects.")
	}
	if len(c.Fields) == 0 {
		return nil, b.invalid("A correction needs fields, the replacement values, and it has none.")
	}
	keys := slices.Sorted(maps.Keys(c.Fields))
	if err := b.validText(append([]string{string(c.Target), c.Reason}, keys...)...); err != nil {
		return nil, err
	}
	fields := maps.Clone(c.Fields)
	for _, k := range keys {
		raw, err := b.fieldValue(k, c.Fields[k])
		if err != nil {
			return nil, err
		}
		fields[k] = raw
	}
	// Together the values must fit in one event too. Whether the new event
	// encodes is judged once the correction rules have passed, because an
	// identity or envelope field never encodes and is a broken rule, not a
	// malformed request.
	all := make([]string, 0, len(fields))
	for _, raw := range fields {
		all = append(all, string(raw))
	}
	if err := b.validText(all...); err != nil {
		return nil, err
	}
	return fields, nil
}

// fieldValue checks one replacement value the way a command checks the same
// value, before the target is known, and returns it as it will be stored.
func (b *builder) fieldValue(k string, raw json.RawMessage) (json.RawMessage, error) {
	path := fieldPath(k)
	switch err := event.ValidText(string(raw)); {
	case errors.Is(err, event.ErrInvalidUTF8):
		return nil, b.invalid("The replacement value of %s is not valid UTF-8, which is refused rather than rewritten.", path)
	case errors.Is(err, event.ErrTooLarge):
		return nil, b.invalid("The replacement value of %s is longer than the %d bytes an event may take; shorten it.", path, event.MaxEventBytes)
	}
	if !json.Valid(raw) {
		return nil, b.invalid("The replacement value of %s is not a JSON value.", path)
	}
	switch k {
	case fieldEffectiveAt:
		var at event.Instant
		if err := json.Unmarshal(raw, &at); err != nil {
			return nil, b.invalid("The replacement value of %s is not an instant: %v.", path, err)
		}
	case fieldReason:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, b.invalid("The replacement value of %s is not a string.", path)
		}
		reason, err := event.ValidReason(s)
		if err != nil {
			return nil, b.invalid("The replacement value of %s is blank: a reason that is empty or only white space is refused.", path)
		}
		return jsonText(reason)
	case fieldMinutes, fieldPlannedMinutes:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, b.invalid("The replacement value of %s, %s, is not a whole number of minutes.", path, raw)
		}
		if n < 1 || n > maxMinutes {
			return nil, b.invalid("The replacement value of %s, %d, is outside 1 to %d.", path, n, maxMinutes)
		}
	}
	return raw, nil
}

// cycleType applies the type rule of a cycle.started's correction to fields:
// the client never supplies the title, and a new type MUST be configured, its
// title then filled from the configuration as it is now.
func (b *builder) cycleType(fields map[string]json.RawMessage) error {
	if _, ok := fields[fieldTitle]; ok {
		return b.invalid("A correction of a cycle.started cannot supply %s: the title follows the cycle's type, so correct %s and the configured title is filled in.", fieldPath(fieldTitle), fieldPath(fieldType))
	}
	raw, ok := fields[fieldType]
	if !ok {
		return nil
	}
	var typ string
	if err := json.Unmarshal(raw, &typ); err != nil {
		return b.invalid("The replacement value of %s is not a string.", fieldPath(fieldType))
	}
	def, known := b.env.Config.CycleType(typ)
	if !known {
		return &Rejection{
			Reason: ReasonUnknownCycleType, Instants: []time.Time{b.at},
			Message: fmt.Sprintf("The cycle type %q is not defined in the configuration.", typ),
		}
	}
	title, err := jsonText(def.Title)
	if err != nil {
		return err
	}
	fields[fieldTitle] = title
	return nil
}

// correctionProblem is the refusal of a correction the rules or the target's
// schema forbid. A schema problem names the first field, in key order, that
// breaks the target alone.
func (b *builder) correctionProblem(target event.Event, fields map[string]json.RawMessage, pr *projection.CorrectionProblem) *Rejection {
	r := &Rejection{
		Reason: ReasonInvalidCorrection, Entity: projection.EntityOf(target.Payload),
		Events: []event.ID{target.ID}, Instants: []time.Time{b.at},
	}
	head := "The new event corrects " + describe(target)
	switch {
	case pr.Identity && pr.Field == "":
		r.Message = head + ", and a correction never targets an event.corrected, an event.retracted or a batch.committed."
	case pr.Identity && pr.Field == fieldType:
		r.Message = head + " with type, its envelope type, which a correction never changes; only a cycle.started's data type can be corrected."
	case pr.Identity:
		r.Message = fmt.Sprintf("%s with %s, an identity or envelope field, which a correction never changes; only effective_at and the data fields that are not identity fields can be corrected.", head, pr.Field)
	default:
		r.Reason = ReasonInvalidRequest
		path := "fields"
		for _, k := range slices.Sorted(maps.Keys(fields)) {
			if p := projection.CheckCorrection(target, map[string]json.RawMessage{k: fields[k]}); p != nil {
				path = fieldPath(k)
				break
			}
		}
		r.Message = fmt.Sprintf("%s with a replacement value of %s that the event cannot hold: %v.", head, path, pr.Err)
	}
	return r
}

// unknownEvent is the refusal of a correction or retraction whose target is
// not in the log. A batch id named as an event gets a pointer at target_batch.
func (b *builder) unknownEvent(id event.ID, verb string) *Rejection {
	msg := fmt.Sprintf("No event %s exists in the log, so there is nothing to %s.", id, verb)
	if len(b.env.Model.BatchEvents(id)) > 0 {
		msg = fmt.Sprintf("No event %s exists in the log: it is a batch, which is retracted whole by naming it as target_batch.", id)
	}
	return &Rejection{Reason: ReasonUnknownEvent, Instants: []time.Time{b.at}, Message: msg}
}

// jsonText is s as a JSON string, written as the log writes text: without
// HTML escaping.
func jsonText(s string) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, fmt.Errorf("command: text does not encode as JSON: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
