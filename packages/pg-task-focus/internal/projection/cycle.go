package projection

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// CycleStatus is where a cycle stands at an instant.
type CycleStatus string

// The statuses of a cycle.
const (
	NotStarted CycleStatus = "not_started"
	Running    CycleStatus = "running"
	Paused     CycleStatus = "paused"
	Stopped    CycleStatus = "stopped"
)

// The codes the cycles can find.
const (
	codeCycleStopped               Code = "cycle_stopped"
	codeAnotherCycleRunning        Code = "another_cycle_running"
	codeInterruptedCycleNotRunning Code = "interrupted_cycle_not_running"
	codeCycleSegmentsOverlap       Code = "cycle_segments_overlap"
	codeBreakEndsAtStop            Code = "break_ends_at_stop"
	codeStopNotAfterStart          Code = "stop_not_after_start"
	codeEmptyRunningSegment        Code = "empty_running_segment"
	codeCycleEventBeforeStart      Code = "cycle_event_before_start"
)

// Segment is one stretch of a cycle's running time, from Start up to End, which
// is nil while the segment is open. OpenedBy is the cycle.started or
// cycle.resumed that set the cycle running.
type Segment struct {
	Start    time.Time
	End      *time.Time
	OpenedBy event.ID
}

// Boost is minutes added to a cycle, effective at At.
type Boost struct {
	At      time.Time
	Minutes int
}

// Cycle is one work cycle, as its cycle.started snapshot says and as the live
// events since have left it. Status is its state at the end of the log and
// StoppedAt the instant it stopped. InterruptedBy is the cycle that displaced
// it, by an interrupting start or a switch, until it is resumed or stopped.
// Note and KV are the latest annotation, whole.
type Cycle struct {
	ID             event.CycleID
	Type           string
	Title          string
	PlannedMinutes int
	Boosts         []Boost
	Status         CycleStatus
	StoppedAt      *time.Time
	Segments       []Segment
	InterruptedBy  event.CycleID
	Note           string
	KV             []event.KV
}

// clone copies the cycle so that no slice or pointer is shared with it.
func (c Cycle) clone() Cycle {
	c.Boosts = slices.Clone(c.Boosts)
	c.KV = slices.Clone(c.KV)
	c.StoppedAt = clonePtr(c.StoppedAt)
	c.Segments = slices.Clone(c.Segments)
	for i := range c.Segments {
		c.Segments[i].End = clonePtr(c.Segments[i].End)
	}
	return c
}

func clonePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

// pausedAt is the end of the cycle's latest segment, the instant it last
// stopped running.
func (c Cycle) pausedAt() time.Time {
	if n := len(c.Segments); n > 0 && c.Segments[n-1].End != nil {
		return *c.Segments[n-1].End
	}
	return time.Time{}
}

// Cycle returns the cycle with the given id, as a copy.
func (m *Model) Cycle(id event.CycleID) (Cycle, bool) {
	i, ok := m.cycleIndex[id]
	if !ok {
		return Cycle{}, false
	}
	return m.cycles[i].clone(), true
}

// Cycles lists every cycle in the log order of its cycle.started. The caller
// owns the result.
func (m *Model) Cycles() []Cycle {
	out := make([]Cycle, len(m.cycles))
	for i, c := range m.cycles {
		out[i] = c.clone()
	}
	return out
}

// Running returns the cycle that is running at the end of the log: the focus.
// A valid log has at most one.
func (m *Model) Running() (Cycle, bool) {
	for _, c := range m.cycles {
		if c.Status == Running {
			return c.clone(), true
		}
	}
	return Cycle{}, false
}

// RunningAt returns the cycle running at instant t.
func (m *Model) RunningAt(t time.Time) (Cycle, bool) {
	for _, c := range m.cycles {
		if c.StatusAt(t) == Running {
			return c.clone(), true
		}
	}
	return Cycle{}, false
}

