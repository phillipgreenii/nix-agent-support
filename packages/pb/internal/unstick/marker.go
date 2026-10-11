package unstick

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Sweep marker grammar, the single definition shared by `pb unstick marker`,
// `prepare` and the worker docs (drift-tested):
//
//	[unstick YYYY-MM-DDTHH:MM:SSZ] <outcome>: <reason>; recheck-when: <recheck>
//
// where outcome matches [a-z][a-z-]*, reason is plain text (see
// ValidateReason) and recheck is YYYY-MM-DD, "<bead-id> closes" or "on-change".

// Well-known outcomes. Any other [a-z][a-z-]* outcome is also well-formed.
const (
	OutcomeUnchanged = "unchanged"
	OutcomeReleased  = "released"
)

const (
	markerPrefix  = "[unstick"
	recheckKey    = "; recheck-when: "
	markerTimeFmt = "2006-01-02T15:04:05Z"
	dateFmt       = "2006-01-02"
)

var (
	outcomeRE = regexp.MustCompile(`^[a-z][a-z-]*$`)
	beadIDRE  = regexp.MustCompile(`^[A-Za-z0-9]+(?:[-.][A-Za-z0-9]+)*$`)
)

// RecheckKind discriminates the three recheck-when forms.
type RecheckKind string

const (
	// RecheckDate means "not due until this YYYY-MM-DD".
	RecheckDate RecheckKind = "date"
	// RecheckCloses means "not due until BeadID is closed".
	RecheckCloses RecheckKind = "closes"
	// RecheckOnChange means "due only when the bead's surroundings change".
	RecheckOnChange RecheckKind = "on-change"
)

// Recheck is a parsed recheck-when clause.
type Recheck struct {
	Kind   RecheckKind
	Date   time.Time // RecheckDate: midnight UTC of the date
	BeadID string    // RecheckCloses
}

// ParseRecheck parses "YYYY-MM-DD", "<bead-id> closes" or "on-change".
func ParseRecheck(s string) (Recheck, error) {
	if s == "on-change" {
		return Recheck{Kind: RecheckOnChange}, nil
	}
	if id, ok := strings.CutSuffix(s, " closes"); ok {
		if !beadIDRE.MatchString(id) {
			return Recheck{}, fmt.Errorf("recheck-when %q: %q is not a bead id", s, id)
		}
		return Recheck{Kind: RecheckCloses, BeadID: id}, nil
	}
	if d, err := time.Parse(dateFmt, s); err == nil {
		return Recheck{Kind: RecheckDate, Date: d.UTC()}, nil
	}
	return Recheck{}, fmt.Errorf("recheck-when %q: want YYYY-MM-DD, \"<bead-id> closes\" or \"on-change\"", s)
}

// String renders the clause in marker form.
func (r Recheck) String() string {
	switch r.Kind {
	case RecheckDate:
		return r.Date.UTC().Format(dateFmt)
	case RecheckCloses:
		return r.BeadID + " closes"
	default:
		return "on-change"
	}
}

// Marker is a parsed (or to-be-rendered) sweep marker.
type Marker struct {
	Time    time.Time // UTC, second precision
	Outcome string
	Reason  string
	Recheck Recheck
	// Source is where a scanned marker was found: "notes" or "comment"; empty
	// for markers built by NewMarker or ParseMarker.
	Source string
}

// String renders the marker as its single canonical line.
func (m Marker) String() string {
	return fmt.Sprintf("[unstick %s] %s: %s%s%s",
		m.Time.UTC().Format(markerTimeFmt), m.Outcome, m.Reason, recheckKey, m.Recheck)
}

// ValidateOutcome checks outcome against [a-z][a-z-]*.
func ValidateOutcome(outcome string) error {
	if !outcomeRE.MatchString(outcome) {
		return fmt.Errorf("outcome %q must match [a-z][a-z-]*", outcome)
	}
	return nil
}

// ValidateReason rejects a reason that would break the plain-text grammar or
// shell quoting when the marker is passed to `bd update --append-notes`:
// empty/blank, control characters (newline included), backtick, $, single or
// double quote, and the substring "; recheck-when:".
func ValidateReason(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("reason must not be empty")
	}
	if !utf8.ValidString(reason) {
		return errors.New("reason must be valid UTF-8")
	}
	if reason != strings.TrimSpace(reason) {
		return errors.New("reason must not have leading or trailing whitespace")
	}
	for _, c := range reason {
		switch {
		case c < 0x20 || c == 0x7f:
			return fmt.Errorf("reason must not contain control characters (found %U)", c)
		case c == '`' || c == '$' || c == '\'' || c == '"':
			return fmt.Errorf("reason must be plain text; found %q", c)
		}
	}
	if strings.Contains(reason, "; recheck-when:") {
		return errors.New(`reason must not contain "; recheck-when:"`)
	}
	return nil
}

