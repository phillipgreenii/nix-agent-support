package claudetranscript

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// limitResetRe captures the rate-limit reset clause. Variations seen in real
// transcripts:
//
//	resets 3:30pm (America/New_York)        — H:MM clock + TZ
//	resets 1pm (America/New_York)           — bare hour + TZ
//	resets Apr 13, 11am (America/New_York)  — month + day + clock + TZ (weekly limit)
//	resets Apr 13, 11:30am (UTC)            — month + day + H:MM + TZ
//
// Capture groups: 1=month-abbr (opt), 2=day (opt), 3=hour, 4=minute (opt),
// 5=am|pm, 6=IANA TZ.
var limitResetRe = regexp.MustCompile(`resets\s+(?:([A-Z][a-z]{2})\s+(\d{1,2}),\s+)?(\d{1,2})(?::(\d{2}))?(am|pm)\s+\(([^)]+)\)`)

var monthAbbrev = map[string]time.Month{
	"Jan": time.January, "Feb": time.February, "Mar": time.March, "Apr": time.April,
	"May": time.May, "Jun": time.June, "Jul": time.July, "Aug": time.August,
	"Sep": time.September, "Oct": time.October, "Nov": time.November, "Dec": time.December,
}

// MaxResetHorizon bounds how far past its event time an ingested rate-limit reset
// instant may fall before it is rejected as upstream-malformed. It is one WEEKLY
// usage window — the longest window Claude reports (the other is the 5h block;
// see ADR 0021's "Source 5h/7d from the status-line rate_limits, via a sibling
// capture file"). A reset further out than the longest window cannot be the reset
// of the window the event hit, so it is garbage rather than a window.
//
// The bound is applied at INGESTION — inside the parse/resolve helpers below,
// once per shape — rather than at each consumer, and an out-of-range instant is
// DISCARDED (reported as (zero, false), the existing "no parseable reset"
// sentinel), never clamped to the horizon. Clamping would fabricate a
// plausible-looking window no message ever stated; discarding lets each consumer
// take its unknown-reset path (in pa-monitor that is the nudger's limit-pause
// producer, which exists precisely for a rate-limit with no parseable reset).
//
// There is deliberately NO lower bound here: every reset this package produces is
// strictly after its event time by construction — parseLimitResetText rolls the
// candidate forward until it is, and RetryInMsResetsAt requires a positive delay.
// Discarding a reset already in the past relative to NOW is a separate, distinct
// concern owned by the consumer (pa-monitor's poller drops one past its
// stale-pause grace).
//
// Kept in sync with pa-monitor's internal/core/limits sevenDayWindow, which
// bounds the same class of value on the account-global status-line path; that
// package's horizon pin test asserts the two are equal.
const MaxResetHorizon = 7 * 24 * time.Hour

// MaxRetryInMs is MaxResetHorizon expressed in the legacy retryInMs unit
// (milliseconds). The raw number MUST be bounded BEFORE it becomes a
// time.Duration: retryInMs is an unbounded int64 and
// time.Duration(retryInMs)*time.Millisecond overflows past ~292 years, wrapping a
// garbage-HIGH value into a NEGATIVE duration that would then read as a plausible
// instant in the PAST.
const MaxRetryInMs = int64(MaxResetHorizon / time.Millisecond)

// ResetWithinHorizon reports whether resetsAt is an acceptable reset instant for
// an event observed at eventTime: non-zero and at most MaxResetHorizon past it. A
// zero resetsAt is the unknown sentinel rather than an instant, so it is never
// acceptable.
func ResetWithinHorizon(resetsAt, eventTime time.Time) bool {
	if resetsAt.IsZero() {
		return false
	}
	return !resetsAt.After(eventTime.Add(MaxResetHorizon))
}

// RetryInMsResetsAt resolves the legacy `retryInMs` rate-limit shape to an
// absolute reset instant (eventTime + retryInMs), bounded by MaxResetHorizon. It
// is the single ingestion point for that numeric path and is shared with
// pa-monitor's single-pass transcript scanner, so the two cannot bound it
// differently. Returns (zero, false) when retryInMs is not a usable delay
// (non-positive) or exceeds the horizon.
func RetryInMsResetsAt(eventTime time.Time, retryInMs int64) (time.Time, bool) {
	if retryInMs <= 0 || retryInMs > MaxRetryInMs {
		return time.Time{}, false
	}
	return eventTime.Add(time.Duration(retryInMs) * time.Millisecond), true
}

