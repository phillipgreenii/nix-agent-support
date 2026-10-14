package claudetranscript

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeProcStartFixture writes a registry file for pid with the given raw
// procStart JSON fragment ("" omits the field) and returns the parsed row.
func writeProcStartFixture(t *testing.T, pid int, procStartJSON string) RegistrySession {
	t.Helper()
	extra := ""
	if procStartJSON != "" {
		extra = `,"procStart":` + procStartJSON
	}
	body := fmt.Sprintf(`{"pid":%d,"sessionId":"s","cwd":"/tmp","kind":"interactive","entrypoint":"cli","status":"busy","startedAt":1,"statusUpdatedAt":1%s}`, pid, extra)
	path := filepath.Join(t.TempDir(), fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := ReadSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestParseProcStart(t *testing.T) {
	want := time.Date(2026, 10, 14, 7, 51, 50, 0, time.UTC)
	for _, in := range []string{"Wed Oct 14 07:51:50 2026", "  Wed Oct 14 07:51:50 2026\n"} {
		got, ok := parseProcStart(in)
		if !ok || !got.Equal(want) {
			t.Errorf("parseProcStart(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	// ps pads single-digit days with a second space.
	got, ok := parseProcStart("Sun Nov  1 00:06:01 2026")
	if !ok || !got.Equal(time.Date(2026, 11, 1, 0, 6, 1, 0, time.UTC)) {
		t.Errorf("padded day: got %v, %v", got, ok)
	}
	for _, in := range []string{"", "   ", "yesterday", "1791936376916"} {
		if _, ok := parseProcStart(in); ok {
			t.Errorf("parseProcStart(%q) ok = true, want false", in)
		}
	}
}

func TestReadSessionFile_procStart(t *testing.T) {
	reg := writeProcStartFixture(t, 4242, `"Wed Oct 14 07:51:50 2026"`)
	if want := time.Date(2026, 10, 14, 7, 51, 50, 0, time.UTC); !reg.ProcStart.Equal(want) {
		t.Errorf("ProcStart = %v, want %v", reg.ProcStart, want)
	}
	for name, frag := range map[string]string{"absent": "", "null": "null", "garbage": `"not a time"`} {
		if got := writeProcStartFixture(t, 4242, frag); !got.ProcStart.IsZero() {
			t.Errorf("%s: ProcStart = %v, want zero", name, got.ProcStart)
		}
	}
}

// The test process's own PID is alive and ps reports its real start time, so
// these exercise the production path (real ps) end to end.
func TestSessionAlive_livePid(t *testing.T) {
	self := os.Getpid()
	start, err := ProcessStartTime(self)
	if err != nil {
		t.Skipf("ps cannot report this process's start time: %v", err)
	}
	if time.Since(start) < -3*time.Second || time.Since(start) > 24*time.Hour {
		t.Fatalf("ProcessStartTime(self) = %v, implausible", start)
	}
	frag := func(ts time.Time) string { return `"` + ts.UTC().Format(procStartLayout) + `"` }

	t.Run("matching procStart is accepted", func(t *testing.T) {
		if !SessionAlive(writeProcStartFixture(t, self, frag(start))) {
			t.Error("SessionAlive = false for matching procStart, want true")
		}
	})
	t.Run("one second off is within tolerance", func(t *testing.T) {
		if !SessionAlive(writeProcStartFixture(t, self, frag(start.Add(time.Second)))) {
			t.Error("SessionAlive = false for +1s procStart, want true")
		}
	})
	t.Run("alive PID with mismatching procStart (PID reuse) is rejected", func(t *testing.T) {
		reg := writeProcStartFixture(t, self, frag(start.Add(-2*time.Hour)))
		if !PidAlive(reg.PID) {
			t.Fatal("precondition: PID must be alive")
		}
		if SessionAlive(reg) {
			t.Error("SessionAlive = true for alive PID with procStart 2h off, want false")
		}
	})
	t.Run("absent procStart keeps PID-only behavior", func(t *testing.T) {
		if !SessionAlive(writeProcStartFixture(t, self, "")) {
			t.Error("SessionAlive = false without procStart, want true (PID-only fallback)")
		}
	})
	t.Run("dead PID is rejected even with matching-looking procStart", func(t *testing.T) {
		if SessionAlive(writeProcStartFixture(t, 2147483646, frag(start))) {
			t.Error("SessionAlive = true for dead PID, want false")
		}
	})
}

func TestProcStartMatches_tolerance(t *testing.T) {
	base := time.Date(2026, 10, 14, 7, 51, 50, 0, time.UTC)
	at := func(live time.Time, err error) func(int) (time.Time, error) {
		return func(int) (time.Time, error) { return live, err }
	}
	reg := RegistrySession{PID: 1, ProcStart: base}
	for _, tc := range []struct {
		name string
		live time.Time
		err  error
		want bool
	}{
		{"exact", base, nil, true},
		{"+2s edge", base.Add(2 * time.Second), nil, true},
		{"-2s edge", base.Add(-2 * time.Second), nil, true},
		{"+3s", base.Add(3 * time.Second), nil, false},
		{"-3s", base.Add(-3 * time.Second), nil, false},
		{"ps failure fails open", time.Time{}, os.ErrNotExist, true},
	} {
		if got := procStartMatches(reg, at(tc.live, tc.err)); got != tc.want {
			t.Errorf("%s: procStartMatches = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !procStartMatches(RegistrySession{PID: 1}, at(base, nil)) {
		t.Error("zero ProcStart must match (older registries)")
	}
}
