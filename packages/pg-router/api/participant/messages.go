package participant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Role is the request's role object: {"name": ..., "config": {...}}. Config is
// always present on the wire, {} when the role has no [role.config].
type Role struct {
	Name   string         `json:"name"`
	Config map[string]any `json:"config"`
}

// DispatchEvent is the event member of a DispatchRequest. ID is the event id
// of the queue event being dispatched.
type DispatchEvent struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	ExpiresAt string         `json:"expiresAt,omitempty"`
	Payload   map[string]any `json:"payload"`
}

// DispatchRequest is the handler.dispatch message (core to handler). ID is the
// dispatch id the core minted for this call; Event.ID is the event id. They are
// the two ids a handler's start and end records carry as dispatch_id and
// event_id.
type DispatchRequest struct {
	SchemaVersion string        `json:"schemaVersion"`
	ID            string        `json:"id"`
	Event         DispatchEvent `json:"event"`
	// Role is the role object for the role's own handler; omitted only for a
	// role built in code without one.
	Role *Role `json:"role,omitempty"`
}

// DispatchID is the id of this dispatch call.
func (r DispatchRequest) DispatchID() string { return r.ID }

// EventID is the id of the event being dispatched.
func (r DispatchRequest) EventID() string { return r.Event.ID }

// LifecycleRequest is the handler.postStartup and handler.preShutdown message;
// both hooks share one wire shape (no event, no deferred branch).
type LifecycleRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Role          *Role  `json:"role,omitempty"`
}

// LifecycleReply is the reply of a lifecycle hook.
type LifecycleReply struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Outcome       string `json:"outcome,omitempty"`
	Error         string `json:"error,omitempty"`
}

// DispatchReply is the handler.dispatch-reply message: an inline outcome, a
// deferred ack, or an error. ID echoes the request's dispatch id.
//
// StartedAt, Session and Supervisors are OPTIONAL and advisory. They stay raw on
// the wire type so a value of the wrong JSON type can be ignored by the reading
// side (ReportedStart, ReportedSupervision) instead of failing a dispatch that
// worked; a producer sets them with SetStartedAt and SetSupervision.
type DispatchReply struct {
	SchemaVersion string          `json:"schemaVersion"`
	ID            string          `json:"id"`
	Deferred      bool            `json:"deferred,omitempty"`
	Outcome       string          `json:"outcome,omitempty"`
	Skipped       string          `json:"skipped,omitempty"`
	Error         string          `json:"error,omitempty"`
	StartedAt     json.RawMessage `json:"startedAt,omitempty"`
	Session       json.RawMessage `json:"session,omitempty"`
	Supervisors   json.RawMessage `json:"supervisors,omitempty"`
}

// SetStartedAt records the launch time of a session this dispatch adopted, as an
// RFC 3339 UTC instant. A zero time leaves the field absent.
func (r *DispatchReply) SetStartedAt(t time.Time) {
	if t.IsZero() {
		return
	}
	b, _ := json.Marshal(t.UTC().Format(time.RFC3339))
	r.StartedAt = b
}

// SetSupervision records the session a lease takeover absorbed and the
// supervisor count that takeover returned. Both are set only together, and only
// for a non-empty session and supervisors of at least 1; otherwise the reply is
// left as it is.
func (r *DispatchReply) SetSupervision(session string, supervisors int) {
	if session == "" || supervisors < 1 {
		return
	}
	r.Session, _ = json.Marshal(session)
	r.Supervisors = json.RawMessage(strconv.Itoa(supervisors))
}

// ReportedStart decodes the optional startedAt. Absent or null is the zero time
// with a nil error; a value that is not a JSON string holding an RFC 3339
// instant is the zero time with an error the caller may log.
func (r DispatchReply) ReportedStart() (time.Time, error) {
	raw := r.StartedAt
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, fmt.Errorf("startedAt is not a string: %s", raw)
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("startedAt is not an RFC 3339 instant: %w", err)
	}
	return t, nil
}

// ReportedSupervision vets the optional session and supervisors: both must be
// present, session a non-empty JSON string and supervisors a JSON integer of at
// least 1. Anything else yields ("", 0).
func (r DispatchReply) ReportedSupervision() (string, int) {
	if len(r.Session) == 0 || len(r.Supervisors) == 0 {
		return "", 0
	}
	var session string
	if err := json.Unmarshal(r.Session, &session); err != nil || session == "" {
		return "", 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(r.Supervisors)), 10, 32)
	if err != nil || n < 1 {
		return "", 0
	}
	return session, int(n)
}

// Decline kinds of a pre-accept decline body: what the core's progress gauges
// count. busy is routine backpressure, fault is a condition worth an operator's
// look, and neutral is a decline that says nothing about the dispatch's health.
const (
	DeclineKindBusy    = "busy"
	DeclineKindFault   = "fault"
	DeclineKindNeutral = "neutral"
)

// DeclineReply is the pre-accept decline body a participant MAY send with
// ExitBusy: a required reason tag, an optional kind (absent means busy) and an
// optional bounded, redacted detail.
type DeclineReply struct {
	SchemaVersion string `json:"schemaVersion"`
	Reason        string `json:"reason"`
	Kind          string `json:"kind,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// DecodeDecline decodes the optional ExitBusy body LENIENTLY: a body that is
// absent, empty, not JSON or not an object yields the zero value, and each of
// reason, kind and detail is decoded on its own, so a field of the wrong JSON
// type is dropped without losing the others. Unknown fields are ignored.
func DecodeDecline(stdout []byte) DeclineReply {
	var fields map[string]json.RawMessage
	if len(stdout) == 0 || json.Unmarshal(stdout, &fields) != nil {
		return DeclineReply{}
	}
	str := func(key string) string {
		var v string
		if raw, ok := fields[key]; ok && json.Unmarshal(raw, &v) == nil {
			return v
		}
		return ""
	}
	return DeclineReply{Reason: str("reason"), Kind: str("kind"), Detail: str("detail")}
}

// DecodeDispatchReply decodes a dispatch reply body. Unknown properties are
// ignored, as a participant may add fields a core does not read yet.
func DecodeDispatchReply(data []byte) (DispatchReply, error) {
	var r DispatchReply
	if err := json.Unmarshal(data, &r); err != nil {
		return DispatchReply{}, err
	}
	return r, nil
}

// DecodeDispatchRequest strictly decodes a handler.dispatch body: an unknown
// property is an error.
func DecodeDispatchRequest(data []byte) (DispatchRequest, error) {
	var r DispatchRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return DispatchRequest{}, fmt.Errorf("decode dispatch request: %w", err)
	}
	return r, nil
}
