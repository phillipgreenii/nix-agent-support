package corpus

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// --- local corpus builders (test files are not importable across packages) ---

// projectDir returns claudeHome/projects/<slug(cwd)> and creates it.
func projectDir(t *testing.T, claudeHome, cwd string) string {
	t.Helper()
	s := &session.Session{Cwd: cwd, SessionID: "x"}
	dir := filepath.Dir(s.TranscriptPath(claudeHome)) // claudeHome/projects/<slug>
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

func titleLine(title string) string {
	return `{"type":"custom-title","customTitle":"` + title + `"}`
}

// fillerLines returns n non-title JSONL lines.
func fillerLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = `{"type":"user","message":{"role":"user","content":[]}}`
	}
	return out
}

// writeTranscript writes lines to dir/<name> and sets its mtime.
func writeTranscript(t *testing.T, dir, name string, mtime time.Time, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

// --- tests ---

func TestResolve_TitleAtLine500(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	// A file whose custom-title lands at ~line 500 — beyond the old dead
	// titleScanLines=200 cap. The write-once title cache must still find it.
	lines := append(fillerLines(500), titleLine("MySession"))
	titled := writeTranscript(t, dir, "aaaa.jsonl", time.Unix(1000, 0), lines...)
	// A newer, untitled file that would win the newest fallback.
	writeTranscript(t, dir, "bbbb.jsonl", time.Unix(2000, 0), fillerLines(3)...)

	s := &session.Session{Cwd: cwd, Name: "MySession", SessionID: "aaaa"}
	tc := newTitleCache()
	got, _, ok := tc.resolve(home, s)
	if !ok || got != titled {
		t.Fatalf("resolve = (%q, ok=%v), want %q (title at line 500 must match)", got, ok, titled)
	}
}

func TestResolve_SessionIDArm(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	sidFile := writeTranscript(t, dir, "sid123.jsonl", time.Unix(1000, 0), fillerLines(3)...)
	writeTranscript(t, dir, "other.jsonl", time.Unix(2000, 0), fillerLines(3)...) // newer

	s := &session.Session{Cwd: cwd, Name: "", SessionID: "sid123"}
	tc := newTitleCache()
	got, _, ok := tc.resolve(home, s)
	if !ok || got != sidFile {
		t.Fatalf("resolve = (%q, ok=%v), want %q (SessionID arm before newest fallback)", got, ok, sidFile)
	}
}

func TestResolve_NewestFallback(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	writeTranscript(t, dir, "old.jsonl", time.Unix(1000, 0), fillerLines(3)...)
	newest := writeTranscript(t, dir, "new.jsonl", time.Unix(2000, 0), fillerLines(3)...)

	s := &session.Session{Cwd: cwd, Name: "", SessionID: "nomatch"}
	tc := newTitleCache()
	got, _, ok := tc.resolve(home, s)
	if !ok || got != newest {
		t.Fatalf("resolve = (%q, ok=%v), want %q (newest fallback)", got, ok, newest)
	}
}

func TestResolve_ExcludesStatusSibling(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	real := writeTranscript(t, dir, "real.jsonl", time.Unix(1000, 0), fillerLines(3)...)
	// A NEWER .status.jsonl sibling must never be selected (ADR 0021 §2).
	writeTranscript(t, dir, "real.status.jsonl", time.Unix(9000, 0), fillerLines(3)...)

	s := &session.Session{Cwd: cwd, Name: "", SessionID: "nomatch"}
	tc := newTitleCache()
	got, _, ok := tc.resolve(home, s)
	if !ok || got != real {
		t.Fatalf("resolve = (%q, ok=%v), want %q (status sibling excluded)", got, ok, real)
	}
}

func TestResolve_MultiSessionSharedCwd(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	fileA := writeTranscript(t, dir, "a.jsonl", time.Unix(1000, 0), append(fillerLines(10), titleLine("Alpha"))...)
	fileB := writeTranscript(t, dir, "b.jsonl", time.Unix(2000, 0), append(fillerLines(10), titleLine("Beta"))...)

	tc := newTitleCache()
	gotA, _, okA := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "a"})
	gotB, _, okB := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Beta", SessionID: "b"})
	if !okA || gotA != fileA {
		t.Fatalf("Alpha resolve = (%q, ok=%v), want %q", gotA, okA, fileA)
	}
	if !okB || gotB != fileB {
		t.Fatalf("Beta resolve = (%q, ok=%v), want %q", gotB, okB, fileB)
	}
}

