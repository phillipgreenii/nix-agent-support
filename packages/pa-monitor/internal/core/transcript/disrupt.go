package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// ErrorKind enumerates the `error` field values seen on synthetic
// isApiErrorMessage events emitted by Claude Code. Kept as a closed
// allowlist so retryability is unambiguous.
type ErrorKind string

const (
	ErrRateLimit      ErrorKind = "rate_limit"
	ErrUnknown        ErrorKind = "unknown"
	ErrServerError    ErrorKind = "server_error"
	ErrInvalidRequest ErrorKind = "invalid_request"
	ErrAuthFailed     ErrorKind = "authentication_failed"
	ErrModelNotFound  ErrorKind = "model_not_found"
)

// IsRetryable reports whether the disrupt producer treats this kind as
// auto-nudgeable. Only transport-level (unknown) and transient-server
// (server_error) kinds qualify.
func (k ErrorKind) IsRetryable() bool {
	return k == ErrUnknown || k == ErrServerError
}

// ErrorRecord is the most recent isApiErrorMessage observed in a session
// transcript. IsTerminal is true iff no non-synthetic user/assistant
// event follows in the JSONL.
type ErrorRecord struct {
	Kind        ErrorKind
	Text        string
	At          time.Time
	IsTerminal  bool
	IsRetryable bool
	// IsContextLimit is true when this error is the Claude Code
	// context-window-exceeded condition (an invalid_request whose text is
	// the "prompt is too long" message). Distinguished from other
	// invalid_request errors (low credit balance, bad params) so callers
	// can count context-limit hits separately.
	IsContextLimit bool
}

// isContextLimitText reports whether an api-error of the given kind is the
// context-window-exceeded condition. Claude Code surfaces it as an
// invalid_request whose message contains "prompt is too long" (often with a
// "<N> tokens > <max> maximum" suffix); matched case-insensitively so a
// leading "API Error: " prefix and casing variations still register.
func isContextLimitText(kind ErrorKind, text string) bool {
	return kind == ErrInvalidRequest &&
		strings.Contains(strings.ToLower(text), "prompt is too long")
}

// LastAPIError returns the most recent isApiErrorMessage event in the
// transcript regardless of kind. IsTerminal is true iff no subsequent
// (non-synthetic) user/assistant event follows. Returns zero ErrorRecord
// if no api-error event is present (Kind == "").
func LastAPIError(path string) (ErrorRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return ErrorRecord{}, err
	}
	defer f.Close()

	type apiErrorScan struct {
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
		Type              string `json:"type"`
		IsApiErrorMessage bool   `json:"isApiErrorMessage"`
		Error             string `json:"error"`
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
		return ErrorRecord{}, sc.Err()
	}

	lastIdx := -1
	var rec ErrorRecord
	for i, line := range lines {
		var ev apiErrorScan
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "assistant" || !ev.IsApiErrorMessage {
			continue
		}
		kind := ErrorKind(ev.Error)
		switch kind {
		case ErrRateLimit, ErrUnknown, ErrServerError, ErrInvalidRequest, ErrAuthFailed, ErrModelNotFound:
		default:
			continue
		}
		var text string
		for _, c := range ev.Message.Content {
			if c.Type == "text" {
				text = c.Text
				break
			}
		}
		lastIdx = i
		rec = ErrorRecord{
			Kind:           kind,
			Text:           text,
			At:             ev.Timestamp,
			IsTerminal:     true,
			IsRetryable:    kind.IsRetryable(),
			IsContextLimit: isContextLimitText(kind, text),
		}
	}
	if lastIdx < 0 {
		return ErrorRecord{}, nil
	}
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
		rec.IsTerminal = false
		break
	}
	return rec, nil
}
