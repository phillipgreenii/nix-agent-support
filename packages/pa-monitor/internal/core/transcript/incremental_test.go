package transcript

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Fixtures exercise every feature Scan computes. Each is a slice of complete
// JSONL lines; helpers join them (with a trailing newline) or append them in
// chunks to drive the incremental path.
var incFixtures = map[string][]string{
	"basic": {
		`{"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"fix the bug in login"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Task"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":500,"output_tokens":50}}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-04-23T10:00:10Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"done"}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-04-23T10:00:15Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":700,"output_tokens":20}}}`,
	},
	// Last assistant leaves an unanswered AskUserQuestion -> AwaitingInput true.
	"awaiting": {
		`{"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"do a thing"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"tool_use","id":"auq_1","name":"AskUserQuestion"}],"usage":{"input_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":7}}}`,
	},
	// An open Task subagent that is never closed -> SubagentCount 1.
	"subagent_open": {
		`{"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"spawn work"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"tool_use","id":"tu_9","name":"Task"}],"usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`,
	},
	// Legacy system api_error rate_limit_error with retryInMs -> deterministic
	// RateLimitResetsAt = timestamp + retryInMs, no subsequent resume.
	"ratelimit_legacy": {
		`{"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"text","text":"working"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":4}}}`,
		`{"type":"system","subtype":"api_error","uuid":"s1","timestamp":"2026-04-23T10:00:10Z","retryInMs":60000,"error":{"error":{"type":"rate_limit_error"}}}`,
	},
	// Synthetic assistant api-error (non-rate-limit) with no later resume ->
	// LastError{server_error, IsTerminal true}.
	"lasterror_terminal": {
		`{"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-opus-4-7","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":4}}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-04-23T10:00:10Z","isApiErrorMessage":true,"error":"server_error","message":{"role":"assistant","content":[{"type":"text","text":"upstream boom"}]}}`,
	},
	// Same as above but a later user turn resumes -> LastError.IsTerminal false.
	"lasterror_resumed": {
		`{"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-04-23T10:00:10Z","isApiErrorMessage":true,"error":"server_error","message":{"role":"assistant","content":[{"type":"text","text":"upstream boom"}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-04-23T10:00:20Z","message":{"role":"user","content":"try again"}}`,
	},
	// Interleaves non-object / malformed lines that must be skipped identically.
	"non_object_lines": {
		`{"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"start"}}`,
		`123`,
		`"a bare string"`,
		`null`,
		`{ this is not valid json`,
		`[]`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}`,
	},
}

func joinLines(lines []string) string {
	var b []byte
	for _, l := range lines {
		b = append(b, l...)
		b = append(b, '\n')
	}
	return string(b)
}

func writeAll(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendAll(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open-append %s: %v", path, err)
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		t.Fatalf("append %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// foldPartition writes the fixture's lines in the given contiguous line-index
// groups, calling ScanIncremental after each append, and returns the final
// Snapshot. cuts are the group boundaries (exclusive upper indices).
func foldPartition(t *testing.T, lines []string, cuts []int) Snapshot {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	var acc *Accumulator
	var last Snapshot
	start := 0
	bounds := append(append([]int{}, cuts...), len(lines))
	for _, end := range bounds {
		if end < start {
			t.Fatalf("bad cut %d < %d", end, start)
		}
		appendAll(t, path, joinLines(lines[start:end]))
		snap, next, err := ScanIncremental(path, acc)
		if err != nil {
			t.Fatalf("ScanIncremental: %v", err)
		}
		acc, last = next, snap
		start = end
	}
	return last
}

func snapEqual(t *testing.T, got, want Snapshot) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// The headline acceptance property: incremental folding across arbitrary append
// boundaries must equal a cold full re-parse of the final file.
func TestScanIncremental_AppendEqualsFullScan(t *testing.T) {
	for name, lines := range incFixtures {
		full := joinLines(lines)
		fullPath := filepath.Join(t.TempDir(), "full.jsonl")
		writeAll(t, fullPath, full)
		want, err := Scan(fullPath)
		if err != nil {
			t.Fatalf("%s: Scan: %v", name, err)
		}
		n := len(lines)
		partitions := map[string][]int{
			"all-at-once":  {},
			"line-by-line": seq(n),
			"halves":       {n / 2},
			"thirds":       {n / 3, 2 * n / 3},
		}
		for pname, cuts := range partitions {
			t.Run(name+"/"+pname, func(t *testing.T) {
				got := foldPartition(t, lines, cuts)
				snapEqual(t, got, want)
			})
		}
	}
}

// seq returns 1..n-1 (interior line boundaries).
func seq(n int) []int {
	var s []int
	for i := 1; i < n; i++ {
		s = append(s, i)
	}
	return s
}

// No-cache path must equal Scan, AND match the independent per-feature oracle
// functions (which are NOT part of the refactor) so a shared fold bug is caught.
func TestScanIncremental_FromNil_MatchesScanAndOracles(t *testing.T) {
	for name, lines := range incFixtures {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.jsonl")
			writeAll(t, path, joinLines(lines))

			want, err := Scan(path)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			got, acc, err := ScanIncremental(path, nil)
			if err != nil {
				t.Fatalf("ScanIncremental: %v", err)
			}
			if acc == nil {
				t.Fatalf("ScanIncremental returned nil accumulator")
			}
			snapEqual(t, got, want)

			// Independent oracles.
			if fp, _ := FirstPrompt(path); got.FirstPrompt != fp {
				t.Errorf("FirstPrompt: got %q want %q", got.FirstPrompt, fp)
			}
			ctx, _ := LatestContext(path)
			if got.Model != ctx.Model || got.ContextTokens != ctx.ContextTokens || got.TotalTokens != ctx.TotalTokens {
				t.Errorf("context: got (%q,%d,%d) want (%q,%d,%d)",
					got.Model, got.ContextTokens, got.TotalTokens,
					ctx.Model, ctx.ContextTokens, ctx.TotalTokens)
			}
			if subs, _ := OpenSubagents(path); got.SubagentCount != subs {
				t.Errorf("SubagentCount: got %d want %d", got.SubagentCount, subs)
			}
			if aw, _ := IsAwaitingInput(path); got.AwaitingInput != aw {
				t.Errorf("AwaitingInput: got %v want %v", got.AwaitingInput, aw)
			}
			if rl, _ := RateLimitPause(path); !got.RateLimitResetsAt.Equal(rl) {
				t.Errorf("RateLimitResetsAt: got %v want %v", got.RateLimitResetsAt, rl)
			}
		})
	}
}

// A read that lands mid-line: incremental must equal a full Scan of the same
// half-written file (both ignore the partial trailing line), then equal the
// full Scan once the remainder is appended.
func TestScanIncremental_MidLinePartialRead(t *testing.T) {
	lines := incFixtures["basic"]
	full := joinLines(lines)
	// Cut inside the 2nd line (after the first newline + a few bytes).
	k := len(lines[0]) + 1 + 20
	if k >= len(full) {
		t.Fatal("bad cut")
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeAll(t, path, full[:k])

	partialWantPath := filepath.Join(t.TempDir(), "partial.jsonl")
	writeAll(t, partialWantPath, full[:k])
	partialWant, err := Scan(partialWantPath)
	if err != nil {
		t.Fatal(err)
	}
	got1, acc, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapEqual(t, got1, partialWant)

	appendAll(t, path, full[k:])
	fullWantPath := filepath.Join(t.TempDir(), "fullwant.jsonl")
	writeAll(t, fullWantPath, full)
	fullWant, err := Scan(fullWantPath)
	if err != nil {
		t.Fatal(err)
	}
	got2, _, err := ScanIncremental(path, acc)
	if err != nil {
		t.Fatal(err)
	}
	snapEqual(t, got2, fullWant)
}

// A line larger than bufio's default 64KB token but within the 16MB cap must be
// parsed (guards against accidentally using a default-buffer scanner).
func TestScanIncremental_LongLineWithinCap(t *testing.T) {
	big := make([]byte, 200*1024)
	for i := range big {
		big[i] = 'x'
	}
	line := `{"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"` + string(big) + `"}}`
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeAll(t, path, line+"\n")

	want, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got, _, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatalf("ScanIncremental: %v", err)
	}
	snapEqual(t, got, want)
	if got.FirstPrompt == "" {
		t.Errorf("expected a first prompt from the long line")
	}
}

