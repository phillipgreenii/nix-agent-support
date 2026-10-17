package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/unstick"
)

// realShape names the beads a shape check looks at inside one export.
type realShape struct {
	child, parent string // child has a "blocks" dependency on parent
	commented     string // a bead carrying exactly the comment shape below
	nullable      string // optional: a bead whose assignee/labels/defer_until may be explicit null
	// epic and epicChild, when set, name an open ready epic and its only child,
	// which is closed: the facts the finished-epic candidate rule reads.
	epic, epicChild string
}

// assertRealShape pins the facts about `bd export` rows and `bd ready --json`
// that internal/unstick relies on (tc-w08kk plan v2). It runs over a committed
// synthetic sample in the untagged unit test (drift without bd) and over REAL
// bd output in the contract test, so both are held to the same facts.
func assertRealShape(t *testing.T, rawExport, rawReady []byte, w realShape) {
	t.Helper()
	zform := func(what, s string) {
		t.Helper()
		if !strings.HasSuffix(s, "Z") {
			t.Errorf("%s %q is not Z-form", what, s)
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Errorf("%s %q is not RFC3339: %v", what, s, err)
		}
	}

	// Raw-level facts.
	statuses := map[string]bool{"open": true, "blocked": true, "in_progress": true, "deferred": true, "closed": true}
	var lines int
	for _, line := range bytes.Split(rawExport, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines++
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("export line is not JSON: %v: %s", err, line)
		}
		if string(m["_type"]) != `"issue"` {
			t.Errorf("row _type = %s, want \"issue\": %s", m["_type"], line)
		}
		if _, ok := m["is_template"]; ok {
			t.Errorf("export row carries is_template (only bd ready may): %s", line)
		}
		for _, k := range []string{"closer", "closed_by", "actor"} {
			if _, ok := m[k]; ok {
				t.Errorf("export row carries unexpected closer-like field %q (attribution is a heuristic because none exists): %s", k, line)
			}
		}
		var st string
		_ = json.Unmarshal(m["status"], &st)
		if !statuses[st] {
			t.Errorf("unexpected status %q", st)
		}
		for _, k := range []string{"created_at", "updated_at", "closed_at", "defer_until"} {
			if raw, ok := m[k]; ok && string(raw) != "null" {
				var s string
				if err := json.Unmarshal(raw, &s); err != nil {
					t.Errorf("%s is not a string: %s", k, raw)
					continue
				}
				zform(k, s)
			}
		}
	}
	if lines == 0 {
		t.Fatal("export is empty")
	}

	rows, err := unstick.ReadExport(bytes.NewReader(rawExport))
	if err != nil {
		t.Fatalf("ReadExport: %v", err)
	}
	byID := map[string]unstick.Row{}
	for _, r := range rows {
		byID[r.ID] = r
		for _, d := range r.Dependencies {
			if d.IssueID != r.ID {
				t.Errorf("%s: dependency issue_id = %s; a row lists only its own dependencies", r.ID, d.IssueID)
			}
			if d.Metadata != "" && !json.Valid([]byte(d.Metadata)) {
				t.Errorf("%s: dependency metadata is not a JSON string: %q", r.ID, d.Metadata)
			}
		}
	}

	// Dependency direction: issue_id depends on depends_on_id.
	c, ok := byID[w.child]
	if !ok {
		t.Fatalf("child %s missing from export", w.child)
	}
	var found bool
	for _, d := range c.Dependencies {
		if d.IssueID == w.child && d.DependsOnID == w.parent && d.Type == unstick.DepBlocks {
			found = true
		}
	}
	if !found {
		t.Errorf("%s: no blocks dependency {issue_id=%s, depends_on_id=%s}: %+v", w.child, w.child, w.parent, c.Dependencies)
	}
	if p := byID[w.parent]; len(p.Dependencies) != 0 {
		t.Errorf("parent %s must not list the reverse edge: %+v", w.parent, p.Dependencies)
	}

	// Comment shape.
	cr := byID[w.commented]
	if len(cr.Comments) == 0 {
		t.Fatalf("%s has no comments in export", w.commented)
	}
	cm := cr.Comments[0]
	if cm.ID == "" || cm.Author == "" || cm.Text == "" || cm.IssueID != w.commented {
		t.Errorf("comment shape drift: %+v", cm)
	}
	zform("comment created_at", cm.CreatedAt)

	// Null / omitted tolerance: absent and null decode to zero values.
	if w.nullable != "" {
		n := byID[w.nullable]
		if n.Assignee != "" || n.DeferUntil != "" || len(n.Labels) != 0 {
			t.Errorf("%s: null/omitted assignee/labels/defer_until must decode to zero values: %+v", w.nullable, n)
		}
	}

	// bd ready --json: {data, schema_version} envelope (bare array tolerated).
	trimmed := bytes.TrimSpace(rawReady)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var env struct {
			Data          json.RawMessage `json:"data"`
			SchemaVersion int             `json:"schema_version"`
		}
		if err := json.Unmarshal(trimmed, &env); err != nil {
			t.Fatalf("ready envelope: %v", err)
		}
		if env.Data == nil || env.SchemaVersion == 0 {
			t.Errorf("ready envelope drift (want data + schema_version): %s", trimmed)
		}
	}
	ready, err := unstick.ParseReady(rawReady)
	if err != nil {
		t.Fatalf("ParseReady: %v", err)
	}
	if len(ready) == 0 {
		t.Fatal("ready list is empty")
	}
	for _, rr := range ready {
		if rr.IsTemplate {
			t.Errorf("ready row %s unexpectedly is_template", rr.ID)
		}
	}

	if w.epic != "" {
		assertEpicShape(t, rows, byID, ready, w)
	}
}

