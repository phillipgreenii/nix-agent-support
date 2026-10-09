// Package command turns a request into the events it would append. Build is
// pure: it reads the model and the configuration it is given, plans the
// events, validates the whole candidate log by replay and returns the model
// that log describes, so the engine appends the events and adopts that model
// without replaying again. A refusal is a *Rejection whose reason comes from
// one closed set, the same code whether the command finds the condition in the
// present state or the candidate replay finds it.
package command

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// Command is one request. ReqHash and IsDryRun let the engine run the
// idempotency lookup and the dry-run exemption before Build. ReqHash covers
// the command's name and the fields the client supplied, never its id and
// never a defaulted value such as an omitted effective_at, so a retry made
// later hashes the same.
type Command interface {
	Name() string
	ClientID() event.ID
	ReqHash() (string, error)
	IsDryRun() bool
}

// Plan is what a command would do. Events are the events to append, in order,
// as the codec reads them back, with Line 0 until appended; BatchID is set
// when they form a batch. NoOp means there is nothing to append and the
// version does not change, and Note is the one plain sentence saying why.
// Candidate is the model of the log with Events appended, which the engine
// adopts once they are stored; it is nil for a no-op.
type Plan struct {
	Events    []event.Event
	BatchID   event.ID
	NoOp      bool
	Note      string
	Preview   *Preview
	Candidate *projection.Model
}

// Preview is what a dry run reports. The task and cycle commands have no dry
// run; the period and profile changes give it its fields.
type Preview struct{}

// Version is the state version: the number of lines in the log and the
// configuration generation.
type Version struct {
	LogLines         int
	ConfigGeneration int64
}

// Env is what Build reads: the present model, the configuration, the clock
// reading the events are recorded at, the state version, and the source of
// fresh event and cycle ids. Model, Config, Now and NewID MUST be set.
type Env struct {
	Model   *projection.Model
	Config  *config.Config
	Now     time.Time
	Version Version
	NewID   func() event.ID
}

// planner is a command this package knows how to plan.
type planner interface {
	Command
	plan(b *builder) (Plan, error)
}

// Build plans command c against env. It is pure: no IO, and neither env's
// model nor its configuration is changed. The request is judged in this order,
// and the first answer stops it: (1) its own validity, invalid_request
// (including an event too long or not valid UTF-8 once encoded) and then
// future_effective_at; (2) its targets, an unknown id before any code that
// depends on state; (3) for a task, a resolution the present state already
// refuses; (4) a pause, resume, stop or switch that finds its cycle already in
// the requested state at the request's instant, which is a no-op; (5) a clock
// that reads earlier than the newest event of the entity acted on, when no
// effective_at was given; (6) the candidate replay of the log with the new
// events, whose finding becomes a Rejection with the same code.
func Build(env Env, c Command) (Plan, error) {
	switch {
	case c == nil:
		return Plan{}, errors.New("command: no command to build")
	case env.Model == nil, env.Config == nil, env.NewID == nil, env.Now.IsZero():
		return Plan{}, errors.New("command: Env needs a Model, a Config, a Now and a NewID")
	}
	p, ok := c.(planner)
	if !ok {
		return Plan{}, fmt.Errorf("command: %T is not a command this package builds", c)
	}
	b := &builder{env: env, cmd: c, at: event.At(env.Now).Time()}
	if id := c.ClientID(); id != "" {
		if _, err := event.ParseID(string(id)); err != nil {
			return Plan{}, b.invalid("The request id %q is not a ULID (%v).", id, err)
		}
		h, err := c.ReqHash()
		if err != nil {
			return Plan{}, b.invalid("The request cannot be hashed: %v.", err)
		}
		b.hash = h
	}
	return p.plan(b)
}

// placeholderID stands in for an id that is not drawn yet when the size of a
// planned event is checked: every id is a ULID of this length.
const placeholderID = event.ID("00000000000000000000000000")

