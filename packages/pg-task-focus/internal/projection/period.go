package projection

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// Kind is the length of a period. Each kind has its own current period and
// changes independently of the others.
type Kind string

// The kinds of period.
const (
	Day    Kind = "day"
	Week   Kind = "week"
	Sprint Kind = "sprint"
)

// The codes the periods and the profile can find.
const (
	codePeriodUnchanged  Code = "period_unchanged"
	codePeriodOutOfOrder Code = "period_out_of_order"
)

// Period is the current period of one kind. Start and End are civil dates of
// the zone TZ, which decides which date "today" is; End is inclusive and equals
// Start for a day. OpenedBy is the period.changed event that began it.
type Period struct {
	Kind     Kind
	Start    civil.Date
	End      civil.Date
	Label    string
	TZ       zone.Zone
	OpenedBy event.ID
}

// Period returns the current period of kind k: the latest live start.
func (m *Model) Period(k Kind) (Period, bool) {
	p, ok := m.periods[k]
	return p, ok
}

// Profile returns the active profile, the latest live profile.changed, or ""
// while the log has none.
func (m *Model) Profile() string { return m.profile }

// run is one projection of the live events onto periods, profile and tasks.
type run struct {
	log        []event.Event
	firstAdded int
	zone       *zone.Zone   // the active day period's zone; nil while no day period exists
	cause      *event.Event // the first added correction or retraction, which no live event shows
}

func newRun(log []event.Event, firstAdded int) *run {
	r := &run{log: log, firstAdded: firstAdded}
	for i := firstAdded; i < len(log); i++ {
		switch log[i].Payload.EventType() {
		case event.TypeEventCorrected, event.TypeEventRetracted:
			r.cause = &log[i]
			return r
		}
	}
	return r
}

// order is the order of an entity's events: by effective_at, then log position.
func order(a, b liveEvent) int {
	if c := a.EffectiveAt.Time().Compare(b.EffectiveAt.Time()); c != 0 {
		return c
	}
	return cmp.Compare(a.pos, b.pos)
}

func sorted(evs []liveEvent) []liveEvent {
	out := slices.Clone(evs)
	slices.SortStableFunc(out, order)
	return out
}

func (r *run) isNew(e liveEvent) bool { return e.pos >= r.firstAdded }

// instant is t in UTC and, once a day period exists, the same instant in that
// period's zone with its identifier, so no zone is implicit. The local date is
// given only when it differs from the UTC date.
func (r *run) instant(t time.Time) string {
	utc := t.UTC()
	s := utc.Format(instantLayout)
	if r.zone == nil {
		return s
	}
	local := t.In(r.zone.Location())
	layout := "15:04"
	if y, mo, d := local.Date(); y != utc.Year() || mo != utc.Month() || d != utc.Day() {
		layout = "2006-01-02 15:04"
	}
	return fmt.Sprintf("%s (%s %s)", s, local.Format(layout), r.zone.Name())
}

// ref names an event inside a sentence, with the instant it takes effect: a
// stored event by id, the event a request would add never by id.
func (r *run) ref(e liveEvent) string {
	if r.isNew(e) {
		return "the new event effective at " + r.instant(e.EffectiveAt.Time())
	}
	return fmt.Sprintf("event %s effective at %s", e.ID, r.instant(e.EffectiveAt.Time()))
}

// finding builds the Invalid for a sentence (without its final full stop)
// about the involved events, in the order they should be listed. Only stored
// events are listed, and every involved instant is carried. When none of the
// involved events is new but the request added a correction or a retraction,
// that event is the cause and the sentence says so.
func (r *run) finding(code Code, entity, sentence string, involved ...liveEvent) *Invalid {
	inv := &Invalid{Code: code, Entity: entity}
	newInvolved := false
	for _, e := range involved {
		if r.isNew(e) {
			newInvolved = true
		} else if !slices.Contains(inv.Events, e.ID) {
			inv.Events = append(inv.Events, e.ID)
		}
		if t := e.EffectiveAt.Time(); !slices.ContainsFunc(inv.Instants, t.Equal) {
			inv.Instants = append(inv.Instants, t)
		}
	}
	if !newInvolved && r.cause != nil {
		t := r.cause.EffectiveAt.Time()
		sentence += " once the new event effective at " + r.instant(t) + " is applied"
		if !slices.ContainsFunc(inv.Instants, t.Equal) {
			inv.Instants = append(inv.Instants, t)
		}
	}
	inv.Message = strings.Join(strings.Fields(sentence+"."), " ")
	return inv
}

