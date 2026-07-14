package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
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
	// ModelTokens accumulates each token category per model across all
	// non-error assistant usage records (ADR 0021 §6). It is the ingestion the
	// native CostPricer prices; nil/empty when the transcript has no usage.
	// Error (isApiErrorMessage) records are excluded, matching TotalTokens.
	ModelTokens map[string]usage.ModelTokens
	LastError   *ErrorRecord // most recent isApiErrorMessage event in the transcript; nil if no such event seen
	// LastErrorRetryable is pa-monitor's derived auto-resume verdict for
	// LastError (transient server/network → true). It is tracked separately
	// from the shared ErrorRecord because the daemon flips it to false on
	// escalation, independent of the record's intrinsic RetryClass. Zero when
	// LastError is nil.
	LastErrorRetryable bool
}

// Scan reads path once and returns all enrichment data. It replaces calling
// FirstPrompt, LatestContext, OpenSubagents, IsAwaitingInput, and RateLimitPause
// individually. Returns zero Snapshot (no error) when path is empty or missing.
func Scan(path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, nil
		}
		return Snapshot{}, err
	}
	defer func() { _ = f.Close() }()

	type scanEv struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		Timestamp time.Time       `json:"timestamp"`
		RetryInMs int64           `json:"retryInMs"`
		Message   Message         `json:"message"`
		Error     json.RawMessage `json:"error"`
	}

	// nestedErrType extracts the legacy rate_limit_error type from the
	// nested error object shape: {"error":{"error":{"type":"..."}}}
	nestedErrType := func(raw json.RawMessage) string {
		var nested struct {
			Error struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &nested) == nil {
			return nested.Error.Error.Type
		}
		return ""
	}

	var snap Snapshot

	firstPromptDone := false
	var lastCtxTotal int
	var lastCtxModel string
	var totalOut int
	modelTokens := map[string]usage.ModelTokens{}
	openTasks := make(map[string]bool)
	pendingAUQ := make(map[string]bool)
	var lastAPIErrTime time.Time
	var lastAPIErrRetry int64
	hasAPIErr := false
	resumedAfterAPIErr := false

	// Collect all lines into a slice so we can do the IsTerminal tail-walk
	// for LastError without a second file scan.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var lines [][]byte
	for sc.Scan() {
		b := make([]byte, len(sc.Bytes()))
		copy(b, sc.Bytes())
		lines = append(lines, b)
	}
	if err := sc.Err(); err != nil {
		return Snapshot{}, err
	}

	// LastError tracking: index and fields of the most recent api-error event.
	lastErrIdx := -1
	var lastErrEventTime time.Time
	var lastErrKind ErrorKind
	var lastErrText string

	for i, line := range lines {
		var ev scanEv
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		for _, b := range ev.Message.Content {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				delete(pendingAUQ, b.ToolUseID)
				delete(openTasks, b.ToolUseID)
			}
		}

		// Auxiliary parse: only the synthetic-assistant rate-limit shape sets
		// these top-level fields. Failure leaves all zero values (old shape).
		var aux struct {
			Error             string `json:"error"`
			IsApiErrorMessage bool   `json:"isApiErrorMessage"`
		}
		_ = json.Unmarshal(line, &aux)
		isSyntheticRateLimit := ev.Type == "assistant" && aux.Error == "rate_limit" && aux.IsApiErrorMessage

		switch ev.Type {
		case "user":
			if !firstPromptDone {
				text := plainUserText(ev.Message.Content)
				if cleaned := cleanPromptText(text); cleaned != "" && !strings.HasPrefix(cleaned, "<") {
					snap.FirstPrompt = cleaned
					firstPromptDone = true
				}
			}
			if hasAPIErr {
				resumedAfterAPIErr = true
			}

		case "assistant":
			if isSyntheticRateLimit {
				// Synthetic rate-limit message has zero usage and is NOT a user/assistant
				// resume. Read the reset time from the text and record it.
				var text string
				for _, b := range ev.Message.Content {
					if b.Type == "text" {
						text = b.Text
						break
					}
				}
				if t, ok := ct.ParseRateLimitReset(text, ev.Timestamp); ok {
					lastAPIErrTime = t
					lastAPIErrRetry = 0 // sentinel: lastAPIErrTime is absolute
					hasAPIErr = true
					resumedAfterAPIErr = false
				}
			}
			// Track any isApiErrorMessage event (including non-rate-limit kinds) for LastError.
			if aux.IsApiErrorMessage {
				k := ErrorKind(aux.Error)
				switch k {
				case ErrRateLimit, ErrUnknown, ErrServerError, ErrInvalidRequest, ErrAuthFailed, ErrModelNotFound:
					var text string
					for _, c := range ev.Message.Content {
						if c.Type == "text" {
							text = c.Text
							break
						}
					}
					lastErrIdx = i
					lastErrEventTime = ev.Timestamp
					lastErrKind = k
					lastErrText = text
				}
			}
			if !aux.IsApiErrorMessage {
				u := ev.Message.Usage
				ctx := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
				if ctx > 0 {
					lastCtxTotal = ctx
					lastCtxModel = ev.Message.Model
				}
				totalOut += u.OutputTokens
				// Cumulative per-model token ingestion for the native
				// CostPricer (ADR 0021 §6). Same single-pass hot path as
				// totalOut; adds only integer sums keyed by model.
				if m := ev.Message.Model; m != "" {
					mt := modelTokens[m]
					mt.Input += u.InputTokens
					mt.Output += u.OutputTokens
					mt.CacheCreation += u.CacheCreationInputTokens
					mt.CacheRead += u.CacheReadInputTokens
					modelTokens[m] = mt
				}
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
			}

		case "system":
			if ev.Subtype == "api_error" &&
				nestedErrType(ev.Error) == "rate_limit_error" && ev.RetryInMs > 0 {
				lastAPIErrTime = ev.Timestamp
				lastAPIErrRetry = ev.RetryInMs
				hasAPIErr = true
				resumedAfterAPIErr = false
			}
		}
	}

	snap.Model = lastCtxModel
	snap.ContextTokens = lastCtxTotal
	snap.TotalTokens = totalOut
	if len(modelTokens) > 0 {
		snap.ModelTokens = modelTokens
	}
	snap.SubagentCount = len(openTasks)
	snap.AwaitingInput = len(pendingAUQ) > 0
	if hasAPIErr && !resumedAfterAPIErr {
		if lastAPIErrRetry == 0 {
			// Synthetic shape: lastAPIErrTime is already the absolute reset time.
			snap.RateLimitResetsAt = lastAPIErrTime
		} else {
			snap.RateLimitResetsAt = lastAPIErrTime.Add(time.Duration(lastAPIErrRetry) * time.Millisecond)
		}
	}

	// Build LastError with IsTerminal detection via tail-walk (mirrors LastAPIError).
	if lastErrIdx >= 0 {
		terminal := true
		for _, line := range lines[lastErrIdx+1:] {
			var tail struct {
				Type              string `json:"type"`
				IsApiErrorMessage bool   `json:"isApiErrorMessage"`
			}
			if err := json.Unmarshal(line, &tail); err != nil {
				continue
			}
			if tail.Type != "user" && tail.Type != "assistant" {
				continue
			}
			if tail.Type == "assistant" && tail.IsApiErrorMessage {
				continue
			}
			terminal = false
			break
		}
		snap.LastError = &ErrorRecord{
			Kind:       lastErrKind,
			Text:       lastErrText,
			At:         lastErrEventTime,
			IsTerminal: terminal,
		}
		// Derive the auto-resume verdict from the shared RetryClass policy. Kept
		// on the Snapshot (not the record) so the daemon's escalation flip can
		// override it without mutating the intrinsic classification.
		snap.LastErrorRetryable = Retryable(snap.LastError)
	}

	return snap, nil
}
