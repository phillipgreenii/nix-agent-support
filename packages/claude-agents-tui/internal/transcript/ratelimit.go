package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// RateLimitPause returns the time the usage window reopens when the transcript's
// most recent api_error is a rate_limit_error with no subsequent user/assistant event.
// Uses event.Timestamp + retryInMs — never time.Now() — so the calculation is correct
// even when the event was written hours before the TUI started.
func RateLimitPause(path string) (resetsAt time.Time, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
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
		return time.Time{}, false
	}

	// Find index of last rate_limit_error api_error event.
	apiErrIdx := -1
	var apiErrTime time.Time
	var apiErrRetry int64
	for i, line := range lines {
		var ev rateLimitScan
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type == "system" && ev.Subtype == "api_error" &&
			ev.Error.Error.Error.Type == "rate_limit_error" && ev.RetryInMs > 0 {
			apiErrIdx = i
			apiErrTime = ev.Timestamp
			apiErrRetry = ev.RetryInMs
		}
	}
	if apiErrIdx < 0 {
		return time.Time{}, false
	}

	// If any user or assistant event follows the api_error, the session already resumed.
	for _, line := range lines[apiErrIdx+1:] {
		var ev typeOnly
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type == "user" || ev.Type == "assistant" {
			return time.Time{}, false
		}
	}

	return apiErrTime.Add(time.Duration(apiErrRetry) * time.Millisecond), true
}
