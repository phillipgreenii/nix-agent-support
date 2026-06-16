package claudetranscript

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func writeTestFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }

// apiErrorEvent returns a JSONL line for a synthetic isApiErrorMessage
// assistant event with the given error kind and message text.
func apiErrorEvent(ts time.Time, kind ErrorKind, text string) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":"%s","error":%q,"isApiErrorMessage":true,`+
			`"message":{"model":"<synthetic>","content":[{"type":"text","text":%q}]}}`,
		ts.UTC().Format(time.RFC3339Nano), string(kind), text)
}

// TestRetryClass_taxonomy covers every row of the spec's classification
// taxonomy table (grounded in the real transcript corpus), plus the empty
// no-match unknown bucket and the zero record.
func TestRetryClass_taxonomy(t *testing.T) {
	cases := []struct {
		name string
		kind ErrorKind
		text string
		want RetryClass
	}{
		// server_error → ClassTransientServer (all 5xx text shapes).
		{"server_error 500", ErrServerError, "API Error: 500 Internal server error", ClassTransientServer},
		{"server_error 529 Overloaded", ErrServerError, "API Error: 529 Overloaded", ClassTransientServer},
		{"server_error 522", ErrServerError, "API Error: 522", ClassTransientServer},
		{"server_error 502", ErrServerError, "API Error: 502 Bad Gateway", ClassTransientServer},

		// unknown matching the network allowlist → ClassTransientNetwork.
		{"unknown socket closed", ErrUnknown, "API Error: The socket connection was closed unexpectedly. ...", ClassTransientNetwork},
		{"unknown unable to connect", ErrUnknown, "API Error: Unable to connect to API (ConnectionRefused)", ClassTransientNetwork},
		{"unknown ECONNRESET via unable-to-connect", ErrUnknown, "Unable to connect to API: ECONNRESET", ClassTransientNetwork},
		{"unknown FailedToOpenSocket via unable-to-connect", ErrUnknown, "Unable to connect to API (FailedToOpenSocket)", ClassTransientNetwork},
		{"unknown stream idle timeout", ErrUnknown, "API Error: Stream idle timeout - partial response received", ClassTransientNetwork},
		{"unknown bare Overloaded", ErrUnknown, "Overloaded", ClassTransientNetwork},
		{"unknown bare Internal server error", ErrUnknown, "Internal server error", ClassTransientNetwork},
		{"unknown defensive socket hang up", ErrUnknown, "socket hang up", ClassTransientNetwork},
		{"unknown defensive ETIMEDOUT", ErrUnknown, "connect ETIMEDOUT 1.2.3.4:443", ClassTransientNetwork},

		// rate_limit → ClassRateLimited.
		{"rate_limit", ErrRateLimit, "You've hit your limit · resets 3:30pm (America/New_York)", ClassRateLimited},

		// Terminal kinds.
		{"authentication_failed", ErrAuthFailed, "Please run /login · 401 ...", ClassTerminal},
		{"model_not_found", ErrModelNotFound, "There's an issue with the selected model. It may not exist.", ClassTerminal},
		{"invalid_request context limit", ErrInvalidRequest, "Prompt is too long: 215000 tokens > 200000 maximum", ClassTerminal},

		// An unknown matching nothing in the allowlist stays ClassTerminal
		// (genuine opaque error → hand back). Empty in the corpus, kept for safety.
		{"unknown no-match opaque", ErrUnknown, "something completely unrecognized happened", ClassTerminal},

		// The zero record classifies as ClassTerminal.
		{"zero record", ErrorKind(""), "", ClassTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := ErrorRecord{Kind: tc.kind, Text: tc.text}
			if got := rec.RetryClass(); got != tc.want {
				t.Errorf("RetryClass(kind=%q text=%q) = %v, want %v", tc.kind, tc.text, got, tc.want)
			}
		})
	}
}

// TestRetryClass_caseInsensitiveAndPrefixTolerant proves the matcher is
// case-insensitive and tolerant of a leading "API Error: " prefix.
func TestRetryClass_caseInsensitiveAndPrefixTolerant(t *testing.T) {
	cases := []string{
		"SOCKET CONNECTION WAS CLOSED unexpectedly",
		"API Error: socket connection was closed",
		"api error: UNABLE TO CONNECT TO API",
		"  Stream Idle Timeout - partial response",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			rec := ErrorRecord{Kind: ErrUnknown, Text: text}
			if got := rec.RetryClass(); got != ClassTransientNetwork {
				t.Errorf("RetryClass(unknown, %q) = %v, want ClassTransientNetwork", text, got)
			}
		})
	}
}

