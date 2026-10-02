package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxResultBytes is the cap on a handler's stdout (1 MiB). The caller that
// reads the handler's stdout enforces it (see LimitedBuffer) and tells
// Classify whether anything was cut off.
const MaxResultBytes = 1 << 20

// startsLen is how many runes of stdout a "starts ..." excerpt in a reason
// shows before it is cut with an ellipsis.
const startsLen = 40

// Reported holds a handler's own claims: the optional fields of its result
// JSON. They are claims, not facts; the wrapper's facts live beside them in
// the report. DetailsFile is filled in by the wrapper when it spills long
// details to a file; a handler never sets it.
type Reported struct {
	Summary     string `json:"summary,omitempty"`
	Details     string `json:"details,omitempty"`
	DetailsFile string `json:"details_file,omitempty"`
	// Meta is the handler's "meta" object kept verbatim, byte for byte.
	Meta json.RawMessage `json:"meta,omitempty"`
}

// Classify turns a handler's exit code and captured stdout into the outcome
// the wrapper records, a reason, and the handler's claims. It is the only
// implementation of the result rules: the wrapper and every handler test use
// it.
//
// The exit code decides the outcome (0 resolved, 2 declined, 3 deferred,
// anything else failed). Stdout is optional: empty means the exit code alone
// speaks; otherwise it MUST be exactly one JSON object, optionally followed
// by whitespace, with no duplicate keys. Of its members, "outcome" (a string
// naming resolved, deferred or declined, and agreeing with the exit code),
// "summary" and "details" (strings) and "meta" (an object) are checked;
// unknown members are ignored. Any violation makes the attempt failed, with a
// specific reason. truncated says the caller cut stdout off at
// MaxResultBytes, which is itself a violation.
//
// When the exit code is outside the table the outcome is failed regardless,
// but a valid stdout object still contributes summary, details and meta, and
// a present "outcome" is reported in the reason as a disagreement. An invalid
// stdout is then ignored, with the problem appended to the reason.
func Classify(exit int, stdout []byte, truncated bool) (outcome Outcome, reason string, reported Reported) {
	fromExit := OutcomeForExit(exit)
	rep, claimed, problem := parseResult(stdout, truncated)

	if fromExit == Failed {
		reason = fmt.Sprintf("exit %d", exit)
		switch {
		case problem != "":
			reason += "; stdout ignored: " + problem
		case claimed != "":
			reason += fmt.Sprintf("; stdout outcome %q disagrees with exit code %d", claimed, exit)
		}
		return Failed, reason, rep
	}
	if problem != "" {
		return Failed, problem, Reported{}
	}
	if claimed != "" && claimed != fromExit {
		return Failed, fmt.Sprintf("stdout outcome %q disagrees with exit code %d (%s)", claimed, exit, fromExit), rep
	}
	return fromExit, fmt.Sprintf("exit %d", exit), rep
}

// parseResult checks stdout against the result-JSON rules. problem is empty
// when stdout is acceptable (including empty). claimed is the outcome named in
// the JSON, if any. rep is only filled when stdout parsed and type-checked.
func parseResult(stdout []byte, truncated bool) (rep Reported, claimed Outcome, problem string) {
	if truncated {
		return Reported{}, "", fmt.Sprintf("stdout is larger than %d bytes (1 MiB cap) and was cut off", MaxResultBytes)
	}
	if len(bytes.Trim(stdout, " \t\r\n")) == 0 {
		return Reported{}, "", ""
	}
	members, err := ParseObject(stdout)
	if err != nil {
		return Reported{}, "", describeParseError(err, stdout)
	}

	if raw, ok := members["outcome"]; ok {
		var name string
		if kind(raw) != "a string" || json.Unmarshal(raw, &name) != nil {
			return Reported{}, "", typeProblem("outcome", "a string", raw)
		}
		o, err := ParseReportable(name)
		if err != nil {
			return Reported{}, "", "stdout JSON: " + err.Error()
		}
		claimed = o
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{"summary", &rep.Summary}, {"details", &rep.Details}} {
		raw, ok := members[f.key]
		if !ok {
			continue
		}
		// json.Unmarshal accepts null into a string without complaint, so the
		// kind is checked first: null is the wrong type, not "absent".
		if kind(raw) != "a string" || json.Unmarshal(raw, f.dst) != nil {
			return Reported{}, "", typeProblem(f.key, "a string", raw)
		}
	}
	if raw, ok := members["meta"]; ok {
		if kind(raw) != "an object" {
			return Reported{}, "", typeProblem("meta", "an object", raw)
		}
		rep.Meta = append(json.RawMessage(nil), raw...)
	}
	return rep, claimed, ""
}

// describeParseError words a ParseObject failure for the reason string.
func describeParseError(err error, stdout []byte) string {
	var dup *DuplicateKeyError
	switch {
	case errors.As(err, &dup):
		return fmt.Sprintf("stdout JSON has a duplicate key %q", dup.Key)
	case errors.Is(err, ErrNotObject):
		return fmt.Sprintf("stdout is JSON but not an object (starts %s); handlers must print one object", excerpt(stdout))
	case errors.Is(err, ErrTrailingData):
		return fmt.Sprintf("stdout has data after the JSON object (starts %s); handlers must print exactly one object and log to stderr", excerpt(stdout))
	default:
		return fmt.Sprintf("stdout is not JSON (starts %s); handlers must log to stderr", excerpt(stdout))
	}
}

// typeProblem words a wrong-field-type failure.
func typeProblem(field, want string, raw json.RawMessage) string {
	return fmt.Sprintf("stdout JSON: field %q must be %s, not %s", field, want, kind(raw))
}

// excerpt quotes the first startsLen runes of stdout (surrounding whitespace
// skipped), ending the quoted text with an ellipsis when it was cut. It walks
// only as far as it needs, so a 1 MiB stdout is not converted wholesale.
func excerpt(stdout []byte) string {
	b := bytes.Trim(stdout, " \t\r\n")
	end := 0
	for n := 0; n < startsLen && end < len(b); n++ {
		_, size := utf8.DecodeRune(b[end:])
		end += size
	}
	text := strings.ToValidUTF8(string(b[:end]), "\uFFFD")
	if end < len(b) {
		text += "…"
	}
	return strconv.Quote(text)
}

// kind names the JSON type of a raw value.
func kind(raw json.RawMessage) string {
	b := bytes.TrimLeft(raw, " \t\r\n")
	if len(b) == 0 {
		return "empty"
	}
	switch b[0] {
	case '"':
		return "a string"
	case '{':
		return "an object"
	case '[':
		return "an array"
	case 't', 'f':
		return "a boolean"
	case 'n':
		return "null"
	default:
		return "a number"
	}
}
