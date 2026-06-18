package claudetranscript

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// PendingScheduledResume reports a pending scheduled self-resume: the last
// ScheduleWakeup tool_use (an assistant event whose content includes a tool_use
// named "ScheduleWakeup" with input.delaySeconds) for which NO subsequent
// user/assistant turn follows in the transcript. This is the same "nothing
// follows" technique LastAPIError uses for IsTerminal — trailing metadata
// records (mode, last-prompt, system/turn_duration, …) and api-error synthetic
// assistant events do NOT count as a resume.
//
// resumeAt = the ScheduleWakeup event's timestamp + delaySeconds seconds.
//
// ok is false when there is no ScheduleWakeup at all, when the session resumed
// afterward (a real user/assistant turn follows the last ScheduleWakeup), or
// when the matching event has no usable timestamp/delay.
//
// This is ADVISORY: a pending scheduled resume is intent to continue later, not
// a turn in progress. Whether to keep the machine awake until the wake fires is
// a separate decision (out of scope for this detector).
func PendingScheduledResume(path string) (resumeAt time.Time, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	type wakeupScan struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Message   struct {
			Content []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Input struct {
					// DelaySeconds is a pointer so an absent field is
					// distinguishable from an explicit 0.
					DelaySeconds *float64 `json:"delaySeconds"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	type typeOnly struct {
		Type              string `json:"type"`
		IsApiErrorMessage bool   `json:"isApiErrorMessage"`
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

	// Find the last ScheduleWakeup tool_use with a delaySeconds.
	lastIdx := -1
	var delay float64
	var at time.Time
	for i, line := range lines {
		var ev wakeupScan
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "assistant" {
			continue
		}
		for _, c := range ev.Message.Content {
			if c.Type == "tool_use" && c.Name == "ScheduleWakeup" && c.Input.DelaySeconds != nil {
				lastIdx = i
				delay = *c.Input.DelaySeconds
				at = ev.Timestamp
			}
		}
	}
	if lastIdx < 0 || at.IsZero() {
		return time.Time{}, false
	}

	// Resumed afterward? Any real (non-synthetic) user/assistant turn after the
	// ScheduleWakeup means the session already woke — no longer pending.
	for _, line := range lines[lastIdx+1:] {
		var ev typeOnly
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "user" && ev.Type != "assistant" {
			continue
		}
		if ev.Type == "assistant" && ev.IsApiErrorMessage {
			continue
		}
		return time.Time{}, false // resumed
	}

	return at.Add(time.Duration(delay * float64(time.Second))), true
}