func TestResolve_MissingDir(t *testing.T) {
	home := t.TempDir() // no projects/<slug> created
	s := &session.Session{Cwd: "/tmp/missing", Name: "", SessionID: "x"}
	tc := newTitleCache()
	_, _, ok := tc.resolve(home, s)
	if ok {
		t.Fatalf("resolve ok=true for missing dir, want false")
	}
}

// TestResolve_MatchesSessionResolveTranscript cross-checks the new resolver
// against the authoritative session.ResolveTranscript on cases where they MUST
// agree (title within the old 200-line cap, plus an equal-mtime tie — guards the
// candidate-ordering/tie-break concern S4).
func TestResolve_MatchesSessionResolveTranscript(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	// Equal-mtime candidates, one titled within 200 lines.
	eq := time.Unix(1000, 0)
	writeTranscript(t, dir, "c1.jsonl", eq, append(fillerLines(5), titleLine("Shared"))...)
	writeTranscript(t, dir, "c2.jsonl", eq, fillerLines(5)...)

	s := &session.Session{Cwd: cwd, Name: "Shared", SessionID: "c2"}
	wantPath, wantMtime, wantOK := session.ResolveTranscript(home, s)

	tc := newTitleCache()
	gotPath, gotMtime, gotOK := tc.resolve(home, s)
	if gotOK != wantOK || gotPath != wantPath || !gotMtime.Equal(wantMtime) {
		t.Fatalf("resolve = (%q, %v, %v); session.ResolveTranscript = (%q, %v, %v)",
			gotPath, gotMtime, gotOK, wantPath, wantMtime, wantOK)
	}
}

func TestTitleCache_NotReprobedOnMtimeBump(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	titled := writeTranscript(t, dir, "a.jsonl", time.Unix(1000, 0), append(fillerLines(10), titleLine("Alpha"))...)

	s := &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "a"}
	tc := newTitleCache()
	if _, _, ok := tc.resolve(home, s); !ok {
		t.Fatalf("first resolve failed")
	}
	opensAfterFirst := tc.opens
	if opensAfterFirst == 0 {
		t.Fatalf("expected the titled file to be opened once on first resolve")
	}
	// Bump mtime WITHOUT changing size/content; an unchanged file must not be re-opened.
	if err := os.Chtimes(titled, time.Unix(5000, 0), time.Unix(5000, 0)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, _, ok := tc.resolve(home, s); !ok {
		t.Fatalf("second resolve failed")
	}
	if tc.opens != opensAfterFirst {
		t.Fatalf("title re-probed after mtime bump: opens %d -> %d (want no change)", opensAfterFirst, tc.opens)
	}
}

// appendTranscript appends raw bytes to path (no newline added).
func appendTranscript(t *testing.T, path, raw string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

// A title can change: Claude Code appends a new custom-title record. The cache
// keeps tailing, so the file resolves by its CURRENT (latest) title.
func TestTitleCache_TitleChangeIsPickedUp(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	renamed := writeTranscript(t, dir, "a.jsonl", time.Unix(1000, 0), append(fillerLines(5), titleLine("Alpha"))...)
	other := writeTranscript(t, dir, "other.jsonl", time.Unix(2000, 0), fillerLines(3)...)

	tc := newTitleCache()
	if got, _, ok := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "zz"}); !ok || got != renamed {
		t.Fatalf("before rename: resolve(Alpha) = (%q, ok=%v), want %q", got, ok, renamed)
	}

	appendTranscript(t, renamed, titleLine("Beta")+"\n")
	if err := os.Chtimes(renamed, time.Unix(3000, 0), time.Unix(3000, 0)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// other is now the newest file, so a miss on a's title falls back to it.
	if err := os.Chtimes(other, time.Unix(4000, 0), time.Unix(4000, 0)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if got, _, ok := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Beta", SessionID: "zz"}); !ok || got != renamed {
		t.Fatalf("after rename: resolve(Beta) = (%q, ok=%v), want %q (latest title wins)", got, ok, renamed)
	}
	if got, _, ok := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "zz"}); !ok || got != other {
		t.Fatalf("after rename: resolve(Alpha) = (%q, ok=%v), want fallback %q (old title must not match)", got, ok, other)
	}
}

