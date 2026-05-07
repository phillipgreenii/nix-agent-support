package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// limitResetRe captures hour, minute, am/pm marker, and IANA TZ id.
// Matches strings like: "resets 3:30pm (America/New_York)".
var limitResetRe = regexp.MustCompile(`resets\s+(\d{1,2}):(\d{2})(am|pm)\s+\(([^)]+)\)`)

// parseLimitResetText resolves the next occurrence of the clock time + IANA TZ
// in the message strictly after eventTime (the next reset window is always in
// the future). Returns (zero, false) on any parse failure (bad clock time,
// unknown TZ, regex miss).
func parseLimitResetText(text string, eventTime time.Time) (time.Time, bool) {
	m := limitResetRe.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour < 1 || hour > 12 {
		return time.Time{}, false
	}
	minute, err := strconv.Atoi(m[2])
	if err != nil || minute < 0 || minute > 59 {
		return time.Time{}, false
	}
	switch strings.ToLower(m[3]) {
	case "am":
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour != 12 {
			hour += 12
		}
	}
	loc, err := time.LoadLocation(m[4])
	if err != nil {
		return time.Time{}, false
	}
	evLocal := eventTime.In(loc)
	candidate := time.Date(evLocal.Year(), evLocal.Month(), evLocal.Day(), hour, minute, 0, 0, loc)
	if !candidate.After(eventTime) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate.UTC(), true
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
// was written hours before the TUI started.
func RateLimitPause(path string) (resetsAt time.Time, err error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()

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
			lastIdx = i
			lastResetsAt = ev.Timestamp.Add(time.Duration(ev.RetryInMs) * time.Millisecond)
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