// Broken cache: truncation below the cached offset -> fresh re-parse.
func TestScanIncremental_TruncationResetsToFull(t *testing.T) {
	lines := incFixtures["basic"]
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeAll(t, path, joinLines(lines))
	_, acc, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate to just the first line.
	writeAll(t, path, joinLines(lines[:1]))
	wantPath := filepath.Join(t.TempDir(), "w.jsonl")
	writeAll(t, wantPath, joinLines(lines[:1]))
	want, err := Scan(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ScanIncremental(path, acc)
	if err != nil {
		t.Fatal(err)
	}
	snapEqual(t, got, want)
}

// Broken cache: in-place rewrite to the same byte size with different content
// (offset stale but size >= offset) -> must detect and re-parse fresh.
func TestScanIncremental_InPlaceRewriteSameSize(t *testing.T) {
	a := incFixtures["basic"]
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeAll(t, path, joinLines(a))
	_, acc, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Build a different content of the SAME byte length.
	orig := joinLines(a)
	b := incFixtures["subagent_open"]
	rew := joinLines(b)
	if len(rew) < len(orig) {
		rew += string(make([]byte, len(orig)-len(rew))) // pad with NULs (still != orig bytes)
	}
	rew = rew[:len(orig)]
	writeAll(t, path, rew)
	wantPath := filepath.Join(t.TempDir(), "w.jsonl")
	writeAll(t, wantPath, rew)
	want, err := Scan(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ScanIncremental(path, acc)
	if err != nil {
		t.Fatal(err)
	}
	snapEqual(t, got, want)
}

// Broken cache: file replaced at the same path with a new inode (rotation).
func TestScanIncremental_RotationNewInode(t *testing.T) {
	a := incFixtures["basic"]
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeAll(t, path, joinLines(a))
	_, acc, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	c := incFixtures["awaiting"]
	writeAll(t, path, joinLines(c))
	wantPath := filepath.Join(t.TempDir(), "w.jsonl")
	writeAll(t, wantPath, joinLines(c))
	want, err := Scan(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ScanIncremental(path, acc)
	if err != nil {
		t.Fatal(err)
	}
	snapEqual(t, got, want)
}

// Missing file: zero Snapshot, nil error, non-nil accumulator (matches Scan's
// missing-file contract), then folding works once the file appears.
func TestScanIncremental_MissingThenAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "later.jsonl")
	got, acc, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if acc == nil {
		t.Fatalf("expected non-nil accumulator for missing file")
	}
	if !reflect.DeepEqual(got, Snapshot{}) {
		t.Fatalf("missing file should yield zero Snapshot, got %+v", got)
	}
	lines := incFixtures["basic"]
	writeAll(t, path, joinLines(lines))
	wantPath := filepath.Join(t.TempDir(), "w.jsonl")
	writeAll(t, wantPath, joinLines(lines))
	want, err := Scan(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	got2, _, err := ScanIncremental(path, acc)
	if err != nil {
		t.Fatal(err)
	}
	snapEqual(t, got2, want)
}

// Empty file that later grows: zero Snapshot, then equals full Scan.
func TestScanIncremental_EmptyThenGrows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeAll(t, path, "")
	got, acc, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, Snapshot{}) {
		t.Fatalf("empty file should yield zero Snapshot, got %+v", got)
	}
	lines := incFixtures["ratelimit_legacy"]
	appendAll(t, path, joinLines(lines))
	wantPath := filepath.Join(t.TempDir(), "w.jsonl")
	writeAll(t, wantPath, joinLines(lines))
	want, err := Scan(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	got2, _, err := ScanIncremental(path, acc)
	if err != nil {
		t.Fatal(err)
	}
	snapEqual(t, got2, want)
}

// finalize must not alias the accumulator's live maps: a Snapshot returned by an
// earlier fold must be unchanged after a later fold mutates the same session.
func TestScanIncremental_NoSnapshotAliasing(t *testing.T) {
	lines := incFixtures["basic"]
	path := filepath.Join(t.TempDir(), "t.jsonl")
	// First two lines (opens Task tu_1 -> SubagentCount 1, modelTokens set).
	appendAll(t, path, joinLines(lines[:2]))
	snap1, acc, err := ScanIncremental(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap1Copy := snap1
	mt1 := map[string]any{}
	for k, v := range snap1.ModelTokens {
		mt1[k] = v
	}
	// Append remaining lines (closes tu_1, adds more tokens).
	appendAll(t, path, joinLines(lines[2:]))
	if _, _, err := ScanIncremental(path, acc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap1, snap1Copy) {
		t.Fatalf("earlier Snapshot mutated by later fold: %+v != %+v", snap1, snap1Copy)
	}
	for k, v := range snap1.ModelTokens {
		if !reflect.DeepEqual(mt1[k], v) {
			t.Fatalf("earlier Snapshot ModelTokens[%q] mutated", k)
		}
	}
}