// Dimmed lists the cycles shown beside the focus: every cycle paused at the
// end of the log, whether interrupted, paused by hand or switched away from,
// most recently paused first. A stopped cycle is never dimmed.
func (m *Model) Dimmed() []Cycle {
	var out []Cycle
	for _, c := range m.cycles {
		if c.Status == Paused {
			out = append(out, c.clone())
		}
	}
	slices.SortStableFunc(out, func(a, b Cycle) int {
		return cmp.Or(b.pausedAt().Compare(a.pausedAt()), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// stepKind is what one entry of a cycle's sequence does. At one instant and
// log position the derived pause sorts before the start, so a cycle.started
// naming its own cycle in interrupts finds it not running.
type stepKind int

const (
	stepInterrupted stepKind = iota // the pause a cycle.started naming the cycle in interrupts derives
	stepStart
	stepPause
	stepResume
	stepBoost
	stepStop
	stepAnnotate
)

// verbs say how a step reads in a sentence.
var verbs = map[stepKind]string{
	stepStart: "started", stepPause: "paused", stepResume: "resumed",
	stepBoost: "boosted", stepStop: "stopped", stepAnnotate: "annotated",
}

// step is one entry of a cycle's sequence: an event of the cycle or, for
// stepInterrupted, the cycle.started of the cycle that interrupts it.
type step struct {
	kind stepKind
	ev   liveEvent
}

// stepOrder is the order of a cycle's sequence: by effective_at, then log
// position, never by id.
func stepOrder(a, b step) int { return cmp.Or(order(a.ev, b.ev), cmp.Compare(a.kind, b.kind)) }

// cycleStep is the cycle a live event is about and the step it is.
func cycleStep(p event.Payload) (event.CycleID, stepKind, bool) {
	switch p := p.(type) {
	case event.CycleStarted:
		return p.CycleID, stepStart, true
	case event.CyclePaused:
		return p.CycleID, stepPause, true
	case event.CycleResumed:
		return p.CycleID, stepResume, true
	case event.CycleBoosted:
		return p.CycleID, stepBoost, true
	case event.CycleStopped:
		return p.CycleID, stepStop, true
	case event.CycleAnnotated:
		return p.CycleID, stepAnnotate, true
	}
	return "", 0, false
}

// startedCycle is the cycle a cycle.started begins.
func startedCycle(e liveEvent) event.CycleID { return e.Payload.(event.CycleStarted).CycleID }

// cycleRun is one projection of the live events onto cycles.
type cycleRun struct {
	*run
	stored  map[event.CycleID]bool   // the cycles a stored event names
	members map[event.ID][]liveEvent // the live cycle.paused and cycle.resumed of each batch
}

// runSegment is a segment with the cycle it belongs to and the event that
// opened it, for the check that one cycle runs at a time.
type runSegment struct {
	cycle  event.CycleID
	start  time.Time
	end    *time.Time
	opener liveEvent
}

// projectCycles groups the live cycle events by cycle, runs each cycle's state
// machine over its sequence, then checks that no two cycles run at once, and
// returns the cycles in the log order of their starts.
//
// Rule: a cycle's sequence is its own events plus, for every cycle.started
// that names it in interrupts, the pause that start derives, effective at that
// start's effective_at and log position. A paused and a resumed of different
// cycles sharing one batch are a switch, which links the paused cycle to the
// resumed one; a paused and a resumed of the same cycle sharing one batch are
// a break. Batch membership is read only through BatchID.
func (r *run) projectCycles(live []liveEvent) ([]Cycle, error) {
	cr := &cycleRun{run: r, stored: map[event.CycleID]bool{}, members: map[event.ID][]liveEvent{}}
	for _, e := range r.log[:r.firstAdded] {
		if id, _, ok := cycleStep(e.Payload); ok {
			cr.stored[id] = true
		}
		if p, ok := e.Payload.(event.CycleStarted); ok && p.Interrupts != "" {
			cr.stored[p.Interrupts] = true
		}
	}

	var ids []event.CycleID
	seqs := map[event.CycleID][]step{}
	add := func(id event.CycleID, s step) {
		if _, ok := seqs[id]; !ok {
			ids = append(ids, id)
		}
		seqs[id] = append(seqs[id], s)
	}
	for _, e := range live {
		id, kind, ok := cycleStep(e.Payload)
		if !ok {
			continue
		}
		add(id, step{kind, e})
		if p, ok := e.Payload.(event.CycleStarted); ok && p.Interrupts != "" {
			add(p.Interrupts, step{stepInterrupted, e})
		}
		if b := e.Payload.BatchID(); b != "" {
			cr.members[b] = append(cr.members[b], e)
		}
	}

	type started struct {
		pos   int
		cycle Cycle
	}
	var out []started
	var segs []runSegment
	for _, id := range ids {
		seq := seqs[id]
		slices.SortStableFunc(seq, stepOrder)
		m := &machine{r: cr, c: Cycle{ID: id}, status: NotStarted}
		if inv := m.run(seq); inv != nil {
			return nil, inv
		}
		out = append(out, started{m.start.pos, m.c})
		for i, s := range m.c.Segments {
			segs = append(segs, runSegment{cycle: id, start: s.Start, end: s.End, opener: m.openers[i]})
		}
	}
	if inv := cr.checkOneRunning(segs); inv != nil {
		return nil, inv
	}
	slices.SortFunc(out, func(a, b started) int { return cmp.Compare(a.pos, b.pos) })
	cycles := make([]Cycle, len(out))
	for i, s := range out {
		cycles[i] = s.cycle
	}
	return cycles, nil
}

// cycle names a cycle inside a sentence: a stored one by id, and one that
// only the events a request would add name as the new cycle.
func (r *cycleRun) cycle(id event.CycleID) string {
	if r.stored[id] {
		return "cycle " + string(id)
	}
	return "the new cycle"
}

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

// cycleFinding is finding for a cycle: the entity and the listed cycles are
// only stored ones.
func (r *cycleRun) cycleFinding(code Code, entity event.CycleID, cycles []event.CycleID, sentence string, involved ...liveEvent) *Invalid {
	name := ""
	if r.stored[entity] {
		name = string(entity)
	}
	inv := r.finding(code, name, sentence, involved...)
	for _, c := range cycles {
		if r.stored[c] && !slices.Contains(inv.Cycles, c) {
			inv.Cycles = append(inv.Cycles, c)
		}
	}
	return inv
}

// isBreak reports whether e, a paused or resumed of cycle id, is a member of a
// break: its batch also pauses and resumes id.
func (r *cycleRun) isBreak(e liveEvent, id event.CycleID) bool {
	var paused, resumed bool
	for _, o := range r.members[e.Payload.BatchID()] {
		switch p := o.Payload.(type) {
		case event.CyclePaused:
			paused = paused || p.CycleID == id
		case event.CycleResumed:
			resumed = resumed || p.CycleID == id
		}
	}
	return paused && resumed
}

// switchedTo is the cycle a switch resumes when e, a pause of cycle id, is
// a member of the switch: its batch resumes a different cycle.
func (r *cycleRun) switchedTo(e liveEvent, id event.CycleID) (event.CycleID, bool) {
	b := e.Payload.BatchID()
	if b == "" {
		return "", false
	}
	for _, o := range r.members[b] {
		if p, ok := o.Payload.(event.CycleResumed); ok && p.CycleID != id {
			return p.CycleID, true
		}
	}
	return "", false
}

// checkOneRunning finds two running segments of different cycles that
// overlap. The offender is the segment whose opening event comes later in the
// log.
func (r *cycleRun) checkOneRunning(segs []runSegment) *Invalid {
	slices.SortFunc(segs, func(a, b runSegment) int {
		return cmp.Or(a.start.Compare(b.start), cmp.Compare(a.opener.pos, b.opener.pos))
	})
	var widest *runSegment // of the segments so far, the one that ends last
	for i := range segs {
		s := &segs[i]
		if widest != nil && (widest.end == nil || s.start.Before(*widest.end)) {
			off, other := s, widest
			if other.opener.pos > off.opener.pos {
				off, other = other, off
			}
			return r.cycleFinding(codeAnotherCycleRunning, off.cycle, []event.CycleID{other.cycle, off.cycle}, fmt.Sprintf(
				"The running segment of %s opened by %s overlaps the running segment of %s opened by %s, and only one cycle runs at a time",
				r.cycle(off.cycle), r.ref(off.opener), r.cycle(other.cycle), r.ref(other.opener),
			), off.opener, other.opener)
		}
		if widest == nil || (widest.end != nil && (s.end == nil || s.end.After(*widest.end))) {
			widest = s
		}
	}
	return nil
}

// machine runs one cycle's state machine over its sequence.
type machine struct {
	r       *cycleRun
	c       Cycle
	status  CycleStatus
	first   *liveEvent  // the first start of the sequence, for a finding before it
	start   *liveEvent  // the start the machine has applied
	opener  liveEvent   // the start or resume that opened the latest segment
	closer  step        // the step that ended the latest segment
	stop    liveEvent   // the stop, once stopped
	openers []liveEvent // the event that opened each segment
}

func (m *machine) run(seq []step) *Invalid {
	for i := range seq {
		if seq[i].kind == stepStart {
			m.first = &seq[i].ev
			break
		}
	}
	for _, s := range seq {
		if inv := m.apply(s); inv != nil {
			return inv
		}
	}
	m.c.Status = m.status
	return nil
}

// apply runs one step: the state table, with every step replay cannot accept
// given its own code. A pause of a paused cycle and a resume of a running one
// are never no-ops here.
func (m *machine) apply(s step) *Invalid {
	e, t := s.ev, s.ev.EffectiveAt.Time()
	if m.status == NotStarted && s.kind != stepStart {
		return m.beforeStart(s)
	}
	switch s.kind {
	case stepStart:
		if m.start != nil {
			return m.finding(codeCycleSegmentsOverlap, fmt.Sprintf(
				"%s after %s started it, so its segments would overlap", m.does(s), m.r.ref(*m.start),
			), e, *m.start)
		}
		p := e.Payload.(event.CycleStarted)
		m.c.Type, m.c.Title, m.c.PlannedMinutes = p.Type, p.Title, p.PlannedMinutes
		m.start = &e
		m.open(e)
	case stepInterrupted:
		if m.status != Running {
			return m.notRunning(s)
		}
		if inv := m.closeAt(s); inv != nil {
			return inv
		}
		m.status = Paused
		m.c.InterruptedBy = startedCycle(e)
	case stepPause:
		switch m.status {
		case Stopped:
			return m.stopped(s)
		case Paused:
			return m.finding(codeCycleSegmentsOverlap, fmt.Sprintf(
				"%s while it is already paused since %s, so its segments would overlap", m.does(s), m.by(m.closer),
			), e, m.closer.ev)
		}
		if inv := m.closeAt(s); inv != nil {
			return inv
		}
		m.status = Paused
		if y, ok := m.r.switchedTo(e, m.c.ID); ok {
			m.c.InterruptedBy = y
		}
	case stepResume:
		switch m.status {
		case Stopped:
			return m.stopped(s)
		case Running:
			return m.finding(codeCycleSegmentsOverlap, fmt.Sprintf(
				"%s while it is already running since %s, so its segments would overlap", m.does(s), m.r.ref(m.opener),
			), e, m.opener)
		}
		m.open(e)
		m.c.InterruptedBy = ""
	case stepBoost:
		if m.status == Stopped {
			return m.stopped(s)
		}
		m.c.Boosts = append(m.c.Boosts, Boost{At: t, Minutes: e.Payload.(event.CycleBoosted).Minutes})
	case stepStop:
		if m.status == Stopped {
			return m.stopped(s)
		}
		if t.Equal(m.start.EffectiveAt.Time()) {
			return m.stopAtStart(s, *m.start)
		}
		if m.status == Running {
			if inv := m.closeAt(s); inv != nil {
				return inv
			}
		}
		m.status, m.stop, m.c.StoppedAt = Stopped, e, &t
		m.c.InterruptedBy = ""
	case stepAnnotate:
		p := e.Payload.(event.CycleAnnotated)
		m.c.Note, m.c.KV = p.Note, slices.Clone(p.KV)
	}
	return nil
}

// open starts a running segment at e.
func (m *machine) open(e liveEvent) {
	m.c.Segments = append(m.c.Segments, Segment{Start: e.EffectiveAt.Time(), OpenedBy: e.ID})
	m.openers = append(m.openers, e)
	m.opener, m.status = e, Running
}

// closeAt ends the open segment at the step's instant, which MUST be strictly
// after the instant that opened it: a running segment has a length.
func (m *machine) closeAt(s step) *Invalid {
	t := s.ev.EffectiveAt.Time()
	if !t.After(m.opener.EffectiveAt.Time()) {
		return m.finding(codeEmptyRunningSegment, fmt.Sprintf(
			"%s, which is not strictly after %s that set it running, so its running segment would have no length",
			m.does(s), m.r.ref(m.opener),
		), m.opener, s.ev)
	}
	m.c.Segments[len(m.c.Segments)-1].End = &t
	m.closer = s
	return nil
}

// finding is a finding about the machine's cycle, whose sentence begins with
// the cycle's name.
func (m *machine) finding(code Code, rest string, involved ...liveEvent) *Invalid {
	return m.r.cycleFinding(code, m.c.ID, nil, capitalize(m.r.cycle(m.c.ID))+" "+rest, involved...)
}

// does says what a step does to the machine's cycle, after its name.
func (m *machine) does(s step) string {
	if s.kind == stepInterrupted {
		return "is interrupted by " + m.by(s)
	}
	return fmt.Sprintf("is %s by %s", verbs[s.kind], m.r.ref(s.ev))
}

// by names the event of a step.
func (m *machine) by(s step) string {
	if s.kind == stepInterrupted {
		return fmt.Sprintf("the start of %s in %s", m.r.cycle(startedCycle(s.ev)), m.r.ref(s.ev))
	}
	return m.r.ref(s.ev)
}

func (m *machine) stopAtStart(s step, start liveEvent) *Invalid {
	return m.finding(codeStopNotAfterStart, fmt.Sprintf(
		"%s at the instant %s started it, and a stop must come after the start", m.does(s), m.r.ref(start),
	), s.ev, start)
}

// beforeStart judges a step that sorts before the cycle's start, or of a cycle
// with no live start.
func (m *machine) beforeStart(s step) *Invalid {
	if s.kind == stepInterrupted {
		return m.notRunning(s)
	}
	if m.first == nil {
		return m.finding(codeCycleEventBeforeStart, m.does(s)+", but no live cycle.started starts it", s.ev)
	}
	if s.kind == stepStop && s.ev.EffectiveAt.Time().Equal(m.first.EffectiveAt.Time()) {
		return m.stopAtStart(s, *m.first)
	}
	return m.finding(codeCycleEventBeforeStart, fmt.Sprintf(
		"%s, which sorts before %s that starts it, and no event of a cycle comes before its start",
		m.does(s), m.r.ref(*m.first),
	), s.ev, *m.first)
}

// notRunning judges an interrupting start whose interrupted cycle, the
// machine's, is not running at that instant. The offender is the cycle the
// start begins.
func (m *machine) notRunning(s step) *Invalid {
	involved := []liveEvent{s.ev}
	var state string
	switch {
	case m.status == Paused:
		state = "is paused since " + m.by(m.closer)
		involved = append(involved, m.closer.ev)
	case m.status == Stopped:
		state = "was stopped by " + m.r.ref(m.stop)
		involved = append(involved, m.stop)
	case startedCycle(s.ev) == m.c.ID:
		state = "is the cycle it starts"
	case m.first != nil:
		state = "starts only with " + m.r.ref(*m.first)
		involved = append(involved, *m.first)
	default:
		state = "has no live cycle.started"
	}
	y := startedCycle(s.ev)
	return m.r.cycleFinding(codeInterruptedCycleNotRunning, y, nil, fmt.Sprintf(
		"%s is started by %s interrupting %s, which %s at that instant, and only a running cycle can be interrupted",
		capitalize(m.r.cycle(y)), m.r.ref(s.ev), m.r.cycle(m.c.ID), state,
	), involved...)
}

// stopped judges a pause, resume, boost or stop of a stopped cycle. A member
// of a break the request adds is break_ends_at_stop, pointing at end at; a
// stored one is cycle_stopped and names its batch.
func (m *machine) stopped(s step) *Invalid {
	e := s.ev
	if (s.kind == stepPause || s.kind == stepResume) && m.r.isNew(e) && m.r.isBreak(e, m.c.ID) {
		edge := "begins"
		if s.kind == stepResume {
			edge = "ends"
		}
		return m.finding(codeBreakEndsAtStop, fmt.Sprintf(
			"has a back-filled break that %s with %s, not before %s stopped it, and a break must end strictly before the stop; to stop the cycle when the break begins, use end at instead",
			edge, m.r.ref(e), m.r.ref(m.stop),
		), m.stop, e)
	}
	batch := ""
	if b := e.Payload.BatchID(); b != "" {
		batch = fmt.Sprintf(", a member of batch %s,", b)
	}
	return m.finding(codeCycleStopped, fmt.Sprintf(
		"%s%s after %s stopped it, and a stopped cycle cannot change", m.does(s), batch, m.r.ref(m.stop),
	), e, m.stop)
}
