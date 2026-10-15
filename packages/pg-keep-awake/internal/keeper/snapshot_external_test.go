package keeper

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// fakePmset writes an executable script and returns its path.
func fakePmset(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pmset")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestParseAssertionsCaptured parses output captured from `pmset -g
// assertions` on the Mac (trimmed) and excludes this tool's own caffeinate pid.
func TestParseAssertionsCaptured(t *testing.T) {
	out := readFixture(t, "pmset-assertions.captured.txt")

	all := ParseAssertions(out, 0)
	wantAll := []lease.StatusExternal{
		{PID: 12129, Process: "caffeinate", Type: "PreventUserIdleSystemSleep", AgeS: 177},
		{PID: 79878, Process: "caffeinate", Type: "PreventUserIdleSystemSleep", AgeS: 4*3600 + 45*60 + 14},
		{PID: 79878, Process: "caffeinate", Type: "PreventSystemSleep", AgeS: 4*3600 + 45*60 + 14},
		{PID: 479, Process: "WindowServer", Type: "UserIsActive", AgeS: 1},
		{PID: 419, Process: "powerd", Type: "PreventUserIdleSystemSleep", AgeS: 95},
	}
	if !reflect.DeepEqual(all, wantAll) {
		t.Fatalf("with no own pid:\n got %+v\nwant %+v", all, wantAll)
	}

	// pid 79878 is "ours": both of its assertions disappear, the others stay.
	got := ParseAssertions(out, 79878)
	want := []lease.StatusExternal{wantAll[0], wantAll[3], wantAll[4]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("excluding own caffeinate 79878:\n got %+v\nwant %+v", got, want)
	}
}

func TestParseAssertionsVariants(t *testing.T) {
	out := readFixture(t, "pmset-assertions.variants.txt")
	got := ParseAssertions(out, 0)
	// The unparsable age and pid lines are skipped; the rest survive.
	want := []lease.StatusExternal{
		{PID: 355, Process: "Google Chrome Helper (Renderer)", Type: "PreventUserIdleDisplaySleep", AgeS: 3723},
		{PID: 612, Process: "coreaudiod", Type: "PreventUserIdleSystemSleep", AgeS: 2*86400 + 3*3600 + 4*60 + 5},
		{PID: 801, Process: "NetworkSync", Type: "NoIdleSleepAssertion", AgeS: 600},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseAssertionsUnparsableOutputIsEmptyNotNil(t *testing.T) {
	for _, out := range []string{"", "not pmset output at all\n", "pid 1(x): broken\n", "Listed by owning process:\n"} {
		got := ParseAssertions(out, 0)
		if got == nil || len(got) != 0 {
			t.Errorf("ParseAssertions(%q) = %#v, want empty non-nil", out, got)
		}
	}
}

func TestExternalProbeRunsPmsetWithItsArguments(t *testing.T) {
	captured := filepath.Join("testdata", "pmset-assertions.captured.txt")
	abs, err := filepath.Abs(captured)
	if err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args")
	script := fakePmset(t, `echo "$@" > "`+argsFile+`"; cat "`+abs+`"`)

	got := ExternalProbe{Path: script}.Probe(79878)
	if len(got) != 3 {
		t.Fatalf("got %d holders, want 3: %+v", len(got), got)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil || string(args) != "-g assertions\n" {
		t.Errorf("pmset args = %q (%v), want \"-g assertions\"", args, err)
	}
}

func TestExternalProbeFailsSoft(t *testing.T) {
	cases := map[string]ExternalProbe{
		"non-zero exit":        {Path: fakePmset(t, "echo oops >&2; exit 3")},
		"missing binary":       {Path: filepath.Join(t.TempDir(), "no-such-pmset")},
		"garbage output":       {Path: fakePmset(t, "echo 'completely unparsable'")},
		"empty output":         {Path: fakePmset(t, "true")},
		"timeout (hung pmset)": {Path: fakePmset(t, "exec sleep 30"), Timeout: 150 * time.Millisecond},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			got := p.Probe(0)
			if got == nil || len(got) != 0 {
				t.Fatalf("Probe = %#v, want empty non-nil", got)
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("Probe took %v, want it bounded by the timeout", d)
			}
		})
	}
}

// TestExternalProbeFailureDoesNotFailTheTick: a failed probe feeds an empty
// external list into the snapshot, which still builds and writes.
func TestExternalProbeFailureDoesNotFailTheTick(t *testing.T) {
	d := newDir(t)
	in := baseInputs()
	in.External = ExternalProbe{Path: fakePmset(t, "exit 1")}.Probe(0)
	if err := WriteSnapshot(d, BuildSnapshot(in)); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	st, err := d.ReadStatus()
	if err != nil || len(st.External) != 0 {
		t.Fatalf("external = %+v, err = %v, want empty", st, err)
	}
	if raw := rawStatus(t, d); raw["external"] == nil {
		t.Error("external must be [] and never null")
	}
}

func TestParseAssertionAge(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"00:00:00", 0, true},
		{"00:02:57", 177, true},
		{"05:15:51", 18951, true},
		{"1+00:00:01", 86401, true},
		{"12+23:59:59", 12*86400 + 86399, true},
		{"", 0, false},
		{"12:34", 0, false},
		{"a:b:c", 0, false},
		{"-1:00:00", 0, false},
		{"x+00:00:00", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseAssertionAge(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseAssertionAge(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
