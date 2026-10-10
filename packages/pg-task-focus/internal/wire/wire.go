// Package wire defines the JSON the daemon's HTTP API speaks and the CLI
// prints: the state, the results of mutations, previews, events, the calendar
// and attention reads, the health document and the RFC 9457 problem. It is
// the Go side of api/openapi.yaml; a contract test checks that every response
// the server sends validates against that document.
//
// None of these shapes is the Go struct shape of the library: durations are
// whole seconds, instants are RFC 3339 UTC with milliseconds, and the library's
// golden state files are not a wire format.
package wire

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

// Instant is an instant on the wire: RFC 3339 in UTC with milliseconds.
type Instant = event.Instant

func at(t time.Time) Instant { return event.At(t) }

func atPtr(t *time.Time) *Instant {
	if t == nil {
		return nil
	}
	i := at(*t)
	return &i
}

// Version is the state version: the number of lines in the log and the
// configuration generation. Clients compare it only for inequality.
type Version struct {
	LogLines         int   `json:"log_lines"`
	ConfigGeneration int64 `json:"config_generation"`
}

// FromVersion converts the engine's version.
func FromVersion(v engine.Version) Version {
	return Version{LogLines: v.LogLines, ConfigGeneration: v.ConfigGeneration}
}

// ToCommand converts a client's expected version.
func (v *Version) ToCommand() *command.Version {
	if v == nil {
		return nil
	}
	return &command.Version{LogLines: v.LogLines, ConfigGeneration: v.ConfigGeneration}
}

// Store is the health of the event store as /state and /healthz carry it.
// Reason and Since are present only while the store is read-only.
type Store struct {
	State  string   `json:"state"`
	Reason string   `json:"reason,omitempty"`
	Since  *Instant `json:"since,omitempty"`
}

// The states of the store.
const (
	StoreOK       = "ok"
	StoreReadOnly = "read_only"
)

// FromStoreHealth converts the view's store health.
func FromStoreHealth(h view.StoreHealth) Store {
	if !h.ReadOnly {
		return Store{State: StoreOK}
	}
	since := at(h.Since)
	return Store{State: StoreReadOnly, Reason: h.Reason, Since: &since}
}

// ReadOnlySentence is the sentence every client shows while the store is
// read-only; empty for a healthy store.
func (s Store) ReadOnlySentence() string {
	if s.State != StoreReadOnly {
		return ""
	}
	return "READ-ONLY: " + s.Reason + ". Restart pg-task-focus to recover"
}

// Period is the current period of one kind.
type Period struct {
	Kind   string `json:"kind"`
	Start  string `json:"start"`
	End    string `json:"end"`
	Label  string `json:"label,omitempty"`
	Zone   string `json:"zone"`
	Today  string `json:"today"`
	Ended  bool   `json:"ended"`
	Banner string `json:"banner,omitempty"`
}

