package unstick

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadExport_fieldHandling(t *testing.T) {
	const in = `{"_type":"issue","id":"sy-1","title":"T1","status":"open","priority":2,"issue_type":"task","labels":null,"assignee":null,"defer_until":null,"extra_unknown":{"a":1}}
{"_type":"issue","id":"sy-mol-4prt","status":"blocked","labels":["human","pb"],"assignee":"w","defer_until":"2026-11-01T00:00:00Z","notes":"n","dependencies":[{"issue_id":"sy-mol-4prt","depends_on_id":"sy-1","type":"blocks","created_at":"2026-01-01T00:00:00Z","created_by":"a","metadata":"{\"k\":1}"}],"comments":[{"author":"a","created_at":"2026-01-02T00:00:00Z","id":7,"issue_id":"sy-mol-4prt","text":"hi"}]}

{"_type":"issue","id":"sy-o14i5.3.7","status":"closed","closed_at":"2026-02-01T00:00:00Z","close_reason":"done","comments":[{"id":"c9","text":"s"}]}
`
	rows, err := ReadExport(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("len = %d", len(rows))
	}
	r0 := rows[0]
	if r0.Labels != nil || r0.Assignee != "" || r0.DeferUntil != "" || r0.Description != "" {
		t.Errorf("null/omitted not zero: %+v", r0)
	}
	if r0.Priority != 2 || r0.IssueType != "task" {
		t.Errorf("r0 = %+v", r0)
	}
	r1 := rows[1]
	if !r1.HasLabel("human") || r1.HasLabel("nope") || r1.Assignee != "w" || r1.DeferUntil != "2026-11-01T00:00:00Z" {
		t.Errorf("r1 = %+v", r1)
	}
	wantDep := Dep{IssueID: "sy-mol-4prt", DependsOnID: "sy-1", Type: DepBlocks, CreatedAt: "2026-01-01T00:00:00Z", CreatedBy: "a", Metadata: `{"k":1}`}
	if len(r1.Dependencies) != 1 || r1.Dependencies[0] != wantDep {
		t.Errorf("dep = %+v, want %+v", r1.Dependencies, wantDep)
	}
	if r1.Comments[0].ID != "7" || r1.Comments[0].Text != "hi" || r1.Comments[0].IssueID != "sy-mol-4prt" {
		t.Errorf("comment = %+v", r1.Comments[0])
	}
	if rows[2].ID != "sy-o14i5.3.7" || rows[2].CloseReason != "done" || rows[2].Comments[0].ID != "c9" {
		t.Errorf("r2 = %+v", rows[2])
	}
}

func TestReadExport_table(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantIDs []string
		wantErr string
	}{
		{"empty", "", nil, ""},
		{"no trailing newline", `{"id":"a-1"}`, []string{"a-1"}, ""},
		{"non-issue skipped", `{"_type":"memory","id":"m-1"}` + "\n" + `{"_type":"issue","id":"a-1"}`, []string{"a-1"}, ""},
		{"bad json names line", "{\"id\":\"a-1\"}\n{oops\n", nil, "line 2"},
		{"missing id", `{"_type":"issue","title":"x"}`, nil, "line 1: row has no id"},
		{"crlf blank", "{\"id\":\"a-1\"}\r\n\r\n", []string{"a-1"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := ReadExport(strings.NewReader(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if !reflect.DeepEqual(ids, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

func TestReadExport_largeLine(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	rows, err := ReadExport(strings.NewReader(`{"id":"a-1","description":"` + big + `"}` + "\n"))
	if err != nil || len(rows) != 1 || len(rows[0].Description) != len(big) {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}

func TestReadExportFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.jsonl")
	if err := os.WriteFile(p, []byte("{\"id\":\"a-1\"}\n{bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExportFile(p); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("err = %v", err)
	}
	if _, err := ReadExportFile(p + ".missing"); err == nil {
		t.Error("want error for missing file")
	}
}

func TestParseReady(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []ReadyRow
		wantErr string
	}{
		{
			"envelope", `{"data":[{"id":"a-1","labels":["human"],"is_template":true,"issue_type":"epic"}],"schema_version":1}`,
			[]ReadyRow{{ID: "a-1", Labels: []string{"human"}, IsTemplate: true, IssueType: "epic"}},
			"",
		},
		{"is_template absent is false", `{"data":[{"id":"a-1"}]}`, []ReadyRow{{ID: "a-1"}}, ""},
		{"bare array", `[{"id":"a-1"},{"id":"a-2"}]`, []ReadyRow{{ID: "a-1"}, {ID: "a-2"}}, ""},
		{"empty data", `{"data":[]}`, []ReadyRow{}, ""},
		{"null data", `{"data":null}`, nil, "positive control"},
		{"missing data", `{"schema_version":1}`, nil, "positive control"},
		{"empty output", "  \n", nil, "empty output"},
		{"garbage", "nope", nil, "parse bd ready json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReady([]byte(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseTimeAndFormat(t *testing.T) {
	ts, err := ParseTime("2026-10-10T12:30:45Z")
	if err != nil || ts != time.Date(2026, 10, 10, 12, 30, 45, 0, time.UTC) {
		t.Fatalf("ts=%v err=%v", ts, err)
	}
	if ts, err = ParseTime("2026-10-10T14:30:45.5+02:00"); err != nil || ts.Location() != time.UTC || ts.Hour() != 12 {
		t.Errorf("offset/fraction: %v %v", ts, err)
	}
	for _, bad := range []string{"", "2026-10-10", "yesterday"} {
		if _, err := ParseTime(bad); err == nil {
			t.Errorf("ParseTime(%q) want error", bad)
		}
	}
	loc := time.FixedZone("x", 3600)
	if got := FormatTime(time.Date(2026, 1, 2, 4, 5, 6, 999, loc)); got != "2026-01-02T03:05:06Z" {
		t.Errorf("FormatTime = %q", got)
	}
}

func TestIndex_firstWins(t *testing.T) {
	m := Index([]Row{{ID: "a"}, {ID: "b"}, {ID: "a"}})
	if m["a"] != 0 || m["b"] != 1 || len(m) != 2 {
		t.Errorf("Index = %v", m)
	}
}
