package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
)

func TestLoadQueryConfig_absentPathIsUnconfigured(t *testing.T) {
	qf, ok, err := loadQueryConfig("")
	if err != nil || ok {
		t.Fatalf("loadQueryConfig(\"\") = %+v, %v, %v; want zero value, ok=false, err=nil", qf, ok, err)
	}
}

func TestLoadQueryConfig_missingEmitTypeIsError(t *testing.T) {
	p := writeRoleConfigFile(t, `{"labels":["worker-ready"]}`)
	if _, _, err := loadQueryConfig(p); err == nil {
		t.Fatal("a query config with no emitType must error")
	}
}

func TestLoadQueryConfig_decodes(t *testing.T) {
	p := writeRoleConfigFile(t, `{"emitType":"work.ready","labels":["worker-ready"],"excludeLabels":["human"],"titlePrefix":"process-feedback:","itemType":"task"}`)
	qf, ok, err := loadQueryConfig(p)
	if err != nil {
		t.Fatalf("loadQueryConfig error: %v", err)
	}
	if !ok {
		t.Fatal("a present, valid query config must report ok=true")
	}
	if qf.EmitType != "work.ready" || len(qf.Labels) != 1 || qf.Labels[0] != "worker-ready" ||
		len(qf.ExcludeLabels) != 1 || qf.ExcludeLabels[0] != "human" ||
		qf.TitlePrefix != "process-feedback:" || qf.ItemType != "task" {
		t.Fatalf("decoded wrong: %+v", qf)
	}
}

func TestLabelArgs(t *testing.T) {
	got := labelArgs([]string{"a", "b"}, []string{"c"})
	want := []string{"--label", "a", "--label", "b", "--exclude-label", "c"}
	if len(got) != len(want) {
		t.Fatalf("labelArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labelArgs = %v, want %v", got, want)
		}
	}
}

func TestPostFilterIssues(t *testing.T) {
	in := []beads.Issue{
		{ID: "1", Title: "process-feedback: a", Type: "task"},
		{ID: "2", Title: "process-feedback: b", Type: "bug"},
		{ID: "3", Title: "other", Type: "task"},
	}
	out := postFilterIssues(in, "process-feedback:", "task")
	if len(out) != 1 || out[0].ID != "1" {
		t.Fatalf("postFilterIssues = %+v, want just issue 1", out)
	}
}

func TestPostFilterIssues_noFiltersPassesThrough(t *testing.T) {
	in := []beads.Issue{{ID: "1"}, {ID: "2"}}
	out := postFilterIssues(in, "", "")
	if len(out) != 2 {
		t.Fatalf("postFilterIssues with no filters = %+v, want both issues unchanged", out)
	}
}

func TestFingerprintID(t *testing.T) {
	if got := fingerprintID("work.ready", "zr-1"); got != "work.ready:zr-1" {
		t.Fatalf("fingerprintID = %q, want %q", got, "work.ready:zr-1")
	}
}

func TestQueryBeadsReady_mapsIssuesToWireEvents(t *testing.T) {
	br := fakeBR{out: map[string]string{
		"ready --label worker-ready --exclude-label human --json --limit 0": `{"data":[` +
			`{"id":"zr-1","issue_type":"task","title":"process-feedback: x","metadata":{"repo":"o/r"}},` +
			`{"id":"zr-2","issue_type":"task","title":"other"}` +
			`]}`,
	}}
	qf := queryFile{EmitType: "work.ready", Labels: []string{"worker-ready"}, ExcludeLabels: []string{"human"}, TitlePrefix: "process-feedback:"}
	events, err := queryBeadsReady(context.Background(), br, qf)
	if err != nil {
		t.Fatalf("queryBeadsReady error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly the title_prefix-matching issue", events)
	}
	evt := events[0]
	if evt["id"] != "work.ready:zr-1" || evt["type"] != "work.ready" {
		t.Fatalf("event wrong: %+v", evt)
	}
	payload, ok := evt["payload"].(map[string]any)
	if !ok || payload["id"] != "zr-1" || payload["title"] != "process-feedback: x" {
		t.Fatalf("event payload wrong: %+v", evt)
	}
}

func TestQueryBeadsReady_bdErrorPropagates(t *testing.T) {
	br := fakeBR{err: errors.New("bd down")}
	if _, err := queryBeadsReady(context.Background(), br, queryFile{EmitType: "work.ready"}); err == nil {
		t.Fatal("a bd failure must propagate, not become zero events")
	}
}

// writeRoleConfigFile writes contents to a fresh temp file and returns its
// path — a small local helper distinct from the conformance package's own
// writeRoleConfig (a different package), used only by this file's tests.
func writeRoleConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := t.TempDir() + "/query.json"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write query config: %v", err)
	}
	return path
}
