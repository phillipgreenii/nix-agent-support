package claudetranscript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sessionsFixtureDir = "testdata/sessions/projects"

func utc(y int, m time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, m, d, h, mi, s, 0, time.UTC)
}

func byID(t *testing.T, recs []SessionRecord, id string) SessionRecord {
	t.Helper()
	for _, r := range recs {
		if r.SessionID == id {
			return r
		}
	}
	t.Fatalf("no record with SessionID %q in %+v", id, recs)
	return SessionRecord{}
}

// writeSessionFile writes lines (joined by newlines) to <root>/<slug>/<name>.
func writeSessionFile(t *testing.T, root, slug, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSessions_MissingDirIsNilNil(t *testing.T) {
	recs, err := Sessions(filepath.Join(t.TempDir(), "nope"), time.Time{}, time.Time{})
	if err != nil || recs != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", recs, err)
	}
}

func TestSessions_EmptyTreeIsNilNil(t *testing.T) {
	recs, err := Sessions(t.TempDir(), time.Time{}, time.Time{})
	if err != nil || recs != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", recs, err)
	}
}

// The fixture tree has no sessions/ directory at all (no PID files), yet its
// sessions are returned; status siblings and subagent transcripts are not.
func TestSessions_FixtureTreeOneRecordPerSession(t *testing.T) {
	recs, err := Sessions(sessionsFixtureDir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	var ids []string
	for _, r := range recs {
		ids = append(ids, r.SessionID)
	}
	want := []string{"aaaa", "orig"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("session ids = %v, want %v (status sibling and subagents must not appear)", ids, want)
	}
}

func TestSessions_StraddlesMidnightOneRecordDatedByFirstEvent(t *testing.T) {
	day1s, day2s := utc(2026, 9, 18, 0, 0, 0), utc(2026, 9, 19, 0, 0, 0)
	day3s := utc(2026, 9, 20, 0, 0, 0)

	recs, err := Sessions(sessionsFixtureDir, day1s, day2s)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("day of first event: got %d records, want 1: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.SessionID != "aaaa" {
		t.Errorf("SessionID = %q, want aaaa", r.SessionID)
	}
	if want := utc(2026, 9, 18, 23, 50, 0); !r.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v, want %v", r.StartedAt, want)
	}
	if want := utc(2026, 9, 19, 0, 10, 0); !r.EndedAt.Equal(want) {
		t.Errorf("EndedAt = %v, want %v", r.EndedAt, want)
	}
	if r.StartedAt.Location() != time.UTC || r.EndedAt.Location() != time.UTC {
		t.Errorf("times must be UTC, got %v / %v", r.StartedAt.Location(), r.EndedAt.Location())
	}

	// The next day does not return it: the window test is on StartedAt only.
	recs, err = Sessions(sessionsFixtureDir, day2s, day3s)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("next day: got %+v, want none", recs)
	}
}

