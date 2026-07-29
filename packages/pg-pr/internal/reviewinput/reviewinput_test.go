package reviewinput

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecode_AgentDocumentedShape is the golden test for the shape the pg-pr
// reviewer agent assets emit (pg2-cns7a AC1). Before the fix this payload
// decoded to {Path: "src/main.go", Line: 0, Body: ""} — every finding blank —
// because `message`, `lines` and `severity` had no target field and
// encoding/json drops unknown fields silently.
//
// Kept as a verbatim literal of the documented block in
// claude-marketplace/pg-pr/agents/pg-pr-review-code-changes.md so a drift in
// either direction fails here.
func TestDecode_AgentDocumentedShape(t *testing.T) {
	const payload = `{
  "comments": [
    {
      "path": "src/main.go",
      "lines": [42],
      "message": "Unchecked error return; err is discarded.",
      "severity": "error"
    }
  ]
}`
	d, err := Decode([]byte(payload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(d.Comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(d.Comments))
	}
	got := d.Comments[0]
	if got.Path != "src/main.go" {
		t.Errorf("path = %q, want src/main.go", got.Path)
	}
	if got.Line != 42 {
		t.Errorf(`line = %d, want 42 (from "lines":[42])`, got.Line)
	}
	if got.Body == "" {
		t.Fatal("body is empty — the pg2-cns7a defect: the finding text was dropped")
	}
	if !strings.Contains(got.Body, "Unchecked error return") {
		t.Errorf("body = %q, want it to carry the message text", got.Body)
	}
	if !strings.HasPrefix(got.Body, "**error**: ") {
		t.Errorf("body = %q, want the severity folded in as a bold prefix", got.Body)
	}
}

// TestDecode_AuthoritativeShape covers the authoritative (non-alias) keys the
// updated assets emit: path/line/body/severity.
func TestDecode_AuthoritativeShape(t *testing.T) {
	const payload = `{
  "body": "summary",
  "comments": [
    {"path": "a.go", "line": 7, "body": "leaks a file handle", "severity": "warning"}
  ]
}`
	d, err := Decode([]byte(payload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.Body != "summary" {
		t.Errorf("draft body = %q, want summary", d.Body)
	}
	if len(d.Comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(d.Comments))
	}
	if got := d.Comments[0]; got.Path != "a.go" || got.Line != 7 ||
		got.Body != "**warning**: leaks a file handle" {
		t.Errorf("comment = %+v, want a.go:7 with the warning-prefixed body", got)
	}
}

// TestDecode_PRLevelNullPathAndLines: the pr-structure and jira-alignment agents
// emit `"path": null, "lines": null` for PR-level findings. Those MUST decode to
// an un-anchored comment (path "", line 0) with the text intact — the provider
// folds such comments into the review body.
func TestDecode_PRLevelNullPathAndLines(t *testing.T) {
	const payload = `{"comments":[{"path":null,"lines":null,"message":"commits 2 and 3 fix commit 1","severity":"suggestion"}]}`
	d, err := Decode([]byte(payload))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got := d.Comments[0]
	if got.Path != "" || got.Line != 0 {
		t.Errorf("path/line = %q/%d, want empty/0 for a PR-level comment", got.Path, got.Line)
	}
	if !strings.Contains(got.Body, "commits 2 and 3") {
		t.Errorf("body = %q, want the finding text", got.Body)
	}
}

// TestDecode_ContiguousLinesSpanBecomesStartLine is pg2-3c8mo AC2: the reviewer
// assets emit findings as `lines: [...]`, an ARRAY, and a contiguous multi-entry
// run is a genuine multi-line finding. It MUST map min -> start_line and
// max -> line, not be rejected (which is what pg2-cns7a's decoder did rather than
// truncate to lines[0] and misplace the comment).
func TestDecode_ContiguousLinesSpanBecomesStartLine(t *testing.T) {
	tests := map[string]struct {
		payload             string
		wantStart, wantLine int
	}{
		"ascending run":    {`{"comments":[{"path":"a.go","lines":[10,11,12],"body":"x"}]}`, 10, 12},
		"unordered run":    {`{"comments":[{"path":"a.go","lines":[12,10,11],"body":"x"}]}`, 10, 12},
		"two-line run":     {`{"comments":[{"path":"a.go","lines":[7,8],"body":"x"}]}`, 7, 8},
		"explicit keys":    {`{"comments":[{"path":"a.go","start_line":10,"line":12,"body":"x"}]}`, 10, 12},
		"keys agree alias": {`{"comments":[{"path":"a.go","start_line":10,"line":12,"lines":[10,11,12],"body":"x"}]}`, 10, 12},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, err := Decode([]byte(tc.payload))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			got := d.Comments[0]
			if got.StartLine != tc.wantStart || got.Line != tc.wantLine {
				t.Errorf("start_line/line = %d/%d, want %d/%d",
					got.StartLine, got.Line, tc.wantStart, tc.wantLine)
			}
		})
	}
}

