// Package contract holds the handler contract's vocabulary: the outcomes a
// handler can report, their exit codes, and the optional result JSON a handler
// prints on stdout. Both the wrapper and the handler binaries import it, so
// there is exactly one definition of the mapping.
package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Outcome is the verdict of one handler attempt.
type Outcome string

const (
	// Resolved means the handler claims it fixed the failure (stop, then verify).
	Resolved Outcome = "resolved"
	// Deferred means the handler handed the work off (stop).
	Deferred Outcome = "deferred"
	// Declined means the handler chose not to act (continue).
	Declined Outcome = "declined"
	// Failed is assigned by the wrapper only; a handler cannot report it.
	Failed Outcome = "failed"
)

// Handler exit codes that map to an outcome. Anything else is Failed.
const (
	ExitResolved = 0
	ExitDeclined = 2
	ExitDeferred = 3
)

// Reportable lists the outcomes a handler may name, in documentation order.
var Reportable = []Outcome{Resolved, Deferred, Declined}

// ParseReportable converts a name to a handler-reportable outcome. The error
// lists the valid names.
func ParseReportable(s string) (Outcome, error) {
	for _, o := range Reportable {
		if string(o) == s {
			return o, nil
		}
	}
	names := make([]string, len(Reportable))
	for i, o := range Reportable {
		names[i] = string(o)
	}
	return "", fmt.Errorf("unknown outcome %q; valid: %s", s, strings.Join(names, ", "))
}

// ExitCode returns the exit code a handler uses to report o. ok is false for
// Failed (and anything unknown), which a handler cannot report.
func (o Outcome) ExitCode() (code int, ok bool) {
	switch o {
	case Resolved:
		return ExitResolved, true
	case Declined:
		return ExitDeclined, true
	case Deferred:
		return ExitDeferred, true
	default:
		return 0, false
	}
}

// OutcomeForExit maps a handler exit code to its outcome; any code outside
// the table is Failed.
func OutcomeForExit(code int) Outcome {
	switch code {
	case ExitResolved:
		return Resolved
	case ExitDeclined:
		return Declined
	case ExitDeferred:
		return Deferred
	default:
		return Failed
	}
}

// Result is the optional JSON object a handler prints on stdout. Every field
// is optional on the wire; Outcome is always set by Render.
type Result struct {
	Outcome Outcome         `json:"outcome"`
	Summary string          `json:"summary,omitempty"`
	Details string          `json:"details,omitempty"`
	Meta    json.RawMessage `json:"meta,omitempty"`
}

// Render returns the result as one compact JSON object followed by a newline.
func (r Result) Render() ([]byte, error) {
	if _, ok := r.Outcome.ExitCode(); !ok {
		return nil, fmt.Errorf("outcome %q cannot be reported by a handler", r.Outcome)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Sentinel errors ParseObject wraps, so a caller can tell the failure kinds
// apart (Classify words its reasons from them).
var (
	// ErrNotJSON means the input is not JSON at all (or ends mid-value).
	ErrNotJSON = errors.New("not JSON")
	// ErrNotObject means the input is valid JSON but its top-level value is
	// not an object.
	ErrNotObject = errors.New("not a JSON object")
	// ErrTrailingData means something other than whitespace follows the
	// object (a second object, junk).
	ErrTrailingData = errors.New("trailing data after the JSON object")
)

// DuplicateKeyError reports an object member name that appears twice in one
// object, at any depth.
type DuplicateKeyError struct{ Key string }

func (e *DuplicateKeyError) Error() string { return fmt.Sprintf("duplicate key %q", e.Key) }

// ParseObject strictly parses data as exactly one JSON object, optionally
// followed by whitespace. Duplicate keys (at any depth) are rejected. It
// returns the object's top-level members as raw JSON, byte-for-byte as they
// appeared. Errors wrap ErrNotJSON, ErrNotObject, ErrTrailingData or are a
// *DuplicateKeyError.
func ParseObject(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotJSON, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, ErrNotObject
	}
	if err := checkNoDuplicates(dec, '{'); err != nil {
		var dup *DuplicateKeyError
		if errors.As(err, &dup) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrNotJSON, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrTrailingData
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotJSON, err)
	}
	return out, nil
}

// checkNoDuplicates consumes tokens up to and including the delimiter that
// closes the container whose opening delimiter open was just read.
func checkNoDuplicates(dec *json.Decoder, open json.Delim) error {
	seen := map[string]bool{}
	for dec.More() {
		if open == '{' {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			if seen[key] {
				return &DuplicateKeyError{Key: key}
			}
			seen[key] = true
		}
		vt, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := vt.(json.Delim); ok && (d == '{' || d == '[') {
			if err := checkNoDuplicates(dec, d); err != nil {
				return err
			}
		}
	}
	_, err := dec.Token() // closing delimiter
	return err
}