func TestSessions_FieldsTurnsAndTokens(t *testing.T) {
	recs, err := Sessions(sessionsFixtureDir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	r := byID(t, recs, "aaaa")
	if r.Cwd != "/work/alpha" || r.Branch != "feature-x" {
		t.Errorf("Cwd/Branch = %q/%q, want /work/alpha / feature-x", r.Cwd, r.Branch)
	}
	// Last assistant event with a real model: the sidechain line (model-two);
	// the later <synthetic> line is ignored.
	if r.Model != "model-two" {
		t.Errorf("Model = %q, want model-two", r.Model)
	}
	// User turns: a-u1 and a-u5. Not turns: tool_result-only, meta, whitespace-only.
	if r.UserTurns != 2 {
		t.Errorf("UserTurns = %d, want 2", r.UserTurns)
	}
	// Assistant turns: msg_1 (two lines, one id) and msg_2; the sidechain
	// message is not a turn.
	if r.AssistantTurns != 2 {
		t.Errorf("AssistantTurns = %d, want 2", r.AssistantTurns)
	}
	// msg_1: largest per category over its two lines; the sidechain message is
	// included in the tally; msg_2 (all zero) contributes nothing.
	wantOne := TokenTotals{Input: 10, Output: 40, CacheRead: 1000, CacheWrite: 100}
	wantTwo := TokenTotals{Input: 3, Output: 7}
	if r.ModelTokens["model-one"] != wantOne {
		t.Errorf("ModelTokens[model-one] = %+v, want %+v", r.ModelTokens["model-one"], wantOne)
	}
	if r.ModelTokens["model-two"] != wantTwo {
		t.Errorf("ModelTokens[model-two] = %+v, want %+v", r.ModelTokens["model-two"], wantTwo)
	}
	if len(r.ModelTokens) != 2 {
		t.Errorf("ModelTokens = %+v, want exactly two models", r.ModelTokens)
	}
	if want := (TokenTotals{Input: 13, Output: 47, CacheRead: 1000, CacheWrite: 100}); r.Tokens != want {
		t.Errorf("Tokens = %+v, want %+v", r.Tokens, want)
	}
	var sum TokenTotals
	for _, tt := range r.ModelTokens {
		sum.add(tt)
	}
	if sum != r.Tokens {
		t.Errorf("per-model tallies sum to %+v, Tokens = %+v", sum, r.Tokens)
	}
	if !strings.HasSuffix(r.TranscriptPath, filepath.Join("-slug-a", "aaaa.jsonl")) {
		t.Errorf("TranscriptPath = %q", r.TranscriptPath)
	}
}

func TestSessions_ResumedSessionIsOneRecord(t *testing.T) {
	recs, err := Sessions(sessionsFixtureDir, utc(2026, 9, 20, 0, 0, 0), utc(2026, 9, 21, 0, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.SessionID != "orig" {
		t.Errorf("SessionID = %q, want the original's id", r.SessionID)
	}
	if want := utc(2026, 9, 20, 9, 0, 0); !r.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v, want %v", r.StartedAt, want)
	}
	if want := utc(2026, 9, 20, 9, 31, 0); !r.EndedAt.Equal(want) {
		t.Errorf("EndedAt = %v, want %v", r.EndedAt, want)
	}
	// o-1, o-3 (replayed, counted once) and r-1.
	if r.UserTurns != 3 {
		t.Errorf("UserTurns = %d, want 3 (replayed events must not double-count)", r.UserTurns)
	}
	if r.AssistantTurns != 2 {
		t.Errorf("AssistantTurns = %d, want 2", r.AssistantTurns)
	}
	if want := (TokenTotals{Input: 25, Output: 36}); r.Tokens != want {
		t.Errorf("Tokens = %+v, want %+v (replayed message counted once)", r.Tokens, want)
	}
	if !strings.HasSuffix(r.TranscriptPath, "orig.jsonl") {
		t.Errorf("TranscriptPath = %q, want the earliest member (orig.jsonl)", r.TranscriptPath)
	}
	if r.Cwd != "/work/beta" || r.Branch != "main" {
		t.Errorf("Cwd/Branch = %q/%q", r.Cwd, r.Branch)
	}
}

// A resumed file whose first event is LATER than the original's (the replay
// does not start at the beginning) still resolves to the original's id.
func TestSessions_ResumeEarliestMemberWinsRegardlessOfFileName(t *testing.T) {
	root := t.TempDir()
	writeSessionFile(t, root, "s", "zzz.jsonl",
		`{"type":"user","uuid":"u1","timestamp":"2026-09-01T10:00:00Z","sessionId":"zzz","message":{"role":"user","content":"hi"}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-09-01T10:05:00Z","sessionId":"zzz","message":{"role":"user","content":"again"}}`)
	writeSessionFile(t, root, "s", "aaa.jsonl",
		`{"type":"user","uuid":"u2","timestamp":"2026-09-01T10:05:00Z","sessionId":"aaa","message":{"role":"user","content":"again"}}`,
		`{"type":"user","uuid":"u3","timestamp":"2026-09-01T11:00:00Z","sessionId":"aaa","message":{"role":"user","content":"more"}}`)
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.SessionID != "zzz" || !strings.HasSuffix(r.TranscriptPath, "zzz.jsonl") {
		t.Errorf("got id %q path %q, want the earliest member zzz", r.SessionID, r.TranscriptPath)
	}
	if r.UserTurns != 3 {
		t.Errorf("UserTurns = %d, want 3", r.UserTurns)
	}
}

// Two files sharing a canonical session id (no shared uuid) are one session.
func TestSessions_SharedCanonicalIDMerges(t *testing.T) {
	root := t.TempDir()
	writeSessionFile(t, root, "s", "one.jsonl",
		`{"type":"user","uuid":"x1","timestamp":"2026-09-01T10:00:00Z","sessionId":"same","message":{"role":"user","content":"a"}}`)
	writeSessionFile(t, root, "t", "two.jsonl",
		`{"type":"user","uuid":"x2","timestamp":"2026-09-01T12:00:00Z","sessionId":"same","message":{"role":"user","content":"b"}}`)
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].UserTurns != 2 || recs[0].SessionID != "same" {
		t.Fatalf("got %+v, want one merged record with 2 user turns", recs)
	}
}

func TestSessions_UnrelatedFilesStaySeparateAndOrdered(t *testing.T) {
	root := t.TempDir()
	ts := "2026-09-01T10:00:00Z"
	writeSessionFile(t, root, "s", "b.jsonl",
		`{"type":"user","uuid":"b1","timestamp":"`+ts+`","sessionId":"b","message":{"role":"user","content":"x"}}`)
	writeSessionFile(t, root, "s", "a.jsonl",
		`{"type":"user","uuid":"a1","timestamp":"`+ts+`","sessionId":"a","message":{"role":"user","content":"x"}}`)
	writeSessionFile(t, root, "s", "early.jsonl",
		`{"type":"user","uuid":"e1","timestamp":"2026-08-31T10:00:00Z","sessionId":"early","message":{"role":"user","content":"x"}}`)
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range recs {
		ids = append(ids, r.SessionID)
	}
	if got := strings.Join(ids, ","); got != "early,a,b" {
		t.Fatalf("order = %s, want early,a,b (StartedAt asc, ties by SessionID)", got)
	}
}

func TestSessions_WindowBoundsInclusiveSinceExclusiveBefore(t *testing.T) {
	root := t.TempDir()
	writeSessionFile(t, root, "s", "w.jsonl",
		`{"type":"user","uuid":"w1","timestamp":"2026-09-01T10:00:00Z","sessionId":"w","message":{"role":"user","content":"x"}}`)
	at := utc(2026, 9, 1, 10, 0, 0)
	cases := []struct {
		name          string
		since, before time.Time
		want          int
	}{
		{"since equals start is in", at, time.Time{}, 1},
		{"before equals start is out", time.Time{}, at, 0},
		{"since after start is out", at.Add(time.Second), time.Time{}, 0},
		{"both open", time.Time{}, time.Time{}, 1},
		{"half-open window containing start", at.Add(-time.Hour), at.Add(time.Second), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recs, err := Sessions(root, c.since, c.before)
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != c.want {
				t.Fatalf("got %d records, want %d", len(recs), c.want)
			}
		})
	}
}