// TestDecode_SingleLineKeepsStartLineUnset pins pg2-3c8mo's compatibility floor:
// every single-line form must leave StartLine at 0 so its api.Comment marshals
// WITHOUT a start_line key (omitempty) and the posted payload is byte-identical
// to the pre-multi-line one. start_line == line is a one-line span and normalizes
// the same way rather than posting an invalid start_line == line range.
func TestDecode_SingleLineKeepsStartLineUnset(t *testing.T) {
	tests := map[string]struct {
		payload  string
		wantLine int
	}{
		"scalar line":            {`{"comments":[{"path":"a.go","line":42,"body":"x"}]}`, 42},
		"single-entry lines":     {`{"comments":[{"path":"a.go","lines":[42],"body":"x"}]}`, 42},
		"start_line equals line": {`{"comments":[{"path":"a.go","start_line":42,"line":42,"body":"x"}]}`, 42},
		"null start_line":        {`{"comments":[{"path":"a.go","line":42,"start_line":null,"body":"x"}]}`, 42},
		"un-anchored":            {`{"comments":[{"path":null,"lines":null,"body":"x"}]}`, 0},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, err := Decode([]byte(tc.payload))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			got := d.Comments[0]
			if got.StartLine != 0 || got.Line != tc.wantLine {
				t.Fatalf("start_line/line = %d/%d, want 0/%d", got.StartLine, got.Line, tc.wantLine)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(raw), "start_line") {
				t.Errorf("a single-line comment must not carry start_line on the wire: %s", raw)
			}
		})
	}
}

// TestDecode_MultiLineSpanRoundTripsThroughAStagedFile: a multi-line finding is
// staged as a file (reviewstage.Save marshals api.Comment, which now writes
// start_line) and `--from-file` re-feeds that file through this strict decoder.
// start_line must therefore be an ACCEPTED input key, or the documented
// inspect-and-edit path would reject exactly the drafts this bead makes possible.
func TestDecode_MultiLineSpanRoundTripsThroughAStagedFile(t *testing.T) {
	staged, err := Decode([]byte(`{"comments":[{"path":"a.go","lines":[10,11,12],"body":"finding"}]}`))
	if err != nil {
		t.Fatalf("first Decode: %v", err)
	}
	staged.Repo, staged.PR = "foo/bar", 42
	onDisk, err := json.MarshalIndent(staged, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(onDisk), `"start_line": 10`) {
		t.Fatalf("staged draft lost the span:\n%s", onDisk)
	}
	again, err := Decode(onDisk)
	if err != nil {
		t.Fatalf("re-feeding a staged multi-line draft must decode, got: %v\npayload:\n%s", err, onDisk)
	}
	if got := again.Comments[0]; got.StartLine != 10 || got.Line != 12 || got.Body != "finding" {
		t.Errorf("round-trip lost the span: %+v", got)
	}
}

// TestDecode_ExampleInHelpIsValid keeps `pg-pr review --help` honest: the
// documented example MUST decode through the very schema it documents
// (pg2-cns7a AC4). Executable documentation.
func TestDecode_ExampleInHelpIsValid(t *testing.T) {
	if !strings.Contains(SchemaDoc, strings.TrimSpace(strings.SplitN(ExampleJSON, "\n", 2)[0])) {
		t.Fatal("SchemaDoc does not embed ExampleJSON")
	}
	d, err := Decode([]byte(ExampleJSON))
	if err != nil {
		t.Fatalf("the example in `review --help` does not decode: %v", err)
	}
	if len(d.Comments) != 3 {
		t.Fatalf("example comments = %d, want 3", len(d.Comments))
	}
	if d.HeadSHA == "" {
		t.Error("example head_sha did not map")
	}
	// The example documents the multi-line form; it must actually decode as one.
	if got := d.Comments[1]; got.StartLine != 88 || got.Line != 90 {
		t.Errorf("example's multi-line comment = start_line %d/line %d, want 88/90", got.StartLine, got.Line)
	}
	for i, c := range d.Comments {
		if c.Body == "" {
			t.Errorf("example comments[%d] has an empty body", i)
		}
	}
	// The example's warnings must reach the body, appended to the prose summary.
	if !strings.Contains(d.Body, "JIRA access unavailable") {
		t.Errorf("draft body = %q, want the example's warning folded in", d.Body)
	}
	if !strings.HasPrefix(d.Body, "Reviewed 3 files") {
		t.Errorf("draft body = %q, want the supplied summary kept first", d.Body)
	}
}

