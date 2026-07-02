package claudetranscript

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// registryJSON renders a registry file body. waitingFor=="" is written as JSON
// null (the absent-field shape observed in the corpus); a non-empty value is
// written as a string.
func registryJSON(pid int, sessionID, name, status, waitingFor string, startedAtMs, statusUpdatedAtMs int64) string {
	wf := "null"
	if waitingFor != "" {
		wf = fmt.Sprintf("%q", waitingFor)
	}
	nm := "null"
	if name != "" {
		nm = fmt.Sprintf("%q", name)
	}
	return fmt.Sprintf(
		`{"pid":%d,"sessionId":%q,"cwd":"/tmp/work","name":%s,"kind":"interactive",`+
			`"entrypoint":"cli","status":%q,"waitingFor":%s,"startedAt":%d,"statusUpdatedAt":%d}`,
		pid, sessionID, nm, status, wf, startedAtMs, statusUpdatedAtMs,
	)
}

func TestReadSessionFile_parsesAllFields(t *testing.T) {
	started := time.Date(2026, 6, 17, 17, 22, 59, 0, time.UTC)
	updated := time.Date(2026, 6, 18, 6, 13, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "5309.json")
	body := registryJSON(5309, "sess-abc", "split-sox-alerts", StatusWaiting, "permission prompt",
		started.UnixMilli(), updated.UnixMilli())
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSessionFile(path)
	if err != nil {
		t.Fatalf("ReadSessionFile err = %v, want nil", err)
	}
	if got.PID != 5309 || got.SessionID != "sess-abc" || got.Name != "split-sox-alerts" {
		t.Errorf("identity = %+v, want pid 5309 / sess-abc / split-sox-alerts", got)
	}
	if got.Cwd != "/tmp/work" || got.Kind != "interactive" || got.Entrypoint != "cli" {
		t.Errorf("meta = %+v, want cwd/kind/entrypoint set", got)
	}
	if got.Status != StatusWaiting || got.WaitingFor != "permission prompt" {
		t.Errorf("status/waitingFor = %q/%q, want waiting/permission prompt", got.Status, got.WaitingFor)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
	}
	if !got.StatusUpdatedAt.Equal(updated) {
		t.Errorf("StatusUpdatedAt = %v, want %v", got.StatusUpdatedAt, updated)
	}
}

func TestReadSessionRegistry(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	upd := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC).UnixMilli()

	// busy, idle, waiting(+waitingFor), waiting(absent waitingFor).
	must := func(name, body string) {
		if err := writeTestFile(filepath.Join(dir, name), body); err != nil {
			t.Fatal(err)
		}
	}
	must("1.json", registryJSON(1, "s-busy", "", StatusBusy, "", started, upd))
	must("2.json", registryJSON(2, "s-idle", "", StatusIdle, "", started, upd))
	must("3.json", registryJSON(3, "s-wait", "", StatusWaiting, "permission prompt", started, upd))
	must("4.json", registryJSON(4, "s-wait2", "", StatusWaiting, "", started, upd))
	// malformed JSON — must be skipped, not fail the sweep.
	must("5.json", `{"pid":5,"status":"busy"`)
	// non-.json — ignored.
	must("notes.txt", "ignore me")
	// a subdirectory — ignored.
	if err := os.MkdirAll(filepath.Join(dir, "sub.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := ReadSessionRegistry(dir)
	if err != nil {
		t.Fatalf("ReadSessionRegistry err = %v, want nil", err)
	}
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4 (malformed/non-json/dir skipped); got %+v", len(got), got)
	}
	byPID := map[int]RegistrySession{}
	for _, s := range got {
		byPID[s.PID] = s
	}
	if byPID[1].Status != StatusBusy {
		t.Errorf("pid 1 status = %q, want busy", byPID[1].Status)
	}
	if byPID[2].Status != StatusIdle {
		t.Errorf("pid 2 status = %q, want idle", byPID[2].Status)
	}
	if byPID[3].Status != StatusWaiting || byPID[3].WaitingFor != "permission prompt" {
		t.Errorf("pid 3 = %q/%q, want waiting/permission prompt", byPID[3].Status, byPID[3].WaitingFor)
	}
	if byPID[4].Status != StatusWaiting || byPID[4].WaitingFor != "" {
		t.Errorf("pid 4 = %q/%q, want waiting/empty waitingFor", byPID[4].Status, byPID[4].WaitingFor)
	}
}

