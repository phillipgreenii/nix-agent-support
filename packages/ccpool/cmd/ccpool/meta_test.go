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
