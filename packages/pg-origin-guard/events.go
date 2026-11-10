package originguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Event kinds.
const (
	EventGuardState = "guard_state"
	EventGuardFault = "guard_fault"
)

// Event is one line of events-YYYY-MM-DD.jsonl. It carries the realm name,
// the reason and timings only: never a host, user, principal or stderr text.
type Event struct {
	Time  time.Time `json:"ts"`
	Event string    `json:"event"`
	Realm string    `json:"realm"`
	// Available is 0 or 1. Loki's json parser turns it into a string label.
	Available *int   `json:"available,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// Transition is 1 when this event is a state change, 0 for a heartbeat.
	Transition *int `json:"transition,omitempty"`
	// LoginAttempt is the outcome of a login attempt that ended, when one did.
	LoginAttempt string `json:"login_attempt,omitempty"`
	// UnavailableSeconds is how long the realm has been unavailable.
	UnavailableSeconds *int64 `json:"unavailable_s,omitempty"`
	// Fault is a short fixed token on a guard_fault.
	Fault string `json:"fault,omitempty"`
}

// EventSink receives events. A failure never changes a verdict.
type EventSink interface{ Emit(Event) error }

// NopSink drops events.
type NopSink struct{}

// Emit drops the event.
func (NopSink) Emit(Event) error { return nil }

var eventFileRE = regexp.MustCompile(`^events-(\d{4}-\d{2}-\d{2})\.jsonl$`)

// FileSink writes one JSON line per event to events-YYYY-MM-DD.jsonl (UTC
// date) in Dir, with one O_APPEND write per event and the file closed after
// each. The first write of a day deletes files older than RetentionDays; a
// prune failure never fails the write, and there is no size cap.
type FileSink struct {
	Dir           string
	Clock         Clock
	RetentionDays int
}

// Emit appends the event.
func (s *FileSink) Emit(ev Event) error {
	now := s.Clock.Now().UTC()
	if ev.Time.IsZero() {
		ev.Time = now
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(s.Dir, "events-"+now.Format("2006-01-02")+".jsonl")
	_, statErr := os.Lstat(p)
	firstOfDay := statErr != nil
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	if firstOfDay {
		s.prune(now)
	}
	return nil
}

func (s *FileSink) prune(now time.Time) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	cutoff := today.AddDate(0, 0, -s.RetentionDays)
	for _, e := range entries {
		m := eventFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		d, err := time.Parse("2006-01-02", m[1])
		if err != nil || !d.Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(s.Dir, e.Name()))
	}
}

// Recorder turns verdicts into persisted state and events. Errors of the
// state store and the sink are returned for the caller to log, but a caller
// MUST NOT let them change a verdict.
type Recorder struct {
	Store             *Store
	Sink              EventSink
	Clock             Clock
	HeartbeatInterval time.Duration
}

// Observation is one verdict to record.
type Observation struct {
	Reason       Reason
	LastError    string // raw text; redacted before it is stored
	LoginAttempt string
}

// Record stores the verdict and emits guard_state on a transition, and as a
// heartbeat while the realm stays unavailable and the last heartbeat is older
// than HeartbeatInterval. It never emits for an available realm that stays
// available. It returns the state as stored.
func (r *Recorder) Record(realm string, obs Observation) (RealmState, error) {
	now := r.Clock.Now()
	var ev *Event
	var out RealmState
	err := r.Store.Update(realm, func(st *RealmState) error {
		wasAvailable := st.Available()
		prev := st.LastReason
		avail := obs.Reason == ReasonOK
		transition := prev != string(obs.Reason) && !(prev == "" && avail)

		st.LastReason = string(obs.Reason)
		switch {
		case avail:
			st.Since = time.Time{}
			st.LastHeartbeatAt = time.Time{}
		case wasAvailable || st.Since.IsZero():
			st.Since = now
		}
		if obs.LastError != "" {
			st.LastError = RedactTail(obs.LastError)
		}

		heartbeat := !avail && !transition &&
			(st.LastHeartbeatAt.IsZero() || now.Sub(st.LastHeartbeatAt) >= r.HeartbeatInterval)
		if transition || heartbeat || obs.LoginAttempt != "" {
			e := Event{
				Time: now, Event: EventGuardState, Realm: realm,
				Reason: string(obs.Reason), LoginAttempt: obs.LoginAttempt,
			}
			a, t := b2i(avail), b2i(transition)
			e.Available, e.Transition = &a, &t
			if !avail {
				u := int64(now.Sub(st.Since).Seconds())
				e.UnavailableSeconds = &u
				st.LastHeartbeatAt = now
			}
			ev = &e
		}
		out = *st
		return nil
	})
	if err != nil {
		return RealmState{}, err
	}
	if ev != nil {
		if err := r.Sink.Emit(*ev); err != nil {
			return out, fmt.Errorf("emit event: %w", err)
		}
	}
	return out, nil
}

// Fault emits a guard_fault. fault MUST be a short fixed token, never text
// taken from a command, a host or a user.
func (r *Recorder) Fault(realm, fault string) error {
	return r.Sink.Emit(Event{Time: r.Clock.Now(), Event: EventGuardFault, Realm: realm, Fault: fault})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
