package claudetranscript

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// oversizedTextSize is the byte length of the synthetic transcript line's text
// field: 2 MiB, written as a plain literal so the fixture size cannot drift with
// the constants under test.
//
// The size is chosen to DISCRIMINATE, and the arithmetic is the whole point.
// Because (*bufio.Scanner).Buffer makes the effective ceiling on one line
// max(scannerMaxTokenSize, scannerInitialBufferSize), a fixture only proves
// scannerMaxTokenSize is load-bearing if it exceeds every value a single-operator
// corruption of `16 * 1024 * 1024` can produce:
//
//	16 * 1024 * 1024 = 16777216   as written — the fixture must still FIT under it
//	(16 + 1024) * 1024 = 1064960  the largest corruption, and the only one > 1 MiB
//	(16 - 1024) * 1024 = -1032192
//	(16 / 1024) * 1024 = 0
//	(16 % 1024) * 1024 = 16384
//	16 * 1024 + 1024 = 17408
//	16 * 1024 - 1024 = 15360
//	16 * 1024 / 1024 = 16
//	16 * 1024 % 1024 = 0
//
// Every corruption but the first falls below scannerInitialBufferSize, where the
// 1 MiB initial buffer sets the ceiling on its own. So the fixture must exceed
// 1064960 bytes to fail under all nine; a few-hundred-KB line would be read
// happily by every one of them and would prove nothing. 2 MiB clears that bar
// with room to spare while staying far below the 16 MiB ceiling.
const oversizedTextSize = 2097152

// oversizedText returns an oversizedTextSize-byte body that needs no JSON string
// escaping, so the encoded line's length is predictable.
func oversizedText() string { return strings.Repeat("x", oversizedTextSize) }

// oversizedFillerLine is an oversized JSONL line that every reader in this
// package skips on type, so it can precede a target event without changing what
// that reader should find. A reader whose scanner kept bufio's 64 KiB default
// stops here and never reaches the target.
func oversizedFillerLine() string {
	return `{"type":"filler","text":"` + oversizedText() + `"}`
}

// TestScannerSizes pins the two tuning sizes by value. This is deliberately a
// direct assertion rather than a behavioural one: scannerInitialBufferSize has NO
// observable effect a test can reach (shrinking it only makes Scan grow the
// buffer back up toward scannerMaxTokenSize, so the same lines still scan), and
// the sizes are a decision about real transcripts rather than something the suite
// can derive. The behavioural gate on the ceiling is the oversized-line tests
// below; this one keeps the numbers themselves from being quietly weakened.
func TestScannerSizes(t *testing.T) {
	if scannerInitialBufferSize != 1048576 {
		t.Errorf("scannerInitialBufferSize = %d, want 1048576 (1 MiB)", scannerInitialBufferSize)
	}
	if scannerMaxTokenSize != 16777216 {
		t.Errorf("scannerMaxTokenSize = %d, want 16777216 (16 MiB)", scannerMaxTokenSize)
	}
	// The ceiling must genuinely be the larger of the two, or the initial buffer
	// would be silently capping every read.
	if scannerMaxTokenSize <= scannerInitialBufferSize {
		t.Errorf("scannerMaxTokenSize (%d) must exceed scannerInitialBufferSize (%d); "+
			"Buffer's effective limit is the larger of the two, so otherwise the max is dead",
			scannerMaxTokenSize, scannerInitialBufferSize)
	}
}