// parseLimitResetText resolves the next occurrence of the clock time + IANA TZ
// in the message strictly after eventTime (the next reset window is always in
// the future). When the message includes a month + day prefix (weekly-limit
// shape), the reset time is anchored to that calendar date; rollover is by
// year. When omitted, rollover is by 24 hours. Returns (zero, false) on any
// parse failure (bad clock time, unknown TZ, regex miss) or when the resolved
// instant falls beyond MaxResetHorizon.
func parseLimitResetText(text string, eventTime time.Time) (time.Time, bool) {
	m := limitResetRe.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	monthStr, dayStr, hourStr, minStr, ampm, tzStr := m[1], m[2], m[3], m[4], m[5], m[6]

	hour, err := strconv.Atoi(hourStr)
	if err != nil || hour < 1 || hour > 12 {
		return time.Time{}, false
	}
	minute := 0
	if minStr != "" {
		minute, err = strconv.Atoi(minStr)
		if err != nil || minute < 0 || minute > 59 {
			return time.Time{}, false
		}
	}
	switch strings.ToLower(ampm) {
	case "am":
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour != 12 {
			hour += 12
		}
	}
	loc, err := time.LoadLocation(tzStr)
	if err != nil {
		return time.Time{}, false
	}
	evLocal := eventTime.In(loc)

	var candidate time.Time
	if monthStr != "" {
		month, ok := monthAbbrev[monthStr]
		if !ok {
			return time.Time{}, false
		}
		day, err := strconv.Atoi(dayStr)
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}
		candidate = time.Date(evLocal.Year(), month, day, hour, minute, 0, 0, loc)
		if !candidate.After(eventTime) {
			candidate = candidate.AddDate(1, 0, 0)
		}
	} else {
		candidate = time.Date(evLocal.Year(), evLocal.Month(), evLocal.Day(), hour, minute, 0, 0, loc)
		if !candidate.After(eventTime) {
			candidate = candidate.Add(24 * time.Hour)
		}
	}
	// Upper bound at ingestion — the single exit both shapes pass through. The
	// month+day shape carries a YEAR-LESS calendar date, so a date that already
	// passed in the event's year rolls forward a whole YEAR; that rollover is
	// legitimate only across a year boundary (days out) and is otherwise the
	// garbage-HIGH shape this bound exists to reject. The clock-only shape is
	// already within 24h by construction, so the check is a no-op there — kept on
	// the shared exit so the invariant cannot be lost to a later edit.
	if !ResetWithinHorizon(candidate, eventTime) {
		return time.Time{}, false
	}
	return candidate.UTC(), true
}

// ParseRateLimitReset is the exported form of parseLimitResetText: it resolves
// the rate-limit reset time encoded in a synthetic rate-limit message text,
// relative to the event time. Consumers that parse rate-limit reset windows
// outside RateLimitPause (e.g. a single-pass transcript scanner) use this so
// the parsing logic stays a single source of truth. Returns (zero, false) on
// any parse failure.
func ParseRateLimitReset(text string, eventTime time.Time) (time.Time, bool) {
	return parseLimitResetText(text, eventTime)
}

// RateLimitPause returns the time the usage window reopens when the transcript's
// most recent rate-limit event has no subsequent (non-synthetic) user/assistant
// event. Two event shapes are recognized:
//   - system/api_error/rate_limit_error with retryInMs (legacy).
//   - synthetic-assistant with error="rate_limit"+isApiErrorMessage; reset time
//     parsed from the message text via parseLimitResetText.
//
// Reset times derive from event.Timestamp (legacy: + retryInMs; synthetic: from
// text), never time.Now() — so the calculation is correct even when the event
// was written hours before the TUI started. Both shapes are bounded by
// MaxResetHorizon at ingestion; an out-of-range event does NOT become the last
// rate-limit event, so it cannot supersede an earlier in-range one.
func RateLimitPause(path string) (resetsAt time.Time, err error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = f.Close() }()

	type rateLimitScan struct {
		Type      string    `json:"type"`
		Subtype   string    `json:"subtype"`
		Timestamp time.Time `json:"timestamp"`
		RetryInMs int64     `json:"retryInMs"`
		Error     struct {
			Error struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			} `json:"error"`
		} `json:"error"`
	}
	type syntheticScan struct {
		Type              string    `json:"type"`
		Timestamp         time.Time `json:"timestamp"`
		Error             string    `json:"error"`
		IsApiErrorMessage bool      `json:"isApiErrorMessage"`
		Message           struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	type typeOnly struct {
		Type string `json:"type"`
	}

	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		b := make([]byte, len(sc.Bytes()))
		copy(b, sc.Bytes())
		lines = append(lines, b)
	}
	if sc.Err() != nil {
		return time.Time{}, sc.Err()
	}

	// Find index of last rate-limit event (either shape) and compute its absolute reset time.
	lastIdx := -1
	var lastResetsAt time.Time
	for i, line := range lines {
		// Old shape: system/api_error/rate_limit_error/retryInMs.
		var ev rateLimitScan
		if err := json.Unmarshal(line, &ev); err == nil &&
			ev.Type == "system" && ev.Subtype == "api_error" &&
			ev.Error.Error.Error.Type == "rate_limit_error" && ev.RetryInMs > 0 {
			// Bounded resolve, mirroring the synthetic shape below: an
			// out-of-horizon delay leaves lastIdx/lastResetsAt untouched.
			if t, ok := RetryInMsResetsAt(ev.Timestamp, ev.RetryInMs); ok {
				lastIdx = i
				lastResetsAt = t
			}
			continue
		}
		// New synthetic-assistant shape: error="rate_limit" + isApiErrorMessage.
		var s syntheticScan
		if err := json.Unmarshal(line, &s); err == nil &&
			s.Type == "assistant" && s.Error == "rate_limit" && s.IsApiErrorMessage {
			var text string
			for _, b := range s.Message.Content {
				if b.Type == "text" {
					text = b.Text
					break
				}
			}
			if t, ok := parseLimitResetText(text, s.Timestamp); ok {
				lastIdx = i
				lastResetsAt = t
			}
		}
	}
	if lastIdx < 0 {
		return time.Time{}, nil
	}

	// If a *non-synthetic* user or assistant event follows the rate-limit, the session resumed.
	for _, line := range lines[lastIdx+1:] {
		var ev typeOnly
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "user" && ev.Type != "assistant" {
			continue
		}
		// A synthetic rate-limit assistant must NOT count as a resume. Re-parse to check.
		var s syntheticScan
		if json.Unmarshal(line, &s) == nil &&
			s.Type == "assistant" && s.Error == "rate_limit" && s.IsApiErrorMessage {
			continue
		}
		return time.Time{}, nil
	}

	return lastResetsAt, nil
}
