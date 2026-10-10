package wire

import (
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
)

// The reasons only the transport can find: they belong to the daemon's HTTP
// layer, not to the library's closed set (command.Reasons), and complete the
// closed set the API documents.
const (
	ReasonForbiddenHost        command.Reason = "forbidden_host"
	ReasonForbiddenOrigin      command.Reason = "forbidden_origin"
	ReasonUnsupportedMediaType command.Reason = "unsupported_media_type"
	ReasonNotFound             command.Reason = "not_found"
	ReasonMethodNotAllowed     command.Reason = "method_not_allowed"
)

// TransportReasons lists the transport reasons, in a stable order.
func TransportReasons() []command.Reason {
	return []command.Reason{
		ReasonForbiddenHost, ReasonForbiddenOrigin, ReasonUnsupportedMediaType,
		ReasonNotFound, ReasonMethodNotAllowed,
	}
}

var transportStatus = map[command.Reason]int{
	ReasonForbiddenHost: http.StatusForbidden, ReasonForbiddenOrigin: http.StatusForbidden,
	ReasonUnsupportedMediaType: http.StatusUnsupportedMediaType, ReasonNotFound: http.StatusNotFound,
	ReasonMethodNotAllowed: http.StatusMethodNotAllowed,
}

// StatusOf is the HTTP status of a reason, library or transport; 500 for a
// string that is neither.
func StatusOf(r command.Reason) int {
	if s := r.Status(); s != 0 {
		return s
	}
	if s, ok := transportStatus[r]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// ReasonInternal is the reason of a defect: an error that is not a refusal.
const ReasonInternal command.Reason = "internal_error"

// ProblemType is the `type` member of a problem for a reason: a URN, so the
// document needs no page to be dereferenced.
func ProblemType(r command.Reason) string { return "urn:pg-task-focus:problem:" + string(r) }

// Details holds the structured form of the facts a problem's detail sentence
// names.
type Details struct {
	Entity   string     `json:"entity,omitempty"`
	Events   []string   `json:"events,omitempty"`
	Instants []Instant  `json:"instants,omitempty"`
	Cycles   []CycleRef `json:"cycles,omitempty"`
	Store    *Store     `json:"store,omitempty"`
}

// Problem is an RFC 9457 application/problem+json body. Reason is a code from
// the closed set the API documents; TraceID names the trace of the request,
// for a bug report.
type Problem struct {
	Type     string   `json:"type"`
	Title    string   `json:"title"`
	Status   int      `json:"status"`
	Detail   string   `json:"detail"`
	Instance string   `json:"instance,omitempty"`
	Reason   string   `json:"reason"`
	TraceID  string   `json:"trace_id"`
	Details  *Details `json:"details,omitempty"`
	Store    *Store   `json:"store,omitempty"`
	// Health is set on the not_ready problem of /readyz: the health document
	// with the failing check named.
	Health *Health `json:"health,omitempty"`
}

// ProblemOf builds the problem of a refusal. store is the store's health when
// the refusal is store_unavailable.
func ProblemOf(r *command.Rejection, instance, traceID string, store *Store) Problem {
	p := Problem{
		Type: ProblemType(r.Reason), Title: Title(r.Reason), Status: StatusOf(r.Reason), Detail: r.Message,
		Instance: instance, Reason: string(r.Reason), TraceID: traceID,
	}
	d := Details{Entity: r.Entity}
	for _, id := range r.Events {
		d.Events = append(d.Events, string(id))
	}
	for _, t := range r.Instants {
		d.Instants = append(d.Instants, at(t))
	}
	d.Cycles = cycleRefs(r.Cycles)
	if len(d.Cycles) == 0 {
		d.Cycles = nil
	}
	if r.Reason == command.ReasonStoreUnavailable && store != nil {
		p.Store = store
		d.Store = store
	}
	if d.Entity != "" || len(d.Events) > 0 || len(d.Instants) > 0 || len(d.Cycles) > 0 || d.Store != nil {
		p.Details = &d
	}
	return p
}

// Simple builds a problem for a reason the transport raises, with a sentence.
func Simple(r command.Reason, detail, instance, traceID string) Problem {
	return Problem{
		Type: ProblemType(r), Title: Title(r), Status: StatusOf(r), Detail: detail,
		Instance: instance, Reason: string(r), TraceID: traceID,
	}
}

// Title is the short human title of a reason: its code, with spaces.
func Title(r command.Reason) string {
	words := strings.ReplaceAll(string(r), "_", " ")
	if words == "" {
		return ""
	}
	rs := []rune(words)
	rs[0] = unicode.ToUpper(rs[0])
	return string(rs)
}

// AsRejection returns the refusal an error is, if it is one.
func AsRejection(err error) (*command.Rejection, bool) {
	var r *command.Rejection
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}