// TestNewTranscriptScanner_readsOversizedLineIntact is the behavioural gate on the
// enlarged buffer: a line far past bufio's 64 KiB default token size must be
// returned WHOLE and leave Err() nil. With a default scanner this fails with
// "bufio.Scanner: token too long".
func TestNewTranscriptScanner_readsOversizedLineIntact(t *testing.T) {
	text := oversizedText()
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`

	sc := newTranscriptScanner(strings.NewReader(line + "\n"))
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil for a %d-byte line", err, len(line))
	}
	if len(got) != 1 {
		t.Fatalf("scanned %d lines, want 1", len(got))
	}
	if len(got[0]) != len(line) {
		t.Fatalf("scanned line is %d bytes, want %d (truncated)", len(got[0]), len(line))
	}

	// Intact means decodable with the full payload, not merely the right length.
	var ev Event
	if err := json.Unmarshal([]byte(got[0]), &ev); err != nil {
		t.Fatalf("Unmarshal of the scanned line: %v", err)
	}
	if len(ev.Message.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(ev.Message.Content))
	}
	if ev.Message.Content[0].Text != text {
		t.Errorf("text field differs from what was written (got %d bytes, want %d)",
			len(ev.Message.Content[0].Text), oversizedTextSize)
	}
}

// TestOversizedLine_everyReaderScansPastIt guards the WIRING: each transcript
// reader in this package must take its scanner from newTranscriptScanner. Every
// fixture puts an oversized filler line BEFORE the event the reader is meant to
// find, so a reader still using a default-sized scanner stops on the filler and
// reports "nothing found" instead of the event — which is exactly how a silently
// truncated transcript read presents.
func TestOversizedLine_everyReaderScansPastIt(t *testing.T) {
	ts := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tsStr := ts.Format(time.RFC3339Nano)

	cases := []struct {
		name   string
		target string
		check  func(t *testing.T, path string)
	}{
		{
			name:   "LastAssistantText",
			target: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"BANANA"}]}}`,
			check: func(t *testing.T, path string) {
				got, err := LastAssistantText(path)
				if err != nil {
					t.Fatalf("LastAssistantText err = %v, want nil", err)
				}
				if got != "BANANA" {
					t.Errorf("LastAssistantText = %q, want BANANA", got)
				}
			},
		},
		{
			name:   "IsAwaitingInput",
			target: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"AskUserQuestion"}]}}`,
			check: func(t *testing.T, path string) {
				ok, err := IsAwaitingInput(path)
				if err != nil {
					t.Fatalf("IsAwaitingInput err = %v, want nil", err)
				}
				if !ok {
					t.Error("IsAwaitingInput = false, want true (dangling AskUserQuestion after the oversized line)")
				}
			},
		},
		{
			name:   "PendingScheduledResume",
			target: scheduleWakeupEvent(ts, 120),
			check: func(t *testing.T, path string) {
				got, ok := PendingScheduledResume(path)
				if !ok {
					t.Fatal("PendingScheduledResume ok = false, want true")
				}
				if want := ts.Add(120 * time.Second); !got.Equal(want) {
					t.Errorf("resumeAt = %v, want %v", got, want)
				}
			},
		},
		{
			name:   "LastAPIError",
			target: apiErrorEvent(ts, ErrServerError, "API Error: 500 Internal server error"),
			check: func(t *testing.T, path string) {
				rec, err := LastAPIError(path)
				if err != nil {
					t.Fatalf("LastAPIError err = %v, want nil", err)
				}
				if rec.Kind != ErrServerError {
					t.Errorf("Kind = %q, want %q", rec.Kind, ErrServerError)
				}
			},
		},
		{
			name:   "LastMessageActivity",
			target: `{"type":"assistant","timestamp":"` + tsStr + `","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
			check: func(t *testing.T, path string) {
				got, ok := LastMessageActivity(path)
				if !ok {
					t.Fatal("LastMessageActivity ok = false, want true")
				}
				if !got.Equal(ts) {
					t.Errorf("LastMessageActivity = %v, want %v", got, ts)
				}
			},
		},
		{
			name:   "RateLimitPause",
			target: rateEvent(ts, 3600000),
			check: func(t *testing.T, path string) {
				got, err := RateLimitPause(path)
				if err != nil {
					t.Fatalf("RateLimitPause err = %v, want nil", err)
				}
				if want := ts.Add(time.Hour); !got.Equal(want) {
					t.Errorf("resetsAt = %v, want %v", got, want)
				}
			},
		},
	}

	filler := oversizedFillerLine()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.jsonl")
			if err := writeTestFile(path, filler+"\n"+tc.target+"\n"); err != nil {
				t.Fatal(err)
			}
			tc.check(t, path)
		})
	}
}

// TestRateLimitPause_propagatesOpenError covers RateLimitPause's os.Open failure
// arm. Dropping the error there (returning nil) makes an unreadable transcript
// indistinguishable from "no rate-limit event", so a consumer would treat a
// vanished session as free to run.
func TestRateLimitPause_propagatesOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	got, err := RateLimitPause(path)
	if err == nil {
		t.Fatal("err = nil, want a non-nil error for a missing transcript")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want a not-exist error", err)
	}
	if !got.IsZero() {
		t.Errorf("resetsAt = %v, want the zero time alongside the error", got)
	}
}

// TestRateLimitPause_propagatesScannerError covers RateLimitPause's `sc.Err()`
// arm: a read that fails PART-WAY must surface, not be swallowed into a "no
// pause" verdict computed from a partial transcript.
//
// A directory is the cheap trigger — os.Open succeeds on one and the first Read
// fails with EISDIR (verified on darwin: "read <path>: is a directory"). The only
// other way to fail this scanner is a line past scannerMaxTokenSize, which would
// need a 16 MiB fixture to reach.
func TestRateLimitPause_propagatesScannerError(t *testing.T) {
	got, err := RateLimitPause(t.TempDir())
	if err == nil {
		t.Fatal("err = nil, want the scanner's read error (reading a directory)")
	}
	if !got.IsZero() {
		t.Errorf("resetsAt = %v, want the zero time alongside the error", got)
	}
}
