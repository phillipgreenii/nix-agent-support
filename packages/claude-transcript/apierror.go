package claudetranscript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrorKind enumerates the `error` field values seen on synthetic
// isApiErrorMessage events emitted by Claude Code. Kept as a closed
// allowlist so classification is unambiguous.
type ErrorKind string

const (
	ErrRateLimit      ErrorKind = "rate_limit"
	ErrUnknown        ErrorKind = "unknown"
	ErrServerError    ErrorKind = "server_error"
	ErrInvalidRequest ErrorKind = "invalid_request"
	ErrAuthFailed     ErrorKind = "authentication_failed"
	ErrModelNotFound  ErrorKind = "model_not_found"
)

// RetryClass is the transience category of an api-error, derived from
// (kind, text). It is a neutral CLASSIFICATION computed by the library; the
// retry POLICY (which classes to retry, and how) belongs to each consumer.
type RetryClass int

const (
	// ClassTerminal is not transient: the caller decides what to do.
	ClassTerminal RetryClass = iota
	// ClassTransientServer is a transient server-side fault (5xx / 529
	// Overloaded / 522 / 502).
	ClassTransientServer
	// ClassTransientNetwork is a transport-level drop (socket closed,
	// ECONNRESET, stream idle timeout, …).
	ClassTransientNetwork
	// ClassRateLimited is a usage-limit pause; it has a reset window
	// (see RateLimitPause) and is not waited out by classification.
	ClassRateLimited
)

// ErrorRecord is the most recent isApiErrorMessage observed in a session
// transcript. IsTerminal is true iff no non-synthetic user/assistant event
// follows in the JSONL.
type ErrorRecord struct {
	Kind       ErrorKind
	Text       string
	At         time.Time
	IsTerminal bool
	// IsContextLimit is true when this error is the Claude Code
	// context-window-exceeded condition (an invalid_request whose text is the
	// "prompt is too long" message). Distinguished from other invalid_request
	// errors (low credit balance, bad params) so callers can count
	// context-limit hits separately.
	IsContextLimit bool
	// FromSubagent is true when this error was found in a subagent transcript
	// (subagents/agent-*.jsonl) rather than the main session transcript. Such
	// errors are surfaced for visibility but typically excluded from auto-nudge.
	FromSubagent bool
}

// networkDropAllowlist is the positive match set applied when kind == unknown.
// Every observed `unknown` in the local transcript corpus is in fact a
// transport/connection drop, so an `unknown` matching one of these is
// classified ClassTransientNetwork. The match is case-insensitive and
// tolerant of a leading "API Error: " prefix (matched on the lowercased text).
// An `unknown` matching none stays ClassTerminal (a genuine opaque error →
// hand back); that bucket is empty in the current corpus but kept for safety.
var networkDropAllowlist = []string{
	"socket connection was closed",
	// Covers ConnectionRefused / ECONNRESET / FailedToOpenSocket, all of which
	// Claude Code surfaces as "Unable to connect to API …".
	"unable to connect to api",
	"stream idle timeout",
	"overloaded",            // bare "Overloaded" (not the 529-prefixed server_error form)
	"internal server error", // bare "Internal server error" (not the 5xx server_error form)
	// Defensive: not observed in the corpus but cheap to allow.
	"socket hang up",
	"etimedout",
}

// RetryClass derives the transience category from the record's (kind, text).
//
//   - rate_limit                       → ClassRateLimited
//   - server_error                     → ClassTransientServer
//   - unknown matching networkDropAllowlist → ClassTransientNetwork
//   - everything else (incl. an unknown matching nothing, invalid_request,
//     authentication_failed, model_not_found, and the zero record) → ClassTerminal
//
// Matching is case-insensitive and tolerant of a leading "API Error: " prefix.
func (r ErrorRecord) RetryClass() RetryClass {
	switch r.Kind {
	case ErrRateLimit:
		return ClassRateLimited
	case ErrServerError:
		return ClassTransientServer
	case ErrUnknown:
		if matchesNetworkDrop(r.Text) {
			return ClassTransientNetwork
		}
		return ClassTerminal
	default:
		return ClassTerminal
	}
}

// matchesNetworkDrop reports whether text matches the connection-drop
// allowlist. Case-insensitive; the "API Error: " prefix is irrelevant because
// matching is a substring containment test on the lowercased text.
func matchesNetworkDrop(text string) bool {
	lower := strings.ToLower(text)
	for _, needle := range networkDropAllowlist {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
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

// IsContextLimit is the exported form of isContextLimitText: it reports whether
// an api-error of the given kind+text is the Claude Code context-window-exceeded
// condition. Consumers that classify api-errors outside LastAPIError (e.g. a
// single-pass transcript scanner) use this to set ErrorRecord.IsContextLimit.
func IsContextLimit(kind ErrorKind, text string) bool { return isContextLimitText(kind, text) }

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

// LastSubagentError scans the subagent transcripts of a session for the most
// recent *terminal* api-error and returns it tagged FromSubagent=true. The
// subagent directory is derived from the main transcript path:
// "<dir>/<sessionid>.jsonl" -> "<dir>/<sessionid>/subagents/agent-*.jsonl".
// Only terminal errors are returned (a child that resumed after its error has
// recovered and is not a disrupt). Returns ok=false if the directory is absent
// or no terminal subagent error exists.
//
// Note: for resumed/forked sessions ResolveTranscript may return a transcript
// whose basename differs from the session-id that spawned the subagents, so the
// derived subagents dir won't exist and this returns ok=false. That is graceful
// (no crash) and correct for the common non-resumed case; a resumed session
// with a dead subagent is a known coverage gap, not handled here.
func LastSubagentError(mainTranscriptPath string) (ErrorRecord, bool) {
	if mainTranscriptPath == "" {
		return ErrorRecord{}, false
	}
	subDir := strings.TrimSuffix(mainTranscriptPath, ".jsonl") + "/subagents"
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return ErrorRecord{}, false
	}
	var best ErrorRecord
	found := false
	for _, e := range entries {
		if e.IsDir() ||
			!strings.HasPrefix(e.Name(), "agent-") ||
			filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		rec, err := LastAPIError(filepath.Join(subDir, e.Name()))
		if err != nil || rec.Kind == "" || !rec.IsTerminal {
			continue
		}
		if !found || rec.At.After(best.At) {
			best = rec
			found = true
		}
	}
	if !found {
		return ErrorRecord{}, false
	}
	best.FromSubagent = true
	return best, true
}
