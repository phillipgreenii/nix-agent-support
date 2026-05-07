package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Snapshot holds all per-session enrichment data extracted in a single pass.
type Snapshot struct {
	FirstPrompt       string
	Model             string
	ContextTokens     int
	TotalTokens       int
	SubagentCount     int
	AwaitingInput     bool
	RateLimitResetsAt time.Time
}

// Scan reads path once and returns all enrichment data. It replaces calling
// FirstPrompt, LatestContext, OpenSubagents, IsAwaitingInput, and RateLimitPause
// individually. Returns zero Snapshot (no error) when path is empty or missing.
func Scan(path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, nil // missing path → zero values, no error
	}
	defer f.Close()

	type scanEv struct {
		Type      string    `json:"type"`
		Subtype   string    `json:"subtype"`
		Timestamp time.Time `json:"timestamp"`
		RetryInMs int64     `json:"retryInMs"`
		Message   Message   `json:"message"`
		Error     struct {
			Error struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			} `json:"error"`
		} `json:"error"`
	}

	var snap Snapshot

	firstPromptDone := false
	var lastCtxTotal int
	var lastCtxModel string
	var totalOut int
	openTasks := make(map[string]bool)
	pendingAUQ := make(map[string]bool)
	var lastAPIErrTime time.Time
	var lastAPIErrRetry int64
	hasAPIErr := false
	resumedAfterAPIErr := false

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var ev scanEv
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "user":
			if !firstPromptDone {
				text := plainUserText(ev.Message.Content)
				if cleaned := cleanPromptText(text); cleaned != "" && !strings.HasPrefix(cleaned, "<") {
					snap.FirstPrompt = cleaned
					firstPromptDone = true
				}
			}
			for _, b := range ev.Message.Content {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					delete(pendingAUQ, b.ToolUseID)
					delete(openTasks, b.ToolUseID)
				}
			}
			if hasAPIErr {
				resumedAfterAPIErr = true
			}

		case "assistant":
			u := ev.Message.Usage
			ctx := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
			if ctx > 0 {
				lastCtxTotal = ctx
				lastCtxModel = ev.Message.Model
			}
			totalOut += u.OutputTokens
			pendingAUQ = make(map[string]bool)
			for _, b := range ev.Message.Content {
				if b.Type == "tool_use" && b.ID != "" {
					switch b.Name {
					case "Task":
						openTasks[b.ID] = true
					case "AskUserQuestion":
						pendingAUQ[b.ID] = true
					}
				}
			}
			if hasAPIErr {
				resumedAfterAPIErr = true
			}

		case "system":
			if ev.Subtype == "api_error" &&
				ev.Error.Error.Error.Type == "rate_limit_error" && ev.RetryInMs > 0 {
				lastAPIErrTime = ev.Timestamp
				lastAPIErrRetry = ev.RetryInMs
				hasAPIErr = true
				resumedAfterAPIErr = false
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Snapshot{}, err
	}

	snap.Model = lastCtxModel
	snap.ContextTokens = lastCtxTotal
	snap.TotalTokens = totalOut
	snap.SubagentCount = len(openTasks)
	snap.AwaitingInput = len(pendingAUQ) > 0
	if hasAPIErr && !resumedAfterAPIErr {
		snap.RateLimitResetsAt = lastAPIErrTime.Add(time.Duration(lastAPIErrRetry) * time.Millisecond)
	}
	return snap, nil
}