func TestLastAPIErrorDetectsEachKind(t *testing.T) {
	cases := []struct {
		kind ErrorKind
		text string
		want RetryClass
	}{
		{ErrUnknown, "API Error: The socket connection was closed unexpectedly. ...", ClassTransientNetwork},
		{ErrServerError, "API Error: 529 Overloaded. ...", ClassTransientServer},
		{ErrInvalidRequest, "Prompt is too long", ClassTerminal},
		{ErrAuthFailed, "Not logged in · Please run /login", ClassTerminal},
		{ErrRateLimit, "You've hit your limit · resets 7:10pm (America/New_York)", ClassRateLimited},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			ts := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
			path := t.TempDir() + "/t.jsonl"
			if err := writeTestFile(path, apiErrorEvent(ts, tc.kind, tc.text)+"\n"); err != nil {
				t.Fatal(err)
			}
			got, err := LastAPIError(path)
			if err != nil {
				t.Fatalf("LastAPIError err = %v, want nil", err)
			}
			if got.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.kind)
			}
			if got.Text != tc.text {
				t.Errorf("Text = %q, want %q", got.Text, tc.text)
			}
			if !got.At.Equal(ts) {
				t.Errorf("At = %v, want %v", got.At, ts)
			}
			if !got.IsTerminal {
				t.Errorf("IsTerminal = false, want true (no event follows)")
			}
			if got.RetryClass() != tc.want {
				t.Errorf("RetryClass = %v, want %v", got.RetryClass(), tc.want)
			}
		})
	}
}

// TestLastAPIErrorDetectsContextLimit covers the IsContextLimit flag: it is
// set only for invalid_request errors whose text is the Claude Code
// "prompt is too long" context-window message, not for other invalid_request
// errors (e.g. low credit balance) nor for other kinds.
func TestLastAPIErrorDetectsContextLimit(t *testing.T) {
	ts := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	cases := []struct {
		name string
		kind ErrorKind
		text string
		want bool
	}{
		{"prompt too long with token counts", ErrInvalidRequest, "Prompt is too long: 215000 tokens > 200000 maximum", true},
		{"prompt too long lowercased + prefixed", ErrInvalidRequest, "API Error: prompt is too long", true},
		{"other invalid_request", ErrInvalidRequest, "Your credit balance is too low to access the Anthropic API", false},
		{"rate limit is not a context limit", ErrRateLimit, "You've hit your limit · resets 7:10pm (America/New_York)", false},
		{"server error is not a context limit", ErrServerError, "API Error: 529 Overloaded", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/t.jsonl"
			if err := writeTestFile(path, apiErrorEvent(ts, tc.kind, tc.text)+"\n"); err != nil {
				t.Fatal(err)
			}
			got, err := LastAPIError(path)
			if err != nil {
				t.Fatalf("LastAPIError err = %v, want nil", err)
			}
			if got.IsContextLimit != tc.want {
				t.Errorf("IsContextLimit = %v, want %v (kind=%q text=%q)", got.IsContextLimit, tc.want, tc.kind, tc.text)
			}
		})
	}
}

