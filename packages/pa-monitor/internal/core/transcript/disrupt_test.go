package transcript

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestErrorKindIsRetryable(t *testing.T) {
	tests := []struct {
		kind ErrorKind
		want bool
	}{
		{ErrUnknown, true},
		{ErrServerError, true},
		{ErrRateLimit, false},
		{ErrInvalidRequest, false},
		{ErrAuthFailed, false},
		{ErrModelNotFound, false},
		{ErrorKind(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := tt.kind.IsRetryable(); got != tt.want {
				t.Errorf("ErrorKind(%q).IsRetryable() = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

// apiErrorEvent returns a JSONL line for a synthetic isApiErrorMessage
// assistant event with the given error kind and message text.
func apiErrorEvent(ts time.Time, kind ErrorKind, text string) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":"%s","error":%q,"isApiErrorMessage":true,`+
			`"message":{"model":"<synthetic>","content":[{"type":"text","text":%q}]}}`,
		ts.UTC().Format(time.RFC3339Nano), string(kind), text)
}

func TestLastAPIErrorDetectsEachKind(t *testing.T) {
	cases := []struct {
		kind ErrorKind
		text string
	}{
		{ErrUnknown, "API Error: The socket connection was closed unexpectedly. ..."},
		{ErrServerError, "API Error: 529 Overloaded. ..."},
		{ErrInvalidRequest, "Prompt is too long"},
		{ErrAuthFailed, "Not logged in · Please run /login"},
		{ErrRateLimit, "You've hit your limit · resets 7:10pm (America/New_York)"},
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
			if got.IsRetryable != tc.kind.IsRetryable() {
				t.Errorf("IsRetryable = %v, want %v", got.IsRetryable, tc.kind.IsRetryable())
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
// Claude Code emits when the selected model is unavailable (verified against
// real transcripts, 2026-06-16). It is non-retryable (human must fix the model).
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
	if got.IsRetryable {
		t.Error("IsRetryable = true, want false (model_not_found needs human fix)")
	}
	if !got.IsTerminal {
		t.Error("IsTerminal = false, want true")
	}
}

// TestLastAPIErrorDetectsStreamIdleTimeout guards bead pg2-lpxq: Claude Code
// emits "API Error: Stream idle timeout - partial response received" as a
// synthetic isApiErrorMessage with error="unknown" (verified against real
// transcripts, 2026-06-16). It must classify as a terminal, retryable unknown
// disrupt with no text-matching special-case.
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
	if !got.IsRetryable {
		t.Error("IsRetryable = false, want true (unknown is retryable)")
	}
}

// TestLastSubagentErrorFindsTerminalChildDisrupt covers bead pg2-lpxq's
// subagent blind spot: a stream-idle-timeout that occurs inside a subagent
// lands only in subagents/agent-*.jsonl, which Scan() does not read. The most
// recent *terminal* subagent error is returned with FromSubagent=true.
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
	if got.Kind != ErrUnknown || !got.IsRetryable || !got.IsTerminal {
		t.Errorf("got %+v, want unknown/retryable/terminal", got)
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
