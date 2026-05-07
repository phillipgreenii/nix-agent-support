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

// parseLimitResetText resolves the next occurrence of the clock time + IANA TZ
// in the message strictly after eventTime (the next reset window is always in
// the future). When the message includes a month + day prefix (weekly-limit
// shape), the reset time is anchored to that calendar date; rollover is by
// year. When omitted, rollover is by 24 hours. Returns (zero, false) on any
// parse failure (bad clock time, unknown TZ, regex miss).
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

	if monthStr != "" {
		month, ok := monthAbbrev[monthStr]
		if !ok {
			return time.Time{}, false
		}
		day, err := strconv.Atoi(dayStr)
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}
		candidate := time.Date(evLocal.Year(), month, day, hour, minute, 0, 0, loc)
		if !candidate.After(eventTime) {
			candidate = candidate.AddDate(1, 0, 0)
		}
		return candidate.UTC(), true
	}

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