func TestSessions_UsesOlderFileThroughResumeEvenWhenModTimeIsOld(t *testing.T) {
	root := t.TempDir()
	p := writeSessionFile(t, root, "s", "old.jsonl",
		`{"type":"user","uuid":"k1","timestamp":"2026-09-01T10:00:00Z","sessionId":"a-old","message":{"role":"user","content":"x"}}`)
	old := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	writeSessionFile(t, root, "s", "new.jsonl",
		`{"type":"user","uuid":"k1","timestamp":"2026-09-01T10:00:00Z","sessionId":"z-new","message":{"role":"user","content":"x"}}`,
		`{"type":"user","uuid":"k2","timestamp":"2026-09-02T10:00:00Z","sessionId":"z-new","message":{"role":"user","content":"y"}}`)
	// A window that opens after the old file's mtime still finds the session
	// only if the old file was read: its StartedAt comes from it.
	recs, err := Sessions(root, utc(2026, 9, 1, 0, 0, 0), utc(2026, 9, 2, 0, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "a-old" {
		t.Fatalf("got %+v, want the session resolved through the old file", recs)
	}
}

func TestSessions_CanonicalIDMostFrequentTiesSmaller(t *testing.T) {
	root := t.TempDir()
	writeSessionFile(t, root, "s", "file-name.jsonl",
		`{"type":"user","uuid":"c1","timestamp":"2026-09-01T10:00:00Z","sessionId":"zeta","message":{"role":"user","content":"x"}}`,
		`{"type":"user","uuid":"c2","timestamp":"2026-09-01T10:01:00Z","sessionId":"alpha","message":{"role":"user","content":"x"}}`)
	writeSessionFile(t, root, "s", "fallback-name.jsonl",
		`{"type":"user","uuid":"d1","timestamp":"2026-09-02T10:00:00Z","message":{"role":"user","content":"x"}}`)
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %+v", recs)
	}
	if recs[0].SessionID != "alpha" {
		t.Errorf("tie id = %q, want the lexicographically smaller alpha", recs[0].SessionID)
	}
	if recs[1].SessionID != "fallback-name" {
		t.Errorf("no-sessionId id = %q, want the file name without .jsonl", recs[1].SessionID)
	}
}

func TestSessions_NoDatedEventNoRecordAndEmptyModelStaysEmpty(t *testing.T) {
	root := t.TempDir()
	writeSessionFile(t, root, "s", "undated.jsonl",
		`{"type":"last-prompt","sessionId":"undated"}`,
		`{"type":"user","uuid":"n1","sessionId":"undated","message":{"role":"user","content":"x"}}`,
		`{"type":"user","uuid":"n2","timestamp":"not-a-time","sessionId":"undated","message":{"role":"user","content":"x"}}`)
	writeSessionFile(t, root, "s", "nomodel.jsonl",
		`{"type":"user","uuid":"m1","timestamp":"2026-09-01T10:00:00Z","sessionId":"nomodel","message":{"role":"user","content":"x"}}`,
		`{"type":"assistant","uuid":"m2","timestamp":"2026-09-01T10:01:00Z","sessionId":"nomodel","message":{"role":"assistant","content":[{"type":"text","text":"y"}]}}`)
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "nomodel" {
		t.Fatalf("got %+v, want only nomodel", recs)
	}
	if recs[0].Model != "" {
		t.Errorf("Model = %q, want empty", recs[0].Model)
	}
	// An id-less assistant line counts on its own as a turn.
	if recs[0].AssistantTurns != 1 {
		t.Errorf("AssistantTurns = %d, want 1", recs[0].AssistantTurns)
	}
}

func TestSessions_IDLessAssistantLinesCountOnTheirOwn(t *testing.T) {
	root := t.TempDir()
	writeSessionFile(t, root, "s", "noid.jsonl",
		`{"type":"assistant","uuid":"p1","timestamp":"2026-09-01T10:00:00Z","sessionId":"noid","message":{"model":"m","role":"assistant","content":[],"usage":{"input_tokens":4,"output_tokens":6}}}`,
		`{"type":"assistant","uuid":"p2","timestamp":"2026-09-01T10:01:00Z","sessionId":"noid","message":{"model":"m","role":"assistant","content":[],"usage":{"input_tokens":4,"output_tokens":6}}}`)
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %+v", recs)
	}
	if recs[0].AssistantTurns != 2 || recs[0].Tokens != (TokenTotals{Input: 8, Output: 12}) {
		t.Errorf("got turns=%d tokens=%+v, want 2 and {8 12 0 0}", recs[0].AssistantTurns, recs[0].Tokens)
	}
}

