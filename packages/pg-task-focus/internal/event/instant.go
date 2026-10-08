// Package event defines the event envelope, the payload of every event type,
// the canonical one-line encoding of an event, and the request hash that makes
// a repeated request recognizable. It has no IO: the store reads and writes
// lines, the projection reads events.
package event

import (
	"encoding/json"
	"fmt"
	"time"
)

// instantLayout is the only form an instant takes on the wire: UTC, with
// exactly three fractional digits and a literal Z.
const instantLayout = "2006-01-02T15:04:05.000Z"

// Instant is a moment on the wire. Its JSON form is always
// "2026-10-07T13:30:04.120Z": UTC, millisecond precision, three digits even
// when they are zero.
type Instant time.Time

// At returns t as an Instant: in UTC and truncated (never rounded) to the
// millisecond.
func At(t time.Time) Instant {
	return Instant(t.UTC().Truncate(time.Millisecond))
}

// Time returns the instant as a time.Time in UTC.
func (i Instant) Time() time.Time { return time.Time(i).UTC() }

// MarshalJSON writes the fixed wire form. Anything finer than a millisecond is
// dropped, so an Instant built by conversion encodes like one built by At.
func (i Instant) MarshalJSON() ([]byte, error) {
	t := i.Time().Truncate(time.Millisecond)
	if y := t.Year(); y < 0 || y > 9999 {
		return nil, fmt.Errorf("instant %v is outside the years 0000 to 9999", t)
	}
	return []byte(`"` + t.Format(instantLayout) + `"`), nil
}

// UnmarshalJSON reads only the wire form: a JSON string of the shape
// "2006-01-02T15:04:05.000Z". A numeric offset, a missing or longer fraction
// and a lower-case z are errors.
func (i *Instant) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("instant must be a JSON string such as 2026-10-07T13:30:04.120Z: %w", err)
	}
	t, err := time.Parse(instantLayout, s)
	if err != nil {
		return fmt.Errorf("instant %q is not in the form 2026-10-07T13:30:04.120Z (UTC, three fractional digits, Z): %w", s, err)
	}
	*i = Instant(t.UTC())
	return nil
}
