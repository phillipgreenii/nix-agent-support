package main

import (
	"reflect"
	"testing"
)

func TestParseMetaArgs_setWithValue(t *testing.T) {
	verb, ext, key, val, err := parseMetaArgs([]string{"set", "zr-abc", "role", "worker"})
	if err != nil {
		t.Fatalf("parseMetaArgs: %v", err)
	}
	if verb != "set" || ext != "zr-abc" || key != "role" || val != "worker" {
		t.Errorf("got (%q,%q,%q,%q)", verb, ext, key, val)
	}
}

func TestParseMetaArgs_setBareTagDefaultsEmptyValue(t *testing.T) {
	verb, ext, key, val, err := parseMetaArgs([]string{"set", "zr-abc", "pinned"})
	if err != nil {
		t.Fatalf("parseMetaArgs: %v", err)
	}
	if verb != "set" || ext != "zr-abc" || key != "pinned" || val != "" {
		t.Errorf("bare tag got (%q,%q,%q,%q), want set/zr-abc/pinned/\"\"", verb, ext, key, val)
	}
}

func TestParseMetaArgs_getNeedsKey(t *testing.T) {
	if _, _, _, _, err := parseMetaArgs([]string{"get", "zr-abc"}); err == nil {
		t.Fatal("get without key must error")
	}
}

func TestParseMetaArgs_listNeedsOnlyExternalID(t *testing.T) {
	verb, ext, _, _, err := parseMetaArgs([]string{"list", "zr-abc"})
	if err != nil || verb != "list" || ext != "zr-abc" {
		t.Fatalf("list parse got verb=%q ext=%q err=%v", verb, ext, err)
	}
}

func TestParseMetaArgs_unknownVerb(t *testing.T) {
	if _, _, _, _, err := parseMetaArgs([]string{"frobnicate", "zr-abc"}); err == nil {
		t.Fatal("unknown verb must error")
	}
}

func TestParseMetaArgs_noArgs(t *testing.T) {
	if _, _, _, _, err := parseMetaArgs(nil); err == nil {
		t.Fatal("no args must error")
	}
}

func TestRenderMetaList_sortedKeyValueLines(t *testing.T) {
	got := renderMetaList(map[string]string{"role": "worker", "bead": "zr-1"})
	want := "bead=zr-1\nrole=worker\n"
	if got != want {
		t.Errorf("renderMetaList = %q, want %q", got, want)
	}
}

func TestRenderMetaListJSON_object(t *testing.T) {
	got, err := renderMetaListJSON(map[string]string{"role": "worker"})
	if err != nil {
		t.Fatalf("renderMetaListJSON: %v", err)
	}
	if !reflect.DeepEqual(got, `{"role":"worker"}`) {
		t.Errorf("renderMetaListJSON = %s", got)
	}
}

func TestMetaFlagsParse_pullsJSONAndLeavesPositionals(t *testing.T) {
	jsonOut, labels, pos, err := parseMetaFlags([]string{"list", "zr-abc", "--json"})
	if err != nil {
		t.Fatalf("parseMetaFlags: %v", err)
	}
	if !jsonOut {
		t.Error("jsonOut = false, want true")
	}
	if len(labels) != 0 {
		t.Errorf("labels = %v, want none", labels)
	}
	want := []string{"list", "zr-abc"}
	if !reflect.DeepEqual(pos, want) {
		t.Errorf("pos = %v, want %v", pos, want)
	}
}

// TestMetaFlagsParse_pullsRepeatedLabelsInterspersed pins meta.go's own
// --label flag (repeatable, only meaningful for `set`), and that it may be
// interspersed with the positional set/external_id/key args exactly like
// --json already is (pg2-24f89/pg2-qye99 D8.1).
func TestMetaFlagsParse_pullsRepeatedLabelsInterspersed(t *testing.T) {
	jsonOut, labels, pos, err := parseMetaFlags(
		[]string{"set", "zr-abc", "--label", "pgrouter.role", "role", "worker", "--label", "pgrouter.pool"},
	)
	if err != nil {
		t.Fatalf("parseMetaFlags: %v", err)
	}
	if jsonOut {
		t.Error("jsonOut = true, want false")
	}
	wantLabels := labelFlag{"pgrouter.role": true, "pgrouter.pool": true}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Errorf("labels = %v, want %v", labels, wantLabels)
	}
	wantPos := []string{"set", "zr-abc", "role", "worker"}
	if !reflect.DeepEqual(pos, wantPos) {
		t.Errorf("pos = %v, want %v", pos, wantPos)
	}
}

func TestMetaFlagsParse_labelRequiresAValue(t *testing.T) {
	if _, _, _, err := parseMetaFlags([]string{"set", "zr-abc", "role", "worker", "--label"}); err == nil {
		t.Fatal("trailing --label with no value must error")
	}
}

func TestMetaFlagsParse_rejectsEmptyLabelKey(t *testing.T) {
	if _, _, _, err := parseMetaFlags([]string{"set", "zr-abc", "--label", ""}); err == nil {
		t.Fatal("--label \"\" must error (empty key)")
	}
}

func TestMetaFlagsParse_noFlagsIsAllPositional(t *testing.T) {
	jsonOut, labels, pos, err := parseMetaFlags([]string{"get", "zr-abc", "role"})
	if err != nil {
		t.Fatalf("parseMetaFlags: %v", err)
	}
	if jsonOut || len(labels) != 0 {
		t.Errorf("jsonOut=%v labels=%v, want false/none", jsonOut, labels)
	}
	want := []string{"get", "zr-abc", "role"}
	if !reflect.DeepEqual(pos, want) {
		t.Errorf("pos = %v, want %v", pos, want)
	}
}