// builder is the state of one Build.
type builder struct {
	env  Env
	cmd  Command
	at   time.Time // the instant the events are recorded, to the millisecond
	hash string    // the req_hash of every event, when the request carries an id
}

// instant writes t as every message does.
func (b *builder) instant(t time.Time) string { return b.env.Model.FormatInstant(t) }

// invalid is an invalid_request with a sentence.
func (b *builder) invalid(format string, args ...any) *Rejection {
	return &Rejection{Reason: ReasonInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

// badText refuses text, or an event, that cannot be stored: not valid UTF-8,
// over the size limit, or otherwise refused by the codec.
func (b *builder) badText(err error) *Rejection {
	switch {
	case errors.Is(err, event.ErrInvalidUTF8):
		return b.invalid("The request holds text that is not valid UTF-8, which is refused rather than rewritten.")
	case errors.Is(err, event.ErrTooLarge):
		return b.invalid("The new event would be longer than %d bytes once encoded; shorten its text.", event.MaxEventBytes)
	}
	return b.invalid("The new event would not be a valid event: %v.", err)
}

// validText checks the text a client supplied, before anything is built.
func (b *builder) validText(strs ...string) error {
	if err := event.ValidText(strs...); err != nil {
		return b.badText(err)
	}
	return nil
}

// effective is the request's effective instant: the supplied one, else the
// recording instant, to the millisecond either way.
func (b *builder) effective(supplied *time.Time) time.Time {
	if supplied == nil {
		return b.at
	}
	return event.At(*supplied).Time()
}

// newEvent is an event of the request with the given id.
func (b *builder) newEvent(id event.ID, eff time.Time, p event.Payload) event.Event {
	return event.Event{
		Envelope: event.Envelope{
			V: event.SchemaVersion, ID: id, At: event.At(b.at), EffectiveAt: event.At(eff),
			ReqHash: b.hash, Type: p.EventType(),
		},
		Payload: p,
	}
}

// lone is the one event of a request: its id is the client's when it has one.
func (b *builder) lone(eff time.Time, p event.Payload) event.Event {
	id := b.cmd.ClientID()
	if id == "" {
		id = b.env.NewID()
	}
	return b.newEvent(id, eff, p)
}

// encodable checks, before the targets are looked up, that each event the
// request would add encodes: ids not drawn yet are placeholders of the same
// length, so an event too long once encoded is a malformed request and never
// reaches the store.
func (b *builder) encodable(eff time.Time, payloads ...event.Payload) error {
	id := b.cmd.ClientID()
	if id == "" {
		id = placeholderID
	}
	for _, p := range payloads {
		if _, err := event.Encode(b.newEvent(id, eff, p)); err != nil {
			return b.badText(err)
		}
	}
	return nil
}

// notFuture refuses a supplied effective_at later than the clock plus the
// configured skew. An instant exactly at the skew is accepted.
func (b *builder) notFuture(eff time.Time, supplied *time.Time) error {
	if supplied == nil {
		return nil
	}
	skew := b.env.Config.Defaults().MaxFutureSkewSeconds
	limit := b.env.Now.Add(time.Duration(skew) * time.Second)
	if !eff.After(limit) {
		return nil
	}
	return &Rejection{
		Reason:   ReasonFutureEffectiveAt,
		Instants: []time.Time{eff, b.at},
		Message: fmt.Sprintf(
			"The new event's effective_at %s is later than the clock, %s, plus the %d seconds of max_future_skew_seconds, and a change cannot take effect in the future.",
			b.instant(eff), b.instant(b.at), skew,
		),
	}
}

// clockBehind refuses a request with no effective_at whose clock reads earlier
// than the newest live event of an entity it acts on. Replay alone would
// accept some of these (a pause stamped before a boost is a valid timeline),
// but the operator meant the present, so the request must say when instead.
func (b *builder) clockBehind(supplied *time.Time, eff time.Time, kind string, ids ...string) error {
	if supplied != nil {
		return nil
	}
	for _, id := range ids {
		newest, ok := b.env.Model.NewestEvent(id)
		if !ok || !eff.Before(newest.EffectiveAt.Time()) {
			continue
		}
		return &Rejection{
			Reason:   ReasonClockBehindLog,
			Entity:   id,
			Events:   []event.ID{newest.ID},
			Instants: []time.Time{eff, newest.EffectiveAt.Time()},
			Message: fmt.Sprintf(
				"The clock reads %s, earlier than %s, when event %s of %s %s takes effect, so the new event stamped with the clock would sort before it; pass an effective_at to say when the change happened.",
				b.instant(eff), b.instant(newest.EffectiveAt.Time()), newest.ID, kind, id,
			),
		}
	}
	return nil
}

// finish runs step 6: each planned event is encoded and read back, so the
// plan holds exactly what the store will write, and the log with the events
// appended is replayed. A finding of the replay becomes a Rejection with the
// same code and facts.
func (b *builder) finish(events []event.Event, batch event.ID) (Plan, error) {
	out := make([]event.Event, len(events))
	for i, e := range events {
		line, err := event.Encode(e)
		if err != nil {
			return Plan{}, b.badText(err)
		}
		if out[i], err = event.Decode(line); err != nil {
			return Plan{}, fmt.Errorf("command: a planned %s does not read back: %w", e.Payload.EventType(), err)
		}
	}
	m, err := projection.Candidate(b.env.Model.Log(), out)
	if err != nil {
		var inv *projection.Invalid
		if errors.As(err, &inv) {
			return Plan{}, b.fromInvalid(inv)
		}
		return Plan{}, err
	}
	return Plan{Events: out, BatchID: batch, Candidate: m}, nil
}

// fromInvalid is the Rejection of a replay finding: the same code, entity,
// stored events, cycles and instants, each cycle with its title.
func (b *builder) fromInvalid(inv *projection.Invalid) *Rejection {
	r := &Rejection{
		Reason:   Reason(inv.Code),
		Message:  inv.Message,
		Entity:   inv.Entity,
		Events:   slices.Clone(inv.Events),
		Instants: slices.Clone(inv.Instants),
	}
	for _, id := range inv.Cycles {
		r.Cycles = append(r.Cycles, b.cycleRef(id))
	}
	return r
}

// cycleRef names a stored cycle with its title, status and start instant. The
// title comes from CycleTitle, which also knows a cycle whose start is
// retracted now.
func (b *builder) cycleRef(id event.CycleID) CycleRef {
	ref := CycleRef{ID: id}
	ref.Title, _ = b.env.Model.CycleTitle(id)
	if c, ok := b.env.Model.Cycle(id); ok {
		ref.Status = c.Status
		if len(c.Segments) > 0 {
			ref.StartedAt = c.Segments[0].Start
		}
	}
	return ref
}

// liveEvents lists the live events, as corrected, of the given types for
// which keep is true, in the entity order: by effective_at, then log
// position.
func (b *builder) liveEvents(keep func(event.Payload) bool, types ...event.Type) []event.Event {
	var out []event.Event
	for _, v := range b.env.Model.Events(projection.EventQuery{Types: types}) {
		if !v.Retracted && keep(v.Corrected.Payload) {
			out = append(out, v.Corrected)
		}
	}
	slices.SortStableFunc(out, func(x, y event.Event) int { return x.EffectiveAt.Time().Compare(y.EffectiveAt.Time()) })
	return out
}

// instantsInOrder lists distinct instants in time order, the way a finding
// lists the instants of events it sorts.
func instantsInOrder(ts ...time.Time) []time.Time {
	out := slices.Clone(ts)
	slices.SortStableFunc(out, time.Time.Compare)
	return slices.CompactFunc(out, time.Time.Equal)
}

// list joins names as "a", "a and b" or "a, b and c".
func list(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