func TestSessions_OnlyDepthTwoJSONLAreTranscripts(t *testing.T) {
	root := t.TempDir()
	line := `{"type":"user","uuid":"q1","timestamp":"2026-09-01T10:00:00Z","sessionId":"%s","message":{"role":"user","content":"x"}}`
	mk := func(rel, id string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(line, "%s", id, 1)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("top-level.jsonl", "toplevel")          // depth 1
	mk("slug/real.jsonl", "real")              // depth 2: the only transcript
	mk("slug/real.status.jsonl", "statusfile") // sibling
	mk("slug/real.txt", "nonjsonl")            // not .jsonl
	mk("slug/real/subagents/a.jsonl", "sub")   // depth 4
	mk("slug/deeper/x.jsonl", "deeper")        // depth 3
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "real" {
		t.Fatalf("got %+v, want only real", recs)
	}
}

func TestSessions_UnparsableAndOversizedLinesDoNotStopTheScan(t *testing.T) {
	root := t.TempDir()
	// A 1.5 MiB line (over bufio's default and over 1 MiB), then more events.
	big := `{"type":"user","uuid":"big","timestamp":"2026-09-01T10:01:00Z","sessionId":"long","message":{"role":"user","content":"` +
		strings.Repeat("x", 1536*1024) + `"}}`
	writeSessionFile(t, root, "s", "long.jsonl",
		`{"type":"user","uuid":"l1","timestamp":"2026-09-01T10:00:00Z","sessionId":"long","message":{"role":"user","content":"first"}}`,
		`{{{ broken`,
		big,
		`{"type":"user","uuid":"l3","timestamp":"2026-09-01T10:02:00Z","sessionId":"long","message":{"role":"user","content":"after"}}`)
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %+v", recs)
	}
	if recs[0].UserTurns != 3 || !recs[0].EndedAt.Equal(utc(2026, 9, 1, 10, 2, 0)) {
		t.Errorf("turns=%d ended=%v, want 3 and 10:02:00 (scan must continue past the long line)", recs[0].UserTurns, recs[0].EndedAt)
	}
}

func TestSessions_UnreadableFileIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any file")
	}
	root := t.TempDir()
	p := writeSessionFile(t, root, "s", "locked.jsonl",
		`{"type":"user","uuid":"z1","timestamp":"2026-09-01T10:00:00Z","sessionId":"locked","message":{"role":"user","content":"x"}}`)
	writeSessionFile(t, root, "s", "ok.jsonl",
		`{"type":"user","uuid":"z2","timestamp":"2026-09-01T11:00:00Z","sessionId":"ok","message":{"role":"user","content":"x"}}`)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	recs, err := Sessions(root, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].SessionID != "ok" {
		t.Fatalf("got %+v, want only ok", recs)
	}
}