// TestDecode_RejectsUnmappableKeys is pg2-cns7a AC3 at the package boundary:
// every key the decoder cannot map is an error naming that key, never a silent
// drop. Both a top-level and a per-comment key are covered.
func TestDecode_RejectsUnmappableKeys(t *testing.T) {
	tests := map[string]struct{ payload, wantKey string }{
		"comment key": {`{"comments":[{"path":"a.go","line":1,"body":"x","confidence":0.4}]}`, "confidence"},
		"top-level key": {
			`{"comments":[],"tickets_found":["DE-1"]}`, "tickets_found",
		},
		"jira agent error key": {`{"comments":[],"error":"no JIRA"}`, "error"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(tc.payload))
			if err == nil {
				t.Fatalf("Decode(%s) = nil error; unmappable keys must be rejected", tc.payload)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error %q does not name the offending key %q", err, tc.wantKey)
			}
		})
	}
}

// TestDecode_RejectsEmptyCommentBody: a comment with no text would post as a
// blank review comment — exactly the observable symptom of pg2-cns7a — so it is
// rejected rather than posted.
func TestDecode_RejectsEmptyCommentBody(t *testing.T) {
	for name, payload := range map[string]string{
		"no body key": `{"comments":[{"path":"a.go","line":1}]}`,
		"empty body":  `{"comments":[{"path":"a.go","line":1,"body":"   "}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(payload))
			if err == nil {
				t.Fatal("expected an error for a comment with no text")
			}
			if !strings.Contains(err.Error(), "comments[0]") {
				t.Errorf("error %q should locate the offending comment", err)
			}
		})
	}
}

func TestDecode_RejectsAmbiguousAndInvalidFields(t *testing.T) {
	tests := map[string]struct{ payload, wantIn string }{
		"body and message differ": {
			`{"comments":[{"path":"a.go","line":1,"body":"x","message":"y"}]}`, "deprecated alias",
		},
		"line and lines disagree": {
			`{"comments":[{"path":"a.go","line":1,"lines":[2],"body":"x"}]}`, "disagree",
		},
		// A gapped range has no GitHub representation, so it stays a loud error
		// even though a CONTIGUOUS run is now accepted (pg2-3c8mo).
		"non-contiguous lines array": {
			`{"comments":[{"path":"a.go","lines":[10,12],"body":"x"}]}`, "not a contiguous span",
		},
		"non-contiguous lines array with a run": {
			`{"comments":[{"path":"a.go","lines":[10,11,20,21],"body":"x"}]}`, "not a contiguous span",
		},
		"duplicate entries in lines array": {
			`{"comments":[{"path":"a.go","lines":[10,10,11],"body":"x"}]}`, "not a contiguous span",
		},
		"start_line without line": {
			`{"comments":[{"path":"a.go","start_line":10,"body":"x"}]}`, "needs BOTH",
		},
		"start_line after line": {
			`{"comments":[{"path":"a.go","start_line":12,"line":10,"body":"x"}]}`, "is after",
		},
		"negative start_line": {
			`{"comments":[{"path":"a.go","start_line":-1,"line":10,"body":"x"}]}`, `"start_line" must be >= 1`,
		},
		"start_line and lines disagree": {
			`{"comments":[{"path":"a.go","start_line":9,"lines":[10,11,12],"body":"x"}]}`, "disagree",
		},
		"negative line": {
			`{"comments":[{"path":"a.go","line":-3,"body":"x"}]}`, "must be >= 1",
		},
		"unknown severity": {
			`{"comments":[{"path":"a.go","line":1,"body":"x","severity":"blocker"}]}`, "unknown \"severity\"",
		},
		// The pre-fix assets documented severity as the literal enumeration
		// "error|warning|suggestion"; that is a placeholder, not a value.
		"enumeration placeholder as severity": {
			`{"comments":[{"path":"a.go","line":1,"body":"x","severity":"error|warning|suggestion"}]}`,
			"unknown \"severity\"",
		},
		"trailing second object": {
			`{"comments":[{"path":"a.go","line":1,"body":"x"}]} {"comments":[]}`, "trailing JSON",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(tc.payload))
			if err == nil {
				t.Fatalf("Decode(%s) = nil error", tc.payload)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err, tc.wantIn)
			}
		})
	}
}

// TestDecode_AcceptsIdenticalBodyAndMessage: an agent that emits both keys with
// the same text is unambiguous, so it is accepted rather than failed.
func TestDecode_AcceptsIdenticalBodyAndMessage(t *testing.T) {
	d, err := Decode([]byte(`{"comments":[{"path":"a.go","line":1,"body":"same","message":"same"}]}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.Comments[0].Body != "same" {
		t.Errorf("body = %q, want same", d.Comments[0].Body)
	}
}

// TestDecode_WarningsBecomeBodyWhenNoSummary: warnings had no target field at
// all before the fix (the orchestrator asset emits them for a reviewer subagent
// that failed), so they vanished. With no prose summary they become the body.
func TestDecode_WarningsBecomeBodyWhenNoSummary(t *testing.T) {
	d, err := Decode([]byte(`{"comments":[],"warnings":["jira-alignment: JIRA access unavailable"," ","code-changes: timed out"]}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !strings.Contains(d.Body, "JIRA access unavailable") || !strings.Contains(d.Body, "timed out") {
		t.Errorf("body = %q, want both warnings", d.Body)
	}
	if strings.Contains(d.Body, "\n- \n") {
		t.Errorf("body = %q, blank warnings should be skipped", d.Body)
	}
}

// TestDecode_NoFindingsKeepsEmptyBody: `{"comments": []}` (what every reviewer
// asset emits when it finds nothing) must NOT gain a synthetic summary. The
// provider treats a body-less, comment-less review as a no-op, and
// reviewsink.IngestSelfReview turns a non-empty Body into a PR-level feedback
// row that then needs dispositioning — a "0 findings" body would manufacture
// blocking feedback out of nothing.
func TestDecode_NoFindingsKeepsEmptyBody(t *testing.T) {
	d, err := Decode([]byte(`{"comments": []}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.Body != "" {
		t.Errorf("body = %q, want empty for a findings-free review", d.Body)
	}
	if len(d.Comments) != 0 {
		t.Errorf("comments = %d, want 0", len(d.Comments))
	}
}

// TestDecode_AcceptsAStagedDraftFile: `reviewstage.Save` marshals
// reviewstage.Draft (whose comments are api.Comment, with id/author/author_role/
// resolved always written), and `--from-file` is documented as a human
// inspect-and-edit path. Re-feeding a staged file MUST therefore decode, not trip
// the strict unknown-key check.
func TestDecode_AcceptsAStagedDraftFile(t *testing.T) {
	staged, err := Decode([]byte(`{"body":"summary","comments":[{"path":"a.go","line":9,"body":"finding"}]}`))
	if err != nil {
		t.Fatalf("first Decode: %v", err)
	}
	staged.Repo, staged.PR = "foo/bar", 42
	onDisk, err := json.MarshalIndent(staged, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	again, err := Decode(onDisk)
	if err != nil {
		t.Fatalf("re-feeding a staged draft file must decode, got: %v\npayload:\n%s", err, onDisk)
	}
	if len(again.Comments) != 1 || again.Comments[0].Line != 9 || again.Comments[0].Body != "finding" {
		t.Errorf("round-trip lost content: %+v", again.Comments)
	}
	// repo/pr are accepted but NOT trusted: the verb sets them from its args.
	if again.Repo != "" || again.PR != 0 {
		t.Errorf("repo/pr = %q/%d, want them ignored on input", again.Repo, again.PR)
	}
}

// TestDecode_MalformedJSON reports a parse error rather than an empty draft.
func TestDecode_MalformedJSON(t *testing.T) {
	if _, err := Decode([]byte(`{"comments": [`)); err == nil {
		t.Fatal("expected a parse error for truncated JSON")
	}
}

// TestDecode_PassesThroughProvenance: the advisory provenance keys the daemon
// and the pr-pool review role set must survive the adapter.
func TestDecode_PassesThroughProvenance(t *testing.T) {
	d, err := Decode([]byte(`{"head_sha":"abc123","ownership":"team","bead_id":"pg2-x","verdict":"comment"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.HeadSHA != "abc123" || d.Ownership != "team" || d.BeadID != "pg2-x" || d.Verdict != "comment" {
		t.Errorf("provenance lost: %+v", d)
	}
}
