package parser_test

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

var updateDigest = flag.Bool("update-digest", false, "rewrite testdata/equivalence.golden")

// badLines are lines the transcript source rejects (a type mismatch, invalid
// JSON, a content that is neither a string nor an array); they land in bad_lines.
const badLines = `{"type":"user","uuid":"b1","sessionId":"s1","isSidechain":"yes"}
{"type":"user","uuid":"b2","sessionId":"s1","extra":
{"type":"user","uuid":"b3","sessionId":"s1","message":{"role":"user","content":5}}
{"type":"user","uuid":"b4","sessionId":"s1","message":{"role":"user","content":[1,"x"]}}
{"type":"user","uuid":"b5","sessionId":"s1","timestamp":"2026-11-03T10:00:12Z","message":{"role":"user","content":[{"type":"text","text":"ok after bad"}]}}
`

// equivalenceFixture is the transcript source's parse fixture plus the bad lines.
func equivalenceFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../source/claudetranscript/testdata/lines.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(string(raw), "\n") + "\n" + badLines
}

// TestOutputEquivalenceDigest pins the content of every table the parse fills
// (records, fields, texts, ext_assistant_usage, unknown_keys,
// unknown_keys_total, bad_lines) for a bulk rebuild over the fixture. The
// golden was produced before the single-pass decode change, so a change to how
// a line is decoded or how unknown keys are found cannot alter the output.
func TestOutputEquivalenceDigest(t *testing.T) {
	k := testkit.NewWithStoreOptions(t, store.Options{V3Layout: true})
	k.StoreOptions = store.Options{V3Layout: true, Rebuild: true, BulkRebuild: true, BulkRecords: 100}
	k.Add("example-proj", "example-session-1.jsonl", equivalenceFixture(t))
	k.Reopen()
	k.Parse()
	if err := k.Store.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(tableDigests(t, k.Store.Read()), "\n") + "\n"
	if *updateDigest {
		if err := os.WriteFile("testdata/equivalence.golden", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("testdata/equivalence.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(got), want) {
		t.Fatalf("table digests differ from testdata/equivalence.golden\n--- got\n%s--- want\n%s", got, want)
	}
}