func TestLastAPIErrorIsTerminalFlipsOnResume(t *testing.T) {
	ts := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := apiErrorEvent(ts, ErrUnknown, "API Error: socket closed") + "\n" +
		`{"type":"user","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := LastAPIError(path)
	if err != nil {
		t.Fatalf("LastAPIError err = %v, want nil", err)
	}
	if got.Kind != ErrUnknown {
		t.Errorf("Kind = %q, want %q", got.Kind, ErrUnknown)
	}
	if got.IsTerminal {
		t.Error("IsTerminal = true, want false (user resumed after error)")
	}
}

func TestLastAPIErrorIsTerminalSurvivesAnotherSyntheticError(t *testing.T) {
	ts1 := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	ts2 := ts1.Add(30 * time.Second)
	path := t.TempDir() + "/t.jsonl"
	body := apiErrorEvent(ts1, ErrServerError, "529 Overloaded") + "\n" +
		apiErrorEvent(ts2, ErrUnknown, "socket closed") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := LastAPIError(path)
	if err != nil {
		t.Fatalf("LastAPIError err = %v, want nil", err)
	}
	if got.Kind != ErrUnknown {
		t.Errorf("Kind = %q, want %q (most recent wins)", got.Kind, ErrUnknown)
	}
	if !got.At.Equal(ts2) {
		t.Errorf("At = %v, want %v", got.At, ts2)
	}
	if !got.IsTerminal {
		t.Error("IsTerminal = false, want true (second synthetic error is not a resume)")
	}
}

// TestLastAPIErrorDetectsModelNotFound covers the model_not_found kind, which
// Claude Code emits when the selected model is unavailable. It is terminal
// (a human must fix the model → hand back).
func TestLastAPIErrorDetectsModelNotFound(t *testing.T) {
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	const text = "There's an issue with the selected model (claude-fable-5). It may not exist or you may not have access."
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, apiErrorEvent(ts, ErrModelNotFound, text)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := LastAPIError(path)
	if err != nil {
		t.Fatalf("LastAPIError err = %v, want nil", err)
	}
	if got.Kind != ErrModelNotFound {
		t.Errorf("Kind = %q, want %q", got.Kind, ErrModelNotFound)
	}
	if got.RetryClass() != ClassTerminal {
		t.Errorf("RetryClass = %v, want ClassTerminal (model_not_found needs human fix)", got.RetryClass())
	}
	if !got.IsTerminal {
		t.Error("IsTerminal = false, want true")
	}
}

// TestLastAPIErrorDetectsStreamIdleTimeout guards a stream-idle-timeout, which
// Claude Code emits as a synthetic isApiErrorMessage with error="unknown". It
// must classify as a terminal record that is ClassTransientNetwork.
func TestLastAPIErrorDetectsStreamIdleTimeout(t *testing.T) {
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	const text = "API Error: Stream idle timeout - partial response received"
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, apiErrorEvent(ts, ErrUnknown, text)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := LastAPIError(path)
	if err != nil {
		t.Fatalf("LastAPIError err = %v, want nil", err)
	}
	if got.Kind != ErrUnknown {
		t.Errorf("Kind = %q, want %q", got.Kind, ErrUnknown)
	}
	if got.Text != text {
		t.Errorf("Text = %q, want %q", got.Text, text)
	}
	if !got.IsTerminal {
		t.Error("IsTerminal = false, want true (no event follows)")
	}
	if got.RetryClass() != ClassTransientNetwork {
		t.Errorf("RetryClass = %v, want ClassTransientNetwork", got.RetryClass())
	}
}

// TestLastSubagentErrorFindsTerminalChildDisrupt covers the subagent blind
// spot: a stream-idle-timeout that occurs inside a subagent lands only in
// subagents/agent-*.jsonl. The most recent *terminal* subagent error is
// returned with FromSubagent=true.
func TestLastSubagentErrorFindsTerminalChildDisrupt(t *testing.T) {
	dir := t.TempDir()
	mainPath := dir + "/sess.jsonl"
	if err := writeTestFile(mainPath, ""); err != nil {
		t.Fatal(err)
	}
	subDir := dir + "/sess/subagents"
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	const text = "API Error: Stream idle timeout - partial response received"
	if err := writeTestFile(subDir+"/agent-aaaa.jsonl", apiErrorEvent(ts, ErrUnknown, text)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, ok := LastSubagentError(mainPath)
	if !ok {
		t.Fatal("LastSubagentError ok = false, want true")
	}
	if got.Kind != ErrUnknown || got.RetryClass() != ClassTransientNetwork || !got.IsTerminal {
		t.Errorf("got %+v (class=%v), want unknown/network/terminal", got, got.RetryClass())
	}
	if !got.FromSubagent {
		t.Error("FromSubagent = false, want true")
	}
}

// TestLastSubagentErrorIgnoresRecoveredChild verifies a subagent that resumed
// after its error (IsTerminal=false) is not surfaced — the child recovered.
func TestLastSubagentErrorIgnoresRecoveredChild(t *testing.T) {
	dir := t.TempDir()
	mainPath := dir + "/sess.jsonl"
	if err := writeTestFile(mainPath, ""); err != nil {
		t.Fatal(err)
	}
	subDir := dir + "/sess/subagents"
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	body := apiErrorEvent(ts, ErrUnknown, "API Error: Stream idle timeout - partial response received") + "\n" +
		`{"type":"user","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(subDir+"/agent-aaaa.jsonl", body); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastSubagentError(mainPath); ok {
		t.Error("LastSubagentError ok = true, want false (child recovered)")
	}
}

// TestLastSubagentErrorNoSubagentDir returns ok=false when there is no
// subagents directory (the common case).
func TestLastSubagentErrorNoSubagentDir(t *testing.T) {
	dir := t.TempDir()
	mainPath := dir + "/sess.jsonl"
	if err := writeTestFile(mainPath, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastSubagentError(mainPath); ok {
		t.Error("LastSubagentError ok = true, want false (no subagents dir)")
	}
}