// Task is one task of a current period.
type Task struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Definition  string   `json:"definition"`
	Cadence     string   `json:"cadence"`
	PeriodStart string   `json:"period_start"`
	Title       string   `json:"title"`
	Group       string   `json:"group,omitempty"`
	Link        string   `json:"link,omitempty"`
	Due         Instant  `json:"due"`
	DueZone     string   `json:"due_zone"`
	Status      string   `json:"status"`
	Overdue     bool     `json:"overdue"`
	ResolvedAt  *Instant `json:"resolved_at,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

// KV is one key/value pair of a cycle's form.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// AlertSettings are the sounds and repeat interval a cycle type has now. A
// sound is its name, or null for no sound.
type AlertSettings struct {
	Sound         *string `json:"sound"`
	ReminderSound *string `json:"reminder_sound"`
	RepeatMinutes int     `json:"repeat_minutes"`
}

// Cycle is a cycle with its timer read at the read instant. Seconds are whole
// seconds; RemainingSeconds is negative in overtime. EndsAt is set for the
// running cycle only: the instant its time is up if nothing changes.
type Cycle struct {
	ID               string        `json:"id"`
	Type             string        `json:"type"`
	Title            string        `json:"title"`
	PlannedMinutes   int           `json:"planned_minutes"`
	BoostMinutes     int           `json:"boost_minutes"`
	Status           string        `json:"status"`
	StartedAt        *Instant      `json:"started_at,omitempty"`
	RunningSince     *Instant      `json:"running_since,omitempty"`
	StoppedAt        *Instant      `json:"stopped_at,omitempty"`
	ElapsedSeconds   int64         `json:"elapsed_seconds"`
	RemainingSeconds int64         `json:"remaining_seconds"`
	Overtime         bool          `json:"overtime"`
	EndsAt           *Instant      `json:"ends_at,omitempty"`
	NotInProfile     bool          `json:"not_in_profile"`
	InterruptedBy    string        `json:"interrupted_by,omitempty"`
	Note             string        `json:"note,omitempty"`
	KV               []KV          `json:"kv"`
	Alert            AlertSettings `json:"alert"`
}

// DimmedCycle is a paused cycle beside the focus.
type DimmedCycle struct {
	Cycle
	CanSwitch bool `json:"can_switch"`
}

// ResumeOffer names the cycle the operator is asked to return to; Action is
// "resume" or "switch".
type ResumeOffer struct {
	Cycle  Cycle  `json:"cycle"`
	Action string `json:"action"`
}

// State is what a client shows at one instant.
type State struct {
	ReadAt         Instant       `json:"read_at"`
	Initialized    bool          `json:"initialized"`
	Profile        string        `json:"profile,omitempty"`
	Version        Version       `json:"version"`
	Store          Store         `json:"store"`
	Periods        []Period      `json:"periods"`
	Tasks          []Task        `json:"tasks"`
	Next           *Task         `json:"next"`
	Focus          *Cycle        `json:"focus"`
	Dimmed         []DimmedCycle `json:"dimmed"`
	InterruptStack []Cycle       `json:"interrupt_stack"`
	ResumeOffer    *ResumeOffer  `json:"resume_offer"`
}

// FromState converts the view's state, read at now, with version v.
func FromState(s view.State, now time.Time, v engine.Version) State {
	out := State{
		ReadAt: at(now), Initialized: s.Initialized, Profile: s.Profile, Version: FromVersion(v),
		Store:   FromStoreHealth(s.Store),
		Periods: []Period{}, Tasks: []Task{}, Dimmed: []DimmedCycle{}, InterruptStack: []Cycle{},
	}
	for _, p := range s.Periods {
		out.Periods = append(out.Periods, Period{
			Kind: string(p.Kind), Start: p.Start.String(), End: p.End.String(), Label: p.Label,
			Zone: p.Zone, Today: p.Today.String(), Ended: p.Ended, Banner: p.Banner,
		})
	}
	for _, t := range s.Tasks {
		out.Tasks = append(out.Tasks, fromTask(t))
	}
	if s.Next != nil {
		n := fromTask(*s.Next)
		out.Next = &n
	}
	if s.Focus != nil {
		c := fromCycle(*s.Focus, now)
		out.Focus = &c
	}
	for _, d := range s.Dimmed {
		out.Dimmed = append(out.Dimmed, DimmedCycle{Cycle: fromCycle(d.CycleView, now), CanSwitch: d.CanSwitch})
	}
	for _, c := range s.InterruptStack {
		out.InterruptStack = append(out.InterruptStack, fromCycle(c, now))
	}
	if s.ResumeOffer != nil {
		out.ResumeOffer = &ResumeOffer{Cycle: fromCycle(s.ResumeOffer.Cycle, now), Action: string(s.ResumeOffer.Action)}
	}
	return out
}

func fromTask(t view.TaskView) Task {
	return Task{
		ID: string(t.Task.ID), Kind: string(t.Kind), Definition: t.Task.Definition,
		Cadence: string(t.Task.Cadence), PeriodStart: t.Task.PeriodStart.String(),
		Title: t.Task.Title, Group: t.Task.Group, Link: t.Task.Link, Due: at(t.Task.Due),
		DueZone: t.Task.DueRule.TZ.Name(), Status: string(t.Task.Status), Overdue: t.Overdue,
		ResolvedAt: atPtr(t.Task.ResolvedAt), Reason: t.Task.Reason,
	}
}

func fromCycle(v view.CycleView, now time.Time) Cycle {
	c := v.Cycle
	out := Cycle{
		ID: string(c.ID), Type: c.Type, Title: c.Title, PlannedMinutes: c.PlannedMinutes,
		Status: string(c.Status), StoppedAt: atPtr(c.StoppedAt),
		ElapsedSeconds: int64(v.Elapsed / time.Second), RemainingSeconds: int64(v.Remaining / time.Second),
		Overtime: v.Overtime, NotInProfile: v.NotInProfile, InterruptedBy: string(c.InterruptedBy),
		Note: c.Note, KV: kvs(c.KV), Alert: fromAlert(v),
	}
	for _, b := range c.Boosts {
		out.BoostMinutes += b.Minutes
	}
	if len(c.Segments) > 0 {
		s := at(c.Segments[0].Start)
		out.StartedAt = &s
	}
	if c.Status == projection.Running {
		e := at(now.Add(v.Remaining))
		out.EndsAt = &e
		if n := len(c.Segments); n > 0 {
			since := at(c.Segments[n-1].Start)
			out.RunningSince = &since
		}
	}
	return out
}

func kvs(in []event.KV) []KV {
	out := make([]KV, 0, len(in))
	for _, p := range in {
		out = append(out, KV{Key: p.Key, Value: p.Value})
	}
	return out
}

func fromAlert(v view.CycleView) AlertSettings {
	name := func(n string) *string {
		if n == "" {
			return nil
		}
		return &n
	}
	return AlertSettings{
		Sound: name(v.Alert.Sound.Name()), ReminderSound: name(v.Alert.ReminderSound.Name()),
		RepeatMinutes: v.Alert.RepeatMinutes,
	}
}

// Result is the success of a mutation. A no-op is Changed false with a Note
// and no EventIDs. Replayed is set when the answer is the one an earlier
// request with the same id and content received.
type Result struct {
	Changed  bool     `json:"changed"`
	Note     string   `json:"note,omitempty"`
	EventIDs []string `json:"event_ids"`
	BatchID  string   `json:"batch_id,omitempty"`
	Replayed bool     `json:"replayed,omitempty"`
	Version  Version  `json:"version"`
}

// FromResult converts the engine's result.
func FromResult(r engine.Result) Result {
	out := Result{
		Changed: r.Changed, Note: r.Note, EventIDs: []string{}, BatchID: string(r.BatchID),
		Replayed: r.Replayed, Version: FromVersion(r.Version),
	}
	for _, id := range r.EventIDs {
		out.EventIDs = append(out.EventIDs, string(id))
	}
	return out
}

// TaskRef names a task in a preview.
type TaskRef struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Overdue bool   `json:"overdue"`
}

// NotMaterialized is a definition a new period could not materialize.
type NotMaterialized struct {
	Definition string `json:"definition"`
	Reason     string `json:"reason"`
}

// CycleRef names a cycle in a preview or a refusal.
type CycleRef struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status,omitempty"`
	StartedAt *Instant `json:"started_at,omitempty"`
}

