package main

import (
	"flag"
	"fmt"
	"reflect"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/launch"
)

// TestRunNew_rejectsUnknownPermissionMode pins the CLI validation contract: an
// unknown --permission-mode value is a usage error (exit 2), consistent with the
// other usage failures in runNew, and is rejected BEFORE any config/store I/O.
func TestRunNew_rejectsUnknownPermissionMode(t *testing.T) {
	if code := runNew([]string{"alpha", "--permission-mode", "nope"}); code != 2 {
		t.Errorf("runNew with unknown --permission-mode = %d, want 2", code)
	}
}

// TestRunNew_acceptsEachValidPermissionMode asserts every documented mode passes
// validation. Each valid value must NOT be rejected as a usage error (exit 2);
// validation happens before config/store I/O, so we only assert it is not the
// usage-error code (a later config/store failure under the test harness is
// expected and not what we are pinning here).
func TestRunNew_acceptsEachValidPermissionMode(t *testing.T) {
	for _, mode := range launch.ValidPermissionModes() {
		t.Run(string(mode), func(t *testing.T) {
			if !launch.PermissionMode(mode).Valid() {
				t.Fatalf("%q should validate", mode)
			}
		})
	}
}

func TestRunNew_acceptsAllowedToolsFlag(t *testing.T) {
	// --allowed-tools is a free-form passthrough: any value parses (no validation).
	// A missing external_id is the only usage error here; with the id present and
	// the flag set, parsing must succeed past the flag stage.
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	allowed := fs.String("allowed-tools", "", "")
	pos := parseInterspersed(fs, []string{"zr-abc", "--allowed-tools", "Bash(git *),Edit"})
	if len(pos) != 1 || pos[0] != "zr-abc" {
		t.Fatalf("positional parse = %v, want [zr-abc]", pos)
	}
	if *allowed != "Bash(git *),Edit" {
		t.Errorf("allowed-tools = %q, want %q", *allowed, "Bash(git *),Edit")
	}
}

func TestRunNew_parsesAutonomousFlag(t *testing.T) {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	autonomous := fs.Bool("autonomous", false, "")
	pos := parseInterspersed(fs, []string{"zr-1", "--autonomous"})
	if len(pos) != 1 || pos[0] != "zr-1" {
		t.Fatalf("positional parse = %v, want [zr-1]", pos)
	}
	if !*autonomous {
		t.Error("--autonomous should parse to true")
	}
}

func TestRunNew_autonomousDefaultsFalse(t *testing.T) {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	autonomous := fs.Bool("autonomous", false, "")
	_ = parseInterspersed(fs, []string{"zr-1"})
	if *autonomous {
		t.Error("--autonomous must default to false (attended)")
	}
}

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

func TestMetaFlag_collectsRepeatedPairs(t *testing.T) {
	m := metaFlag{}
	for _, kv := range []string{"pgrouter.bead=zr-1", "pgrouter.role=worker"} {
		if err := m.Set(kv); err != nil {
			t.Fatalf("Set(%q): %v", kv, err)
		}
	}
	want := metaFlag{"pgrouter.bead": "zr-1", "pgrouter.role": "worker"}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("metaFlag = %v, want %v", m, want)
	}
}

func TestMetaFlag_rejectsMissingEquals(t *testing.T) {
	if err := (metaFlag{}).Set("noequals"); err == nil {
		t.Fatal("metaFlag.Set without '=' must error")
	}
}

func TestMetaFlag_allowsEmptyValue(t *testing.T) {
	m := metaFlag{}
	if err := m.Set("pgrouter.pinned="); err != nil {
		t.Fatalf("Set bare tag: %v", err)
	}
	if v, ok := m["pgrouter.pinned"]; !ok || v != "" {
		t.Errorf("bare tag = (%q,%v), want (\"\",true)", v, ok)
	}
}

func TestNewLabelFlag_collectsRepeatedKeys(t *testing.T) {
	l := labelFlag{}
	for _, k := range []string{"pgrouter.role", "pgrouter.pool"} {
		if err := l.Set(k); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}
	want := labelFlag{"pgrouter.role": true, "pgrouter.pool": true}
	if !reflect.DeepEqual(l, want) {
		t.Errorf("labelFlag = %v, want %v", l, want)
	}
}

func TestNewLabelFlag_rejectsEmptyKey(t *testing.T) {
	if err := (labelFlag{}).Set(""); err == nil {
		t.Fatal("labelFlag.Set(\"\") must error")
	}
}

func TestNewLabelFlag_parsesViaFlagSetRepeatable(t *testing.T) {
	// --label is repeatable and may follow the positional external_id, exactly
	// like --meta/--env (mirrors TestRunNew_acceptsAllowedToolsFlag's pattern).
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	label := labelFlag{}
	fs.Var(label, "label", "")
	pos := parseInterspersed(fs, []string{"zr-abc", "--label", "role", "--label", "pool"})
	if len(pos) != 1 || pos[0] != "zr-abc" {
		t.Fatalf("positional parse = %v, want [zr-abc]", pos)
	}
	want := labelFlag{"role": true, "pool": true}
	if !reflect.DeepEqual(label, want) {
		t.Errorf("labelFlag after parse = %v, want %v", label, want)
	}
}

// fakeLabelMarker is a labelMarker test double recording every MarkAsLabel
// call, optionally erroring for named keys — lets applyLabels' wiring (used by
// both `ccpool new` and `ccpool meta set`) be unit-tested without a real store.
type fakeLabelMarker struct {
	marked  map[string]bool
	errKeys map[string]error
}

func (f *fakeLabelMarker) MarkAsLabel(externalID, key string) error {
	if f.errKeys != nil {
		if err, ok := f.errKeys[key]; ok {
			return err
		}
	}
	if f.marked == nil {
		f.marked = map[string]bool{}
	}
	f.marked[externalID+"/"+key] = true
	return nil
}

func TestNewApplyLabels_callsMarkAsLabelForEachKey(t *testing.T) {
	m := &fakeLabelMarker{}
	labels := labelFlag{"role": true, "pool": true}
	if err := applyLabels(m, "zr-abc", labels); err != nil {
		t.Fatalf("applyLabels: %v", err)
	}
	want := map[string]bool{"zr-abc/role": true, "zr-abc/pool": true}
	if !reflect.DeepEqual(m.marked, want) {
		t.Errorf("marked = %v, want %v", m.marked, want)
	}
}

func TestNewApplyLabels_emptySetIsNoop(t *testing.T) {
	m := &fakeLabelMarker{}
	if err := applyLabels(m, "zr-abc", labelFlag{}); err != nil {
		t.Fatalf("applyLabels(empty): %v", err)
	}
	if len(m.marked) != 0 {
		t.Errorf("marked = %v, want none", m.marked)
	}
}

func TestNewApplyLabels_propagatesMarkAsLabelError(t *testing.T) {
	wantErr := fmt.Errorf("mark as label %q/%q: key not found", "zr-abc", "bead")
	m := &fakeLabelMarker{errKeys: map[string]error{"bead": wantErr}}
	if err := applyLabels(m, "zr-abc", labelFlag{"bead": true}); err != wantErr {
		t.Errorf("applyLabels error = %v, want %v", err, wantErr)
	}
}