// TestUnstickRealShapeSample runs the shape facts over a committed synthetic
// sample built in the real bd export/ready format, so drift in the decoders or
// in the facts is caught without bd installed. TestContract_UnstickSweep runs
// the same assertions over real bd output.
func TestUnstickRealShapeSample(t *testing.T) {
	exp, err := os.ReadFile("testdata/unstick-realshape/export.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rdy, err := os.ReadFile("testdata/unstick-realshape/ready.json")
	if err != nil {
		t.Fatal(err)
	}
	assertRealShape(t, exp, rdy, realShape{child: "rs-bbb", parent: "rs-aaa", commented: "rs-aaa", nullable: "rs-bbb", epic: "rs-eee", epicChild: "rs-fff"})

	// Labels on a ready row and the omitted/explicit-null equivalence.
	rows, _ := unstick.ReadExport(bytes.NewReader(exp))
	for _, r := range rows {
		if r.ID == "rs-ddd" && !r.HasLabel("human") {
			t.Errorf("rs-ddd lost its human label: %+v", r)
		}
	}
}

// assertEpicShape pins the facts the finished-epic candidate rule depends on:
// a parent-child edge is listed on the CHILD row (issue_id = child,
// depends_on_id = epic), a closed child carries closed_at and close_reason,
// and an open epic is a bd ready row with issue_type epic. It then runs the
// real triage over the rows and expects the epic to be the one candidate.
func assertEpicShape(t *testing.T, rows []unstick.Row, byID map[string]unstick.Row, ready []unstick.ReadyRow, w realShape) {
	t.Helper()
	epic, ok := byID[w.epic]
	if !ok {
		t.Fatalf("epic %s missing from export", w.epic)
	}
	kid, ok := byID[w.epicChild]
	if !ok {
		t.Fatalf("epic child %s missing from export", w.epicChild)
	}
	if epic.IssueType != "epic" || epic.Status != unstick.StatusOpen {
		t.Errorf("%s: issue_type/status = %q/%q, want epic/open", w.epic, epic.IssueType, epic.Status)
	}
	var edge bool
	for _, d := range kid.Dependencies {
		if d.IssueID == w.epicChild && d.DependsOnID == w.epic && d.Type == unstick.DepParentChild {
			edge = true
		}
	}
	if !edge {
		t.Errorf("%s: no parent-child dependency {issue_id=%s, depends_on_id=%s}: %+v", w.epicChild, w.epicChild, w.epic, kid.Dependencies)
	}
	for _, d := range epic.Dependencies {
		if d.Type == unstick.DepParentChild {
			t.Errorf("epic %s must not list its children's parent-child edges: %+v", w.epic, d)
		}
	}
	if kid.Status != unstick.StatusClosed || kid.ClosedAt == "" || kid.CloseReason == "" {
		t.Errorf("%s: closed child must carry closed_at and close_reason: %+v", w.epicChild, kid)
	}
	var rr *unstick.ReadyRow
	for i := range ready {
		if ready[i].ID == w.epic {
			rr = &ready[i]
		}
	}
	if rr == nil {
		t.Fatalf("open epic %s is not a bd ready row", w.epic)
	}
	if rr.IssueType != "epic" || rr.IsTemplate {
		t.Errorf("ready row for %s = %+v, want issue_type epic and no is_template", w.epic, *rr)
	}
	res := unstick.Triage(rows, ready, unstick.TriageOptions{}, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if err := res.CheckPartition(); err != nil {
		t.Error(err)
	}
	if len(res.EpicCandidates) != 1 || res.EpicCandidates[0] != w.epic {
		t.Errorf("epic candidates over real-shape rows = %v, want [%s]", res.EpicCandidates, w.epic)
	}
}
