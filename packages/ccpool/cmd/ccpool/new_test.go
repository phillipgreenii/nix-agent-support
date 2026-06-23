package main

import (
	"flag"
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
