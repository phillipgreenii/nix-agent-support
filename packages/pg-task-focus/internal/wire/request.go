package wire

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// Time is an instant a client sends: RFC 3339 with any offset, read as the
// instant it names. Marshalling writes it as UTC with milliseconds.
type Time struct{ time.Time }

// UnmarshalJSON reads an RFC 3339 string.
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("an instant is a string in RFC 3339 form, such as 2026-10-07T13:30:00Z")
	}
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("%q is not an RFC 3339 instant (for example 2026-10-07T13:30:00Z): %v", s, err)
	}
	t.Time = v.UTC()
	return nil
}

// MarshalJSON writes the instant as the log does.
func (t Time) MarshalJSON() ([]byte, error) { return event.At(t.Time).MarshalJSON() }

func (t *Time) ptr() *time.Time {
	if t == nil {
		return nil
	}
	v := t.Time
	return &v
}

// PeriodChange is one period a change begins.
type PeriodChange struct {
	Kind  string      `json:"kind"`
	Start civil.Date  `json:"start"`
	End   *civil.Date `json:"end,omitempty"`
	TZ    string      `json:"tz"`
	Label string      `json:"label,omitempty"`
}

// Override skips one open task of a period being left, for a reason.
type Override struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

// ChangePeriodsRequest is the body of POST /periods/change.
type ChangePeriodsRequest struct {
	ID              string         `json:"id,omitempty"`
	Changes         []PeriodChange `json:"changes"`
	EffectiveAt     *Time          `json:"effective_at,omitempty"`
	Profile         string         `json:"profile,omitempty"`
	Overrides       []Override     `json:"overrides,omitempty"`
	SkipAllReason   *string        `json:"skip_all_reason,omitempty"`
	ExpectedVersion *Version       `json:"expected_version,omitempty"`
	DryRun          bool           `json:"dry_run,omitempty"`
}

// ToCommand converts the request.
func (r ChangePeriodsRequest) ToCommand() command.ChangePeriods {
	c := command.ChangePeriods{
		ID: event.ID(r.ID), EffectiveAt: r.EffectiveAt.ptr(), Profile: r.Profile, SkipAllReason: r.SkipAllReason,
		ExpectedVersion: r.ExpectedVersion.ToCommand(), DryRun: r.DryRun,
	}
	for _, ch := range r.Changes {
		c.Changes = append(c.Changes, command.PeriodChange{
			Kind: projection.Kind(ch.Kind), Start: ch.Start, End: ch.End, TZ: ch.TZ, Label: ch.Label,
		})
	}
	for _, o := range r.Overrides {
		c.Overrides = append(c.Overrides, command.Override{TaskID: event.TaskID(o.TaskID), Reason: o.Reason})
	}
	return c
}

// ChangeProfileRequest is the body of POST /profile/change.
type ChangeProfileRequest struct {
	ID              string   `json:"id,omitempty"`
	Profile         string   `json:"profile"`
	ExpectedVersion *Version `json:"expected_version,omitempty"`
	DryRun          bool     `json:"dry_run,omitempty"`
}

// ToCommand converts the request.
func (r ChangeProfileRequest) ToCommand() command.ChangeProfile {
	return command.ChangeProfile{
		ID: event.ID(r.ID), Profile: r.Profile, ExpectedVersion: r.ExpectedVersion.ToCommand(), DryRun: r.DryRun,
	}
}

// CompleteTaskRequest is the body of POST /tasks/{id}/complete.
type CompleteTaskRequest struct {
	ID          string `json:"id,omitempty"`
	EffectiveAt *Time  `json:"effective_at,omitempty"`
}

// SkipTaskRequest is the body of POST /tasks/{id}/skip.
type SkipTaskRequest struct {
	ID          string `json:"id,omitempty"`
	Reason      string `json:"reason"`
	EffectiveAt *Time  `json:"effective_at,omitempty"`
}

// StartCycleRequest is the body of POST /cycles/start.
type StartCycleRequest struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	Minutes     *int   `json:"minutes,omitempty"`
	EffectiveAt *Time  `json:"effective_at,omitempty"`
}

// CycleVerbRequest is the body of POST /cycles/{pause,resume,stop}.
type CycleVerbRequest struct {
	ID          string `json:"id,omitempty"`
	CycleID     string `json:"cycle_id,omitempty"`
	EffectiveAt *Time  `json:"effective_at,omitempty"`
}

// BoostCycleRequest is the body of POST /cycles/boost.
type BoostCycleRequest struct {
	ID          string `json:"id,omitempty"`
	CycleID     string `json:"cycle_id,omitempty"`
	Minutes     int    `json:"minutes"`
	EffectiveAt *Time  `json:"effective_at,omitempty"`
}

// SwitchCycleRequest is the body of POST /cycles/switch.
type SwitchCycleRequest struct {
	ID          string `json:"id,omitempty"`
	To          string `json:"to"`
	EffectiveAt *Time  `json:"effective_at,omitempty"`
}

// AnnotateCycleRequest is the body of POST /cycles/annotate: the whole form.
type AnnotateCycleRequest struct {
	ID      string `json:"id,omitempty"`
	CycleID string `json:"cycle_id,omitempty"`
	Note    string `json:"note,omitempty"`
	KV      []KV   `json:"kv,omitempty"`
}

// BreakCycleRequest is the body of POST /cycles/break.
type BreakCycleRequest struct {
	ID      string `json:"id,omitempty"`
	CycleID string `json:"cycle_id"`
	From    *Time  `json:"from"`
	To      *Time  `json:"to"`
}

// CorrectRequest is the body of POST /events/{id}/correct.
type CorrectRequest struct {
	ID     string                     `json:"id,omitempty"`
	Fields map[string]json.RawMessage `json:"fields"`
	Reason string                     `json:"reason,omitempty"`
}

// RetractRequest is the body of POST /events/{id}/retract and
// POST /batches/{id}/retract.
type RetractRequest struct {
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// KVsToEvent converts the pairs of an annotation.
func KVsToEvent(in []KV) []event.KV {
	out := make([]event.KV, 0, len(in))
	for _, p := range in {
		out = append(out, event.KV{Key: p.Key, Value: p.Value})
	}
	return out
}

// Instants converts the optional effective instant of a request.
func EffectiveAt(t *Time) *time.Time { return t.ptr() }