func TestReadSessionRegistry_missingDir(t *testing.T) {
	got, err := ReadSessionRegistry(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("err = %v, want nil for missing dir", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil slice", got)
	}
}

// TestClassifyActivity_precedence covers the spec §4.2/§4.3 precedence exactly.
func TestClassifyActivity_precedence(t *testing.T) {
	const freshWindow = 30 * time.Second
	base := time.Date(2026, 6, 18, 0, 13, 0, 0, time.UTC)

	cases := []struct {
		name          string
		status        string
		waitingFor    string
		statusUpdated time.Time
		awaitingInput bool
		lastActivity  time.Time
		wantActivity  Activity
		wantReason    string
	}{
		{
			name:          "waiting + fresh -> WaitingForHuman (permission prompt)",
			status:        StatusWaiting,
			waitingFor:    "permission prompt",
			statusUpdated: base,
			lastActivity:  base.Add(10 * time.Second), // within freshWindow
			wantActivity:  WaitingForHuman,
			wantReason:    "permission prompt",
		},
		{
			name:          "waiting + stale -> Idle (transcript advanced well past statusUpdatedAt)",
			status:        StatusWaiting,
			waitingFor:    "permission prompt",
			statusUpdated: base,                                   // status set 00:13
			lastActivity:  base.Add(3*time.Hour + 30*time.Minute), // transcript at 03:43 = stale
			wantActivity:  Idle,
			wantReason:    "",
		},
		{
			name:          "awaitingInput overrides busy -> WaitingForHuman (NOT freshness-gated)",
			status:        StatusBusy,
			statusUpdated: base,
			awaitingInput: true,
			lastActivity:  base.Add(99 * time.Hour), // arbitrarily stale; awaiting arm ignores it
			wantActivity:  WaitingForHuman,
			wantReason:    WaitReasonAskUserQuestion,
		},
		{
			name:          "awaitingInput overrides idle -> WaitingForHuman",
			status:        StatusIdle,
			statusUpdated: base,
			awaitingInput: true,
			lastActivity:  base,
			wantActivity:  WaitingForHuman,
			wantReason:    WaitReasonAskUserQuestion,
		},
		{
			name:          "busy -> Active (fresh transcript)",
			status:        StatusBusy,
			statusUpdated: base,
			lastActivity:  base,
			wantActivity:  Active,
		},
		{
			name:          "busy -> Active ALWAYS, even with very old statusUpdatedAt (turn-start marker, not heartbeat)",
			status:        StatusBusy,
			statusUpdated: base,                     // turn started long ago
			lastActivity:  base.Add(16 * time.Hour), // main transcript long-stale during subagent run
			wantActivity:  Active,
		},
		{
			name:          "idle -> Idle",
			status:        StatusIdle,
			statusUpdated: base,
			lastActivity:  base,
			wantActivity:  Idle,
		},
		{
			name:          "waiting fresh but no waitingFor -> WaitingForHuman / unknown",
			status:        StatusWaiting,
			waitingFor:    "",
			statusUpdated: base,
			lastActivity:  base.Add(5 * time.Second),
			wantActivity:  WaitingForHuman,
			wantReason:    WaitReasonUnknown,
		},
		{
			name:          "waiting stale BUT awaitingInput still WaitingForHuman via AUQ arm",
			status:        StatusWaiting,
			waitingFor:    "permission prompt",
			statusUpdated: base,
			lastActivity:  base.Add(16 * time.Hour), // waiting flag stale...
			awaitingInput: true,                     // ...but a real dangling question stands
			wantActivity:  WaitingForHuman,
			wantReason:    WaitReasonAskUserQuestion,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := RegistrySession{
				Status:          tc.status,
				WaitingFor:      tc.waitingFor,
				StatusUpdatedAt: tc.statusUpdated,
			}
			got := ClassifyActivity(reg, tc.awaitingInput, tc.lastActivity, freshWindow)
			if got.Activity != tc.wantActivity {
				t.Errorf("Activity = %v (%s), want %v (%s)", got.Activity, got.Activity, tc.wantActivity, tc.wantActivity)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

func TestActivity_String(t *testing.T) {
	cases := map[Activity]string{
		Active:          "active",
		Idle:            "idle",
		WaitingForHuman: "waiting-for-human",
	}
	for a, want := range cases {
		if got := a.String(); got != want {
			t.Errorf("Activity(%d).String() = %q, want %q", a, got, want)
		}
	}
}

// TestLastMessageActivity_ignoresTrailingMetadata verifies the freshness probe
// returns the last REAL message timestamp, skipping trailing metadata/system and
// api-error synthetic events.
func TestLastMessageActivity_ignoresTrailingMetadata(t *testing.T) {
	tsUser := time.Date(2026, 6, 18, 3, 0, 0, 0, time.UTC)
	tsAsst := time.Date(2026, 6, 18, 3, 5, 0, 0, time.UTC) // the real last message
	tsMeta := time.Date(2026, 6, 18, 3, 10, 0, 0, time.UTC)
	tsErr := time.Date(2026, 6, 18, 3, 6, 0, 0, time.UTC)

	line := func(s string) string { return s + "\n" }
	body := line(fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"hi"}}`, tsUser.Format(time.RFC3339Nano))) +
		line(fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`, tsAsst.Format(time.RFC3339Nano))) +
		// an api-error synthetic assistant event AFTER the real message: must NOT count
		line(apiErrorEvent(tsErr, ErrUnknown, "API Error: socket closed")) +
		// trailing metadata records — must all be skipped
		line(`{"type":"mode","mode":"normal"}`) +
		line(`{"type":"permission-mode","permissionMode":"default"}`) +
		line(`{"type":"last-prompt","lastPrompt":"x"}`) +
		line(`{"type":"custom-title","title":"t"}`) +
		line(`{"type":"agent-name","name":"a"}`) +
		line(fmt.Sprintf(`{"type":"queue-operation","timestamp":%q,"op":"add"}`, tsMeta.Format(time.RFC3339Nano))) +
		line(fmt.Sprintf(`{"type":"system","subtype":"turn_duration","timestamp":%q}`, tsMeta.Format(time.RFC3339Nano)))

	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, ok := LastMessageActivity(path)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !got.Equal(tsAsst) {
		t.Errorf("LastMessageActivity = %v, want %v (the real assistant message; metadata + api-error skipped)", got, tsAsst)
	}
}

func TestLastMessageActivity_noMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := `{"type":"mode","mode":"normal"}` + "\n" + `{"type":"system","subtype":"turn_duration","timestamp":"2026-06-18T03:00:00Z"}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastMessageActivity(path); ok {
		t.Error("ok = true, want false (only metadata records present)")
	}
}

func TestPidAlive(t *testing.T) {
	if !PidAlive(os.Getpid()) {
		t.Error("PidAlive(self) = false, want true")
	}
	if PidAlive(0) || PidAlive(-1) {
		t.Error("PidAlive(<=0) = true, want false")
	}
	// A very large pid is almost certainly not a live process.
	if PidAlive(2147483646) {
		t.Error("PidAlive(huge) = true, want false (no such process)")
	}
}