// A growing file is read only from where the previous probe stopped, whether or
// not it carries a title yet.
func TestTitleCache_GrowthReadsOnlyAppendedBytes(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	path := writeTranscript(t, dir, "a.jsonl", time.Unix(1000, 0), fillerLines(500)...)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	s := &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "a"}
	tc := newTitleCache()
	tc.resolve(home, s)
	if tc.bytes != fi.Size() {
		t.Fatalf("first probe read %d bytes, want the whole file (%d)", tc.bytes, fi.Size())
	}

	appended := fillerLines(1)[0] + "\n"
	appendTranscript(t, path, appended)
	before := tc.bytes
	tc.resolve(home, s)
	if got := tc.bytes - before; got != int64(len(appended)) {
		t.Fatalf("growth probe read %d bytes, want only the appended %d", got, len(appended))
	}
}

// Only lines that mention custom-title are JSON-decoded; every other line is
// skipped by a byte scan.
func TestTitleCache_DecodesOnlyCandidateLines(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	mention := `{"type":"user","message":{"role":"user","content":"what is a custom-title record?"}}`
	lines := append(fillerLines(500), titleLine("Alpha"), mention)
	writeTranscript(t, dir, "a.jsonl", time.Unix(1000, 0), lines...)

	tc := newTitleCache()
	tc.resolve(home, &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "a"})
	if tc.decodes != 2 {
		t.Fatalf("decoded %d lines, want 2 (the title record and the line mentioning it)", tc.decodes)
	}
}

// A file rewritten in place (truncated, same inode) is re-read from the start.
func TestTitleCache_RewrittenFileIsRescanned(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	path := writeTranscript(t, dir, "a.jsonl", time.Unix(1000, 0), append(fillerLines(20), titleLine("Alpha"))...)
	other := writeTranscript(t, dir, "other.jsonl", time.Unix(2000, 0), fillerLines(3)...)

	tc := newTitleCache()
	if got, _, ok := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "zz"}); !ok || got != path {
		t.Fatalf("resolve(Alpha) = (%q, ok=%v), want %q", got, ok, path)
	}

	if err := os.WriteFile(path, []byte(titleLine("Gamma")+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	// The rewrite stamped a with the current time; make other newer so a title miss falls back to it.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(other, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if got, _, ok := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Gamma", SessionID: "zz"}); !ok || got != path {
		t.Fatalf("after rewrite: resolve(Gamma) = (%q, ok=%v), want %q", got, ok, path)
	}
	if got, _, ok := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "zz"}); !ok || got != other {
		t.Fatalf("after rewrite: resolve(Alpha) = (%q, ok=%v), want fallback %q (old title gone)", got, ok, other)
	}
}

// A half-written final line is not consumed; the completed line is read once.
func TestTitleCache_PartialTrailingLineWaitsForNewline(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	dir := projectDir(t, home, cwd)
	path := writeTranscript(t, dir, "a.jsonl", time.Unix(1000, 0), fillerLines(3)...)
	other := writeTranscript(t, dir, "other.jsonl", time.Unix(2000, 0), fillerLines(3)...)

	tc := newTitleCache()
	appendTranscript(t, path, `{"type":"custom-title","customTitle":"Al`)
	// The append stamped a with the current time; make other newer so a title miss falls back to it.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(other, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if got, _, ok := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "zz"}); !ok || got != other {
		t.Fatalf("partial line: resolve(Alpha) = (%q, ok=%v), want fallback %q", got, ok, other)
	}

	appendTranscript(t, path, `pha"}`+"\n")
	if got, _, ok := tc.resolve(home, &session.Session{Cwd: cwd, Name: "Alpha", SessionID: "zz"}); !ok || got != path {
		t.Fatalf("completed line: resolve(Alpha) = (%q, ok=%v), want %q", got, ok, path)
	}
}
