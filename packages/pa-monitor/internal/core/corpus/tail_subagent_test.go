package corpus

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// apiErrorLine emits an assistant isApiErrorMessage event (the shape
// ct.LastAPIError matches).
func apiErrorLine(kind, text, ts string) string {
	return `{"type":"assistant","isApiErrorMessage":true,"error":"` + kind +
		`","timestamp":"` + ts + `","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

func userTsLine(ts string) string {
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":[]}}`
}

// subagentsDirFor returns the subagents dir for a main transcript path and
// creates it: "<dir>/<sid>.jsonl" -> "<dir>/<sid>/subagents".
func subagentsDirFor(t *testing.T, mainPath string) string {
	t.Helper()
	sub := mainPath[:len(mainPath)-len(".jsonl")] + "/subagents"
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	return sub
}

func TestSubagentTail_MatchesLastSubagentError(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl")
	if err := os.WriteFile(main, []byte(userPromptLine("hi")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1000, 0),
		apiErrorLine("server_error", "API Error: 500", "2026-07-20T10:00:00Z"))

	want, wantOK := transcript.LastSubagentError(main)

	st := newSubagentTail()
	got, _ := st.fold("sid", main)
	if !wantOK {
		t.Fatalf("LastSubagentError returned ok=false; test fixture wrong")
	}
	if got == nil || *got != want {
		t.Fatalf("fold = %+v, want %+v (must match ct.LastSubagentError exactly)", got, want)
	}
	if !got.FromSubagent {
		t.Fatalf("FromSubagent not set")
	}
}

func TestSubagentTail_ContextLimitPreserved(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl")
	os.WriteFile(main, []byte("\n"), 0o600)
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1000, 0),
		apiErrorLine("invalid_request", "API Error: prompt is too long: 250000 tokens > 200000 maximum", "2026-07-20T10:00:00Z"))

	st := newSubagentTail()
	got, _ := st.fold("sid", main)
	if got == nil || !got.IsContextLimit {
		t.Fatalf("fold = %+v, want IsContextLimit=true (guards B1: must use ct.LastAPIError, not scanState)", got)
	}
}

func TestSubagentTail_SurfacesLatestTerminal(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl")
	os.WriteFile(main, []byte("\n"), 0o600)
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1000, 0),
		apiErrorLine("server_error", "old", "2026-07-20T09:00:00Z"))
	writeTranscript(t, sub, "agent-2.jsonl", time.Unix(1000, 0),
		apiErrorLine("rate_limit", "new", "2026-07-20T11:00:00Z"))

	st := newSubagentTail()
	got, _ := st.fold("sid", main)
	if got == nil || got.Kind != transcript.ErrRateLimit {
		t.Fatalf("fold = %+v, want the later (rate_limit) terminal error", got)
	}
}

func TestSubagentTail_RecoveryClearsTerminal(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl")
	os.WriteFile(main, []byte("\n"), 0o600)
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1000, 0),
		apiErrorLine("server_error", "err", "2026-07-20T10:00:00Z"),
		userTsLine("2026-07-20T10:05:00Z")) // recovery: later non-error line

	st := newSubagentTail()
	got, _ := st.fold("sid", main)
	if got != nil {
		t.Fatalf("fold = %+v, want nil (recovered error is not terminal)", got)
	}
}

func TestSubagentTail_SameMtimeAppendReRead(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl")
	os.WriteFile(main, []byte("\n"), 0o600)
	sub := subagentsDirFor(t, main)
	agent := writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1000, 0),
		userTsLine("2026-07-20T09:00:00Z")) // no error yet

	st := newSubagentTail()
	if got, _ := st.fold("sid", main); got != nil {
		t.Fatalf("expected no error before append, got %+v", got)
	}
	// Append an error but keep the SAME mtime — size grows, so the (size,mtime)
	// key must still detect the change and re-read (guards S7).
	appendLines(t, agent, time.Unix(1000, 0),
		apiErrorLine("server_error", "boom", "2026-07-20T10:00:00Z"))
	got, _ := st.fold("sid", main)
	if got == nil || got.Kind != transcript.ErrServerError {
		t.Fatalf("fold after same-mtime append = %+v, want the appended server_error", got)
	}
}

func TestSubagentTail_UnchangedFileNotReRead(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl")
	os.WriteFile(main, []byte("\n"), 0o600)
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1000, 0),
		apiErrorLine("server_error", "boom", "2026-07-20T10:00:00Z"))

	st := newSubagentTail()
	st.fold("sid", main)
	readsAfterFirst := st.reads
	if readsAfterFirst == 0 {
		t.Fatalf("expected the agent file to be read once")
	}
	st.fold("sid", main) // unchanged size+mtime -> cache reused
	if st.reads != readsAfterFirst {
		t.Fatalf("unchanged agent file re-read: reads %d -> %d", readsAfterFirst, st.reads)
	}
}

func TestSubagentTail_MissingSubdirGraceful(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl") // no subagents dir created
	os.WriteFile(main, []byte("\n"), 0o600)
	st := newSubagentTail()
	got, maxMtime := st.fold("sid", main)
	if got != nil || !maxMtime.IsZero() {
		t.Fatalf("fold with missing subdir = (%+v, %v), want (nil, zero)", got, maxMtime)
	}
	// Empty resolved path is also graceful.
	got2, _ := st.fold("sid", "")
	if got2 != nil {
		t.Fatalf("fold with empty path = %+v, want nil", got2)
	}
}

func TestSubagentTail_MaxActivity(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl")
	os.WriteFile(main, []byte("\n"), 0o600)
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", time.Unix(5000, 0), userTsLine("2026-07-20T09:00:00Z"))
	writeTranscript(t, sub, "agent-2.jsonl", time.Unix(9000, 0), userTsLine("2026-07-20T09:00:00Z"))

	st := newSubagentTail()
	_, maxMtime := st.fold("sid", main)
	if !maxMtime.Equal(time.Unix(9000, 0)) {
		t.Fatalf("maxSubagentMtime = %v, want %v (newest agent file)", maxMtime, time.Unix(9000, 0))
	}
}

func TestSubagentTail_PruneByActiveIDs(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sid.jsonl")
	os.WriteFile(main, []byte("\n"), 0o600)
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", time.Unix(1000, 0),
		apiErrorLine("server_error", "boom", "2026-07-20T10:00:00Z"))

	st := newSubagentTail()
	st.fold("sid", main)
	if len(st.cache) == 0 {
		t.Fatalf("expected cache populated")
	}
	st.prune(map[string]bool{}) // sid no longer active
	if len(st.cache) != 0 {
		t.Fatalf("prune did not drop inactive session cache: %d entries", len(st.cache))
	}
}