// Preview is what the dry run of a period or profile change reports.
type Preview struct {
	Leaving          []TaskRef         `json:"leaving"`
	Materialize      []TaskRef         `json:"materialize"`
	ProfileAdd       []TaskRef         `json:"profile_add"`
	ProfileWithdraw  []TaskRef         `json:"profile_withdraw"`
	ProfileReinstate []TaskRef         `json:"profile_reinstate"`
	NotMaterialized  []NotMaterialized `json:"not_materialized"`
	BlockingCycles   []CycleRef        `json:"blocking_cycles"`
}

// DryRunResult is the answer to a dry run: what the real request would
// affect at StateVersion, which the client carries back as expected_version.
type DryRunResult struct {
	DryRun       bool    `json:"dry_run"`
	Note         string  `json:"note,omitempty"`
	Preview      Preview `json:"preview"`
	StateVersion Version `json:"state_version"`
}

// FromDryRun converts the engine's dry-run result.
func FromDryRun(r engine.Result) DryRunResult {
	out := DryRunResult{DryRun: true, Note: r.Note, StateVersion: FromVersion(r.Version)}
	p := r.Preview
	out.Preview = Preview{
		Leaving: refs(nil), Materialize: refs(nil), ProfileAdd: refs(nil), ProfileWithdraw: refs(nil),
		ProfileReinstate: refs(nil), NotMaterialized: []NotMaterialized{}, BlockingCycles: []CycleRef{},
	}
	if p == nil {
		return out
	}
	out.Preview.Leaving = refs(p.Leaving)
	out.Preview.Materialize = refs(p.Materialize)
	out.Preview.ProfileAdd = refs(p.ProfileAdd)
	out.Preview.ProfileWithdraw = refs(p.ProfileWithdraw)
	out.Preview.ProfileReinstate = refs(p.ProfileReinstate)
	for _, n := range p.NotMaterialized {
		out.Preview.NotMaterialized = append(out.Preview.NotMaterialized, NotMaterialized{Definition: n.Definition, Reason: n.Reason})
	}
	out.Preview.BlockingCycles = cycleRefs(p.BlockingCycles)
	return out
}

