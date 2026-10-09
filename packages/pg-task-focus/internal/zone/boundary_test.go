package zone_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// TestHostZoneLinkSegmentBoundaries pins where a zone name starts in the
// local-time link: only after a "zoneinfo/" that is a whole path segment,
// which may also begin the target.
func TestHostZoneLinkSegmentBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantZone string // empty: Host MUST fail
	}{
		{name: "a target that begins with the zoneinfo segment", target: "zoneinfo/Europe/Paris", wantZone: "Europe/Paris"},
		{name: "zoneinfo inside a longer name after a letter is no segment", target: "/usr/share/myzoneinfo/Europe/Paris"},
		{name: "zoneinfo inside a longer name after a dash is no segment", target: "/usr/share/tz-zoneinfo/Europe/Paris"},
		{name: "zoneinfo inside a longer name after a dot is no segment", target: "/usr/share/tz.zoneinfo/Europe/Paris"},
		{name: "a target shorter than the marker names no zone", target: "UTC"},
		{name: "an empty target names no zone", target: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			z, err := zone.Host(
				func(string) string { return "" },
				func(string) (string, error) { return tt.target, nil },
			)
			if tt.wantZone == "" {
				if err == nil {
					t.Fatalf("Host() = %q for the link %q, want an error", z.Name(), tt.target)
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("%q", tt.target)) {
					t.Errorf("error %q does not name the link target %q", err, tt.target)
				}
				return
			}
			if err != nil {
				t.Fatalf("Host() error: %v", err)
			}
			if z.Name() != tt.wantZone {
				t.Fatalf("Host() = %q, want %q", z.Name(), tt.wantZone)
			}
		})
	}
}

// TestLoadSaysWhyANameIsRejected pins the reason each rejection gives: a bare
// offset or an unknown abbreviation is refused because the standard library
// does not know it, not because it is a malformed path, and a malformed path
// is refused as one.
func TestLoadSaysWhyANameIsRejected(t *testing.T) {
	const unknown, malformed = "the standard library does not know it", "is not a zone path"
	tests := []struct{ name, why string }{
		{"-05:00", unknown},
		{"+0530", unknown},
		{"UTC-5", unknown},
		{"PST", unknown},
		{"-", unknown},
		{"America/-05:00", unknown},
		{"America//New_York", malformed},
		{"America/./New_York", malformed},
		{"../America/New_York", malformed},
		{"America/New_York/", malformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := zone.Load(tt.name)
			var ze *zone.Error
			if !errors.As(err, &ze) {
				t.Fatalf("Load(%q) error %v (%T), want *zone.Error", tt.name, err, err)
			}
			if !strings.Contains(ze.Message, tt.why) {
				t.Errorf("Load(%q) says %q, want it to say %q", tt.name, ze.Message, tt.why)
			}
		})
	}
}

// utcTZif is a minimal version-1 TZif file: one zone type, UTC+0 named "UTC",
// and no transitions.
func utcTZif() []byte {
	b := []byte("TZif")
	b = append(b, 0)                               // version 1
	b = append(b, make([]byte, 15)...)             // reserved
	for _, n := range []uint32{0, 0, 0, 0, 1, 4} { // isutcnt isstdcnt leapcnt timecnt typecnt charcnt
		b = binary.BigEndian.AppendUint32(b, n)
	}
	b = append(b, 0, 0, 0, 0, 0, 0) // utoff 0, isdst 0, abbrind 0
	return append(b, "UTC\x00"...)
}

// TestHelperLoadNames is the child half of TestLoadChecksTheSpellingInZONEINFO.
// It does nothing unless the parent set the guard variable.
func TestHelperLoadNames(t *testing.T) {
	names := os.Getenv("PG_TASK_FOCUS_HELPER_NAMES")
	if names == "" {
		return
	}
	for _, name := range strings.Split(names, ",") {
		if _, err := zone.Load(name); err != nil {
			fmt.Printf("REJECTED %s\n", name)
		} else {
			fmt.Printf("LOADED %s\n", name)
		}
	}
}

// TestLoadChecksTheSpellingInZONEINFO pins that a zone file found through the
// ZONEINFO directory is accepted in its exact spelling only: on a
// case-insensitive file system the standard library would also open it under
// another case, and the spelling check MUST read that directory too.
// ZONEINFO is read once per process, so the loads run in a child.
func TestLoadChecksTheSpellingInZONEINFO(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Pgtf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Pgtf", "Synthetic"), utcTZif(), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLoadNames$") // #nosec G204 -- re-executes this test binary
	cmd.Env = append(os.Environ(), "ZONEINFO="+dir, "PG_TASK_FOCUS_HELPER_NAMES=Pgtf/Synthetic,pgtf/synthetic,PGTF/Synthetic")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	for _, want := range []string{"LOADED Pgtf/Synthetic\n", "REJECTED pgtf/synthetic\n", "REJECTED PGTF/Synthetic\n"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the child did not print %q:\n%s", want, out)
		}
	}
}
