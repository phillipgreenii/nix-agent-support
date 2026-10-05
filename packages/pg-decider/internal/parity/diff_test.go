package parity

import (
	"strings"
	"testing"
)

func e(kind, op string) Entry { return Entry{Entity: "acme/api#1", Kind: kind, Op: op} }

func exp(id, kind, op string, side Side) Expected {
	return Expected{ID: id, Entity: "acme/api#1", Kind: kind, Op: op, Side: side, Reason: "because"}
}

func TestDiffMatchingSidesAreClean(t *testing.T) {
	r := Diff("s", []Entry{e("anchor", OpCreate)}, []Entry{e("anchor", OpCreate)}, nil)
	if !r.Clean() || len(r.Matched) != 1 || len(r.Unexplained) != 0 || len(r.Missing) != 0 {
		t.Fatalf("report = %+v", r)
	}
}

func TestDiffUnlistedDifferenceFails(t *testing.T) {
	r := Diff("s", []Entry{e("anchor", OpCreate), e("process-feedback", OpCreate)}, []Entry{e("anchor", OpCreate), e("fix-ci", OpCreate)}, nil)
	if r.Clean() {
		t.Fatal("report is clean, want unexplained differences")
	}
	want := []Difference{
		{Entry: e("fix-ci", OpCreate), Side: SideNew},
		{Entry: e("process-feedback", OpCreate), Side: SideOld},
	}
	if len(r.Unexplained) != 2 || r.Unexplained[0] != want[0] || r.Unexplained[1] != want[1] {
		t.Fatalf("unexplained = %v, want %v", r.Unexplained, want)
	}
}

func TestDiffListedDifferenceIsExpectedAndClean(t *testing.T) {
	r := Diff("s", []Entry{e("process-feedback", OpCreate)}, nil, []Expected{exp("S16", "process-feedback", OpCreate, SideOld)})
	if !r.Clean() || len(r.Expected) != 1 || r.Expected[0].ID != "S16" {
		t.Fatalf("report = %+v", r)
	}
}

func TestDiffListedExceptionThatDidNotOccurFails(t *testing.T) {
	r := Diff("s", []Entry{e("anchor", OpCreate)}, []Entry{e("anchor", OpCreate)}, []Expected{exp("S13", "fix-ci", OpCreate, SideNew)})
	if r.Clean() || len(r.Missing) != 1 || r.Missing[0].ID != "S13" {
		t.Fatalf("report = %+v; want S13 reported as missing", r)
	}
}

func TestDiffWrongSideIsBothUnexplainedAndMissing(t *testing.T) {
	r := Diff("s", nil, []Entry{e("fix-ci", OpCreate)}, []Expected{exp("S13", "fix-ci", OpCreate, SideOld)})
	if len(r.Unexplained) != 1 || len(r.Missing) != 1 {
		t.Fatalf("report = %+v", r)
	}
}

func TestDiffCountsOccurrences(t *testing.T) {
	// Two new-only updates, one listed: the second is unexplained. A listed
	// pair against a single occurrence leaves one missing.
	r := Diff("s", nil, []Entry{e("anchor", OpUpdate), e("anchor", OpUpdate)}, []Expected{exp("UNOBS", "anchor", OpUpdate, SideNew)})
	if len(r.Expected) != 1 || len(r.Unexplained) != 1 {
		t.Fatalf("report = %+v", r)
	}
	r = Diff("s", nil, []Entry{e("anchor", OpUpdate)}, []Expected{
		exp("UNOBS", "anchor", OpUpdate, SideNew), exp("UNOBS", "anchor", OpUpdate, SideNew),
	})
	if len(r.Expected) != 1 || len(r.Missing) != 1 {
		t.Fatalf("report = %+v", r)
	}
}

func TestDiffOnlyMatchesTheSameEntity(t *testing.T) {
	other := Entry{Entity: "acme/api#2", Kind: "anchor", Op: OpCreate}
	r := Diff("s", []Entry{e("anchor", OpCreate)}, []Entry{other}, nil)
	if len(r.Unexplained) != 2 {
		t.Fatalf("unexplained = %v, want both sides", r.Unexplained)
	}
}

func TestParseExpectedValidates(t *testing.T) {
	good := `{"scenarios":{"s":[{"id":"S13","entity":"acme/api#1","kind":"fix-ci","op":"create","side":"new-only","reason":"r"}]}}`
	m, err := ParseExpected([]byte(good))
	if err != nil || len(m["s"]) != 1 {
		t.Fatalf("good: %v %v", m, err)
	}
	for name, bad := range map[string]string{
		"unknown id":     strings.Replace(good, `"S13"`, `"S99"`, 1),
		"empty reason":   strings.Replace(good, `"r"`, `""`, 1),
		"unknown side":   strings.Replace(good, `new-only`, `both`, 1),
		"unknown member": strings.Replace(good, `"id"`, `"idd"`, 1),
		"empty entity":   strings.Replace(good, `acme/api#1`, ``, 1),
		"not json":       `{`,
	} {
		if _, err := ParseExpected([]byte(bad)); err == nil {
			t.Errorf("%s: want an error, got none", name)
		}
	}
}

func TestRenderSummarisesEveryScenario(t *testing.T) {
	var b strings.Builder
	outs := []Outcome{
		{Scenario: Scenario{Name: "01-a"}, Report: Diff("01-a", []Entry{e("anchor", OpCreate)}, []Entry{e("anchor", OpCreate)}, nil)},
		{Scenario: Scenario{Name: "02-b"}, Report: Diff("02-b", nil, []Entry{e("fix-ci", OpCreate)}, nil)},
		{Scenario: Scenario{Name: "03-c"}, Err: ErrUnsupported},
	}
	if Render(&b, outs) {
		t.Fatal("Render reported clean for a run with failures")
	}
	got := b.String()
	for _, want := range []string{"01-a", "02-b", "03-c", "UNEXPLAINED", "UNSUPPORTED", "fix-ci"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestParseExpectedAcceptsTheScenario13Ruling(t *testing.T) {
	doc := `{"scenarios":{"s":[{"id":"pg2-nbkps","entity":"acme/api#1","kind":"anchor","op":"adopt","side":"old-only","reason":"ruling"}]}}`
	if m, err := ParseExpected([]byte(doc)); err != nil || len(m["s"]) != 1 {
		t.Fatalf("ruling id: %v %v", m, err)
	}
}