func refs(in []command.TaskRef) []TaskRef {
	out := make([]TaskRef, 0, len(in))
	for _, t := range in {
		out = append(out, TaskRef{ID: string(t.ID), Title: t.Title, Overdue: t.Overdue})
	}
	return out
}

func cycleRefs(in []command.CycleRef) []CycleRef {
	out := make([]CycleRef, 0, len(in))
	for _, c := range in {
		r := CycleRef{ID: string(c.ID), Title: c.Title, Status: string(c.Status)}
		if !c.StartedAt.IsZero() {
			s := at(c.StartedAt)
			r.StartedAt = &s
		}
		out = append(out, r)
	}
	return out
}

// EventView is one logged event as the editor shows it. Event is the event in
// the view that was asked for (its corrected form by default); Original is the
// raw logged line, set only when the corrected view differs from it.
type EventView struct {
	ID          string          `json:"id"`
	Event       json.RawMessage `json:"event"`
	Original    json.RawMessage `json:"original,omitempty"`
	CorrectedBy []string        `json:"corrected_by"`
	Retracted   bool            `json:"retracted"`
	RetractedBy string          `json:"retracted_by,omitempty"`
}

// Events is the answer to GET /events.
type Events struct {
	View   string      `json:"view"`
	Events []EventView `json:"events"`
}

// FromEvents converts the projection's event views.
func FromEvents(views []projection.EventView, which string) (Events, error) {
	out := Events{View: which, Events: []EventView{}}
	for _, v := range views {
		shown := v.Corrected
		if which == projection.ViewOriginal {
			shown = v.Original
		}
		line, err := event.Encode(shown)
		if err != nil {
			return Events{}, fmt.Errorf("encoding event %s: %w", shown.ID, err)
		}
		ev := EventView{
			ID: string(v.Original.ID), Event: line, CorrectedBy: []string{}, Retracted: v.Retracted,
			RetractedBy: string(v.RetractedBy),
		}
		for _, id := range v.CorrectedBy {
			ev.CorrectedBy = append(ev.CorrectedBy, string(id))
		}
		if which != projection.ViewOriginal && len(v.CorrectedBy) > 0 {
			orig, err := event.Encode(v.Original)
			if err != nil {
				return Events{}, fmt.Errorf("encoding event %s: %w", v.Original.ID, err)
			}
			ev.Original = orig
		}
		out.Events = append(out.Events, ev)
	}
	return out, nil
}

