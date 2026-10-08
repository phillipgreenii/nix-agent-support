package event_test

import (
	"encoding/json"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

func TestInstantRoundTrip(t *testing.T) {
	src := time.Date(2026, 10, 7, 13, 30, 4, 120_900_000, time.UTC)
	i := event.At(src)

	b, err := json.Marshal(i)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(b), `"2026-10-07T13:30:04.120Z"`; got != want {
		t.Errorf("encoded = %s, want %s (truncated to milliseconds, never rounded)", got, want)
	}

	var back event.Instant
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal(%s): %v", b, err)
	}
	if !back.Time().Equal(i.Time()) {
		t.Errorf("round trip = %v, want %v", back.Time(), i.Time())
	}
}

func TestInstantKeepsThreeDigitsAndConvertsToUTC(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"whole second keeps .000", time.Date(2026, 10, 7, 13, 30, 0, 0, time.UTC), "2026-10-07T13:30:00.000Z"},
		{"one millisecond keeps leading zeros", time.Date(2026, 10, 7, 13, 30, 0, 1_000_000, time.UTC), "2026-10-07T13:30:00.001Z"},
		{"sub-millisecond part is dropped", time.Date(2026, 10, 7, 13, 30, 0, 999_999, time.UTC), "2026-10-07T13:30:00.000Z"},
		{"another zone is converted", time.Date(2026, 10, 7, 9, 30, 0, 0, time.FixedZone("EDT", -4*3600)), "2026-10-07T13:30:00.000Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(event.At(c.in))
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if got := string(b); got != `"`+c.want+`"` {
				t.Errorf("encoded = %s, want %q", got, c.want)
			}
			if got := event.At(c.in).Time(); got.Location() != time.UTC {
				t.Errorf("Time() location = %v, want UTC", got.Location())
			}
		})
	}
}

func TestInstantRejectsEveryOtherForm(t *testing.T) {
	for _, in := range []string{
		`"2026-10-07T13:30:04+00:00"`,     // numeric offset instead of Z
		`"2026-10-07T13:30:04.120+01:00"`, // another zone
		`"2026-10-07T13:30:04Z"`,          // no fraction
		`"2026-10-07T13:30:04.12Z"`,       // two digits
		`"2026-10-07T13:30:04.1200Z"`,     // four digits
		`"2026-10-07T13:30:04.120z"`,      // lower case z
		`"2026-10-07 13:30:04.120Z"`,      // space instead of T
		`"2026-10-07T13:30:04.120"`,       // no zone
		`"2026-13-07T13:30:04.120Z"`,      // month 13
		`"2026-02-30T13:30:04.120Z"`,      // a day that does not exist
		`"2026-10-07T24:00:00.000Z"`,      // hour 24
		`""`,
		`null`,
		`1791379804120`,
		`true`,
	} {
		var i event.Instant
		if err := json.Unmarshal([]byte(in), &i); err == nil {
			t.Errorf("Unmarshal(%s) succeeded with %v, want an error", in, i.Time())
		}
	}
}

func TestInstantRoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ms := rapid.Int64Range(0, 253402300799999).Draw(t, "unix milliseconds") // up to 9999-12-31
		extra := rapid.IntRange(0, 999_999).Draw(t, "sub-millisecond nanoseconds")
		src := time.UnixMilli(ms).UTC().Add(time.Duration(extra))
		b, err := json.Marshal(event.At(src))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var back event.Instant
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", b, err)
		}
		if got := back.Time().UnixMilli(); got != ms {
			t.Fatalf("round trip of %s = %d ms, want %d ms", b, got, ms)
		}
		again, err := json.Marshal(back)
		if err != nil || string(again) != string(b) {
			t.Fatalf("second encoding = %s (%v), want %s", again, err, b)
		}
	})
}