// periodStart is the start a period.changed event sets.
func periodStart(e liveEvent) civil.Date { return e.Payload.(event.PeriodChanged).Start }

// projectPeriods groups the live period.changed events by kind, finds the
// zone messages are given in, checks the order of each kind and returns the
// current period of every kind that has one. The checks look at the events
// alone, so a stored log and a candidate agree: the changes of a kind, in
// event order, MUST have rising starts, and where a neighbouring pair does not,
// the change that stands later in the log is the offender. One that sorts after the change it
// fails to follow does not start a later period (period_unchanged); one that
// sorts before a change with a lower start is out of order although its own
// start is later (period_out_of_order). An equal start is never later, so it is
// period_unchanged whichever sorts first.
func (r *run) projectPeriods(live []liveEvent) (map[Kind]Period, []liveEvent, error) {
	byKind := map[Kind][]liveEvent{}
	var profiles []liveEvent
	for _, e := range live {
		switch p := e.Payload.(type) {
		case event.PeriodChanged:
			byKind[Kind(p.Kind)] = append(byKind[Kind(p.Kind)], e)
		case event.ProfileChanged:
			profiles = append(profiles, e)
		}
	}
	for k := range byKind {
		byKind[k] = sorted(byKind[k])
	}
	r.zone = r.activeZone(byKind[Day])

	for _, k := range []Kind{Day, Week, Sprint} {
		changes := byKind[k]
		for i := 1; i < len(changes); i++ {
			if inv := r.checkPair(k, changes[i-1], changes[i]); inv != nil {
				return nil, nil, inv
			}
		}
	}

	periods := map[Kind]Period{}
	for k, changes := range byKind {
		last := changes[len(changes)-1]
		p := last.Payload.(event.PeriodChanged)
		z, err := zone.Load(p.TZ)
		if err != nil {
			return nil, nil, fmt.Errorf("event %s: %w", last.ID, err) // the codec refuses such an event
		}
		end := p.Start
		if p.End != nil {
			end = *p.End
		}
		periods[k] = Period{Kind: k, Start: p.Start, End: end, Label: p.Label, TZ: z, OpenedBy: last.ID}
	}
	return periods, sorted(profiles), nil
}

// activeZone is the zone of the day period with the latest start.
func (r *run) activeZone(days []liveEvent) *zone.Zone {
	var best *liveEvent
	for i := range days {
		if best == nil || periodStart(days[i]).Compare(periodStart(*best)) >= 0 {
			best = &days[i]
		}
	}
	if best == nil {
		return nil
	}
	z, err := zone.Load(best.Payload.(event.PeriodChanged).TZ)
	if err != nil {
		return nil
	}
	return &z
}

// checkPair judges two neighbours of one kind, a sorting before b.
func (r *run) checkPair(k Kind, a, b liveEvent) *Invalid {
	as, bs := periodStart(a), periodStart(b)
	if as.Compare(bs) < 0 {
		return nil
	}
	offender, other := b, a
	if a.pos > b.pos {
		offender, other = a, b
	}
	if offender.pos == a.pos && as.Compare(bs) > 0 {
		return r.finding(codePeriodOutOfOrder, string(k), fmt.Sprintf(
			"The %s period start %s set by %s sorts before %s, which sets the lower start %s, so the %s period starts do not rise in time order",
			k, as, r.ref(a), r.ref(b), bs, k,
		), a, b)
	}
	return r.finding(codePeriodUnchanged, string(k), fmt.Sprintf(
		"The %s period start %s set by %s is not later than the start %s set by %s, so the %s period does not move forward",
		k, periodStart(offender), r.ref(offender), periodStart(other), r.ref(other), k,
	), a, b)
}