// SortReasons returns the reasons as strings in a stable order.
func SortReasons(rs []command.Reason) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	slices.SortFunc(out, strings.Compare)
	return out
}

// byStart orders segments by start.
func byStart(a, b Segment) int {
	return cmp.Compare(a.Start.Time().UnixNano(), b.Start.Time().UnixNano())
}

// Segment is one running interval of a cycle as the calendar shows it.
type Segment struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Start Instant `json:"start"`
	End   Instant `json:"end"`
	Notes string  `json:"notes"`
}

// Calendar is the answer to GET /calendar: the segments overlapping the
// requested window. AsOf is the read time and Stale is false on every answer
// from the daemon.
type Calendar struct {
	AsOf       Instant   `json:"as_of"`
	Stale      bool      `json:"stale"`
	CalendarID string    `json:"calendar_id"`
	Calendar   string    `json:"calendar"`
	Events     []Segment `json:"events"`
}

// The fixed calendar of work cycles.
const (
	CalendarID   = "focus-cycles"
	CalendarName = "Focus cycles"
)

// BuildCalendar lists the segments of the cycles that overlap [from, to) read
// at now. An open segment ends at now, and a zero-length segment is never
// listed. A segment's id is the id of the event that opened it.
func BuildCalendar(cycles []projection.Cycle, from, to, now time.Time) Calendar {
	out := Calendar{AsOf: at(now), CalendarID: CalendarID, Calendar: CalendarName, Events: []Segment{}}
	for _, c := range cycles {
		notes := Notes(c.Type, c.KV, c.Note)
		for _, s := range c.Segments {
			end := now
			if s.End != nil {
				end = *s.End
			}
			if !end.After(s.Start) || !s.Start.Before(to) || !end.After(from) {
				continue
			}
			out.Events = append(out.Events, Segment{
				ID: string(s.OpenedBy), Title: c.Title, Start: at(s.Start), End: at(end), Notes: notes,
			})
		}
	}
	slices.SortStableFunc(out.Events, byStart)
	return out
}

// Notes is the notes block of a calendar event: the reserved cycle_type pair,
// the operator's key/value pairs in the order entered (a repeated key is
// several values), a line that is exactly "---", then the free-text note. The
// "---" line is always present. A value is one line: a newline or tab becomes
// one space and the value is trimmed.
func Notes(cycleType string, kv []event.KV, note string) string {
	var b strings.Builder
	b.WriteString("cycle_type: " + oneLine(cycleType) + "\n")
	for _, p := range kv {
		b.WriteString(p.Key + ": " + oneLine(p.Value) + "\n")
	}
	b.WriteString("---\n")
	b.WriteString(note)
	return b.String()
}

func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// ParseNotes is the reference parser of the notes format: it reads lines until
// the first line equal to "---" as pairs, and everything after it is the
// note. It reports whether the separator was found.
func ParseNotes(notes string) (pairs []KV, note string, ok bool) {
	rest := notes
	for rest != "" {
		line, tail, found := strings.Cut(rest, "\n")
		if line == "---" {
			return pairs, tail, true
		}
		if !found {
			return pairs, "", false
		}
		k, v, _ := strings.Cut(line, ": ")
		pairs = append(pairs, KV{Key: k, Value: v})
		rest = tail
	}
	return pairs, "", false
}

// AttentionItem is one item of the attention feed.
type AttentionItem struct {
	Type     string         `json:"type"`
	ID       string         `json:"id"`
	Summary  string         `json:"summary"`
	Severity string         `json:"severity"`
	Group    AttentionGroup `json:"group"`
	URL      string         `json:"url,omitempty"`
}

// AttentionGroup is the group object of an attention item.
type AttentionGroup struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Attention is the answer to GET /attention. The feed is stateless: an item
// exists while its condition holds.
type Attention struct {
	AsOf  Instant         `json:"as_of"`
	Items []AttentionItem `json:"items"`
}
