package main

import (
	"reflect"
	"testing"
)

func TestEnvFlag_parsesAndAccumulatesRepeated(t *testing.T) {
	e := envFlag{}
	for _, kv := range []string{"BEADS_ACTOR=worker-1", "BEADS_DIR=/repo/.beads", "WORKSPACE_ROOT=/repo"} {
		if err := e.Set(kv); err != nil {
			t.Fatalf("Set(%q): %v", kv, err)
		}
	}
	want := map[string]string{
		"BEADS_ACTOR":    "worker-1",
		"BEADS_DIR":      "/repo/.beads",
		"WORKSPACE_ROOT": "/repo",
	}
	if !reflect.DeepEqual(map[string]string(e), want) {
		t.Errorf("envFlag = %v, want %v", map[string]string(e), want)
	}
}

func TestEnvFlag_valueMayContainEquals(t *testing.T) {
	e := envFlag{}
	if err := e.Set("FOO=a=b=c"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if e["FOO"] != "a=b=c" {
		t.Errorf("FOO = %q, want a=b=c (split on first '=' only)", e["FOO"])
	}
}

func TestEnvFlag_rejectsMissingEquals(t *testing.T) {
	e := envFlag{}
	if err := e.Set("NOEQUALS"); err == nil {
		t.Error("Set(\"NOEQUALS\") should error (want KEY=VAL)")
	}
}

func TestEnvFlag_allowsEmptyValue(t *testing.T) {
	e := envFlag{}
	if err := e.Set("EMPTY="); err != nil {
		t.Fatalf("Set(\"EMPTY=\"): %v", err)
	}
	if v, ok := e["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY = %q (present=%v), want empty string present", v, ok)
	}
}
