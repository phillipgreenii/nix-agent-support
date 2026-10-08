package zone_test

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

func mustLoad(t testing.TB, name string) zone.Zone {
	t.Helper()
	z, err := zone.Load(name)
	if err != nil {
		t.Fatalf("Load(%q): %v", name, err)
	}
	return z
}

func mustTime(t testing.TB, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test instant %q: %v", s, err)
	}
	return v
}

func TestLoadAcceptsIANA(t *testing.T) {
	for _, name := range []string{"America/New_York", "Europe/London", "Asia/Kolkata", "UTC", "Etc/GMT+5"} {
		t.Run(name, func(t *testing.T) {
			z, err := zone.Load(name)
			if err != nil {
				t.Fatalf("Load(%q): %v", name, err)
			}
			if z.Name() != name {
				t.Errorf("Name() = %q, want %q", z.Name(), name)
			}
			if z.Location() == nil {
				t.Errorf("Location() is nil")
			}
		})
	}
}

func TestLoadAppliesZoneOffsets(t *testing.T) {
	z := mustLoad(t, "America/New_York")
	_, winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).In(z.Location()).Zone()
	_, summer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).In(z.Location()).Zone()
	if winter != -5*3600 || summer != -4*3600 {
		t.Fatalf("New York offsets = %d (winter), %d (summer); want -18000, -14400", winter, summer)
	}
}

func TestLoadAcceptsLegacyNamesTheStandardLibraryKnows(t *testing.T) {
	// The offsets of these names differ across tz database releases, so the
	// test deliberately pins none of them.
	for _, name := range []string{"EST", "EST5EDT"} {
		t.Run(name, func(t *testing.T) {
			if _, err := zone.Load(name); err != nil {
				t.Fatalf("Load(%q): %v", name, err)
			}
		})
	}
}

func TestLoadRejectsImplicitAndUnknownZones(t *testing.T) {
	for _, name := range []string{"", "Local", "ET", "PST", "-05:00", "+0530", "UTC-5"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			_, err := zone.Load(name)
			assertInvalidZone(t, name, err)
		})
	}
}

func TestLoadRejectsMalformedPaths(t *testing.T) {
	for _, name := range []string{"America//New_York", "/America/New_York", "America/New_York/", "America/./New_York", "../America/New_York", "America/../America/New_York", `America\New_York`, "America"} {
		t.Run(name, func(t *testing.T) {
			_, err := zone.Load(name)
			assertInvalidZone(t, name, err)
		})
	}
}

func TestLoadRequiresExactSpelling(t *testing.T) {
	// A case-insensitive host file system (APFS) lets time.LoadLocation
	// resolve the odd-cased forms, so the implementation checks the spelling.
	for _, name := range []string{" America/New_York", "america/new_york", "America/new_york", "AMERICA/NEW_YORK", "utc", "est5edt"} {
		t.Run(name, func(t *testing.T) {
			_, err := zone.Load(name)
			assertInvalidZone(t, name, err)
		})
	}
}

func assertInvalidZone(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("Load(%q) succeeded, want invalid_zone", name)
	}
	var ze *zone.Error
	if !errors.As(err, &ze) {
		t.Fatalf("Load(%q) error %T (%v), want *zone.Error", name, err, err)
	}
	if ze.Reason != "invalid_zone" {
		t.Errorf("Load(%q) reason = %q, want invalid_zone", name, ze.Reason)
	}
	if ze.Error() == "" {
		t.Errorf("Load(%q) error has no message", name)
	}
}

// TestHelperLoad is the child half of TestLoadIgnoresBrokenZONEINFO. It does
// nothing unless the parent set the guard variable.
func TestHelperLoad(t *testing.T) {
	if os.Getenv("PG_TASK_FOCUS_HELPER") != "1" {
		return
	}
	z, err := zone.Load("America/New_York")
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	_, off := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).In(z.Location()).Zone()
	fmt.Printf("LOADED %s %d\n", z.Name(), off)
}

// TestLoadIgnoresBrokenZONEINFO checks that an unusable ZONEINFO does not
// break Load. It does not prove the embedded copy answered: on a host with
// zone files those answer first. ZONEINFO is read once per process, so each
// case runs the test binary as a child.
func TestLoadIgnoresBrokenZONEINFO(t *testing.T) {
	corrupt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(corrupt, "America"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, "America", "New_York"), []byte("not a zone file"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"nonexistent directory": filepath.Join(t.TempDir(), "does-not-exist"),
		"corrupted zone file":   corrupt,
	}
	for name, zoneinfo := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLoad$") // #nosec G204 -- re-executes this test binary
			cmd.Env = append(os.Environ(), "ZONEINFO="+zoneinfo, "PG_TASK_FOCUS_HELPER=1")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "LOADED America/New_York -14400\n") {
				t.Fatalf("child did not load New York correctly:\n%s", out)
			}
		})
	}
}

// TestEmbeddedTZDataIsLinked pins the rule that no zone database is committed
// and none is required on the host: zone.go MUST import time/tzdata. Removing
// the blank import turns this red.
func TestEmbeddedTZDataIsLinked(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "zone.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse zone.go: %v", err)
	}
	for _, imp := range f.Imports {
		if imp.Path.Value == `"time/tzdata"` {
			if imp.Name == nil || imp.Name.Name != "_" {
				t.Fatalf("time/tzdata is imported but not as a blank import")
			}
			return
		}
	}
	t.Fatal(`zone.go does not import "time/tzdata"`)
}

func TestToday(t *testing.T) {
	tests := []struct {
		zone string
		now  string
		want civil.Date
	}{
		{"America/New_York", "2026-10-08T02:00:00Z", civil.Date{Year: 2026, Month: time.October, Day: 7}},
		{"America/New_York", "2026-10-08T04:00:00Z", civil.Date{Year: 2026, Month: time.October, Day: 8}},
		{"Asia/Kolkata", "2026-10-07T18:30:00Z", civil.Date{Year: 2026, Month: time.October, Day: 8}},
		{"Asia/Kolkata", "2026-10-07T18:29:59Z", civil.Date{Year: 2026, Month: time.October, Day: 7}},
		{"UTC", "2026-12-31T23:59:59Z", civil.Date{Year: 2026, Month: time.December, Day: 31}},
	}
	for _, tt := range tests {
		got := zone.Today(mustLoad(t, tt.zone), mustTime(t, tt.now))
		if got != tt.want {
			t.Errorf("Today(%s, %s) = %v, want %v", tt.zone, tt.now, got, tt.want)
		}
	}
}