// NewMarker validates the parts and builds a marker stamped at now (UTC,
// truncated to whole seconds). recheck uses the ParseRecheck forms.
func NewMarker(now time.Time, outcome, reason, recheck string) (Marker, error) {
	if err := ValidateOutcome(outcome); err != nil {
		return Marker{}, err
	}
	if err := ValidateReason(reason); err != nil {
		return Marker{}, err
	}
	rc, err := ParseRecheck(recheck)
	if err != nil {
		return Marker{}, err
	}
	return Marker{Time: now.UTC().Truncate(time.Second), Outcome: outcome, Reason: reason, Recheck: rc}, nil
}

// ParseMarker parses one marker line strictly. Surrounding whitespace is
// trimmed; anything not matching the grammar (date-only or non-Z timestamp,
// missing recheck-when, bad outcome, unsanitary reason) is an error.
func ParseMarker(line string) (Marker, error) {
	s := strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(s, markerPrefix+" ")
	if !ok {
		return Marker{}, fmt.Errorf("not a marker: missing %q prefix", markerPrefix+" ")
	}
	tsStr, rest, ok := strings.Cut(rest, "] ")
	if !ok {
		return Marker{}, errors.New("marker: missing \"] \" after timestamp")
	}
	ts, err := time.Parse(markerTimeFmt, tsStr)
	if err != nil || len(tsStr) != len(markerTimeFmt) {
		return Marker{}, fmt.Errorf("marker timestamp %q must be YYYY-MM-DDTHH:MM:SSZ", tsStr)
	}
	outcome, rest, ok := strings.Cut(rest, ": ")
	if !ok {
		return Marker{}, errors.New("marker: missing \": \" after outcome")
	}
	if err := ValidateOutcome(outcome); err != nil {
		return Marker{}, err
	}
	i := strings.LastIndex(rest, recheckKey)
	if i < 0 {
		return Marker{}, errors.New(`marker: missing "; recheck-when: "`)
	}
	reason, rc := rest[:i], rest[i+len(recheckKey):]
	if err := ValidateReason(reason); err != nil {
		return Marker{}, err
	}
	recheck, err := ParseRecheck(rc)
	if err != nil {
		return Marker{}, err
	}
	return Marker{Time: ts.UTC(), Outcome: outcome, Reason: reason, Recheck: recheck}, nil
}

// MalformedMarker is a marker-looking line that failed ParseMarker.
type MalformedMarker struct {
	Source string
	Line   string
	Err    error
}

// MarkerScan is the result of scanning free text for sweep markers.
type MarkerScan struct {
	Valid     []Marker
	Malformed []MalformedMarker
}

// ScanMarkers scans text line by line. A line whose trimmed form starts with
// "[unstick" is a marker candidate: well-formed ones go to Valid, the rest to
// Malformed. source labels where the text came from.
func ScanMarkers(text, source string) MarkerScan {
	var out MarkerScan
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, markerPrefix) {
			continue
		}
		m, err := ParseMarker(l)
		if err != nil {
			out.Malformed = append(out.Malformed, MalformedMarker{Source: source, Line: l, Err: err})
			continue
		}
		m.Source = source
		out.Valid = append(out.Valid, m)
	}
	return out
}

// Markers scans the row's notes and every comment text.
func (r Row) Markers() MarkerScan {
	out := ScanMarkers(r.Notes, "notes")
	for _, c := range r.Comments {
		s := ScanMarkers(c.Text, "comment")
		out.Valid = append(out.Valid, s.Valid...)
		out.Malformed = append(out.Malformed, s.Malformed...)
	}
	return out
}

// Newest returns the marker with the greatest parsed Time (the later one in
// scan order wins a tie) and false when ms is empty.
func Newest(ms []Marker) (Marker, bool) {
	if len(ms) == 0 {
		return Marker{}, false
	}
	best := ms[0]
	for _, m := range ms[1:] {
		if !m.Time.Before(best.Time) {
			best = m
		}
	}
	return best, true
}
