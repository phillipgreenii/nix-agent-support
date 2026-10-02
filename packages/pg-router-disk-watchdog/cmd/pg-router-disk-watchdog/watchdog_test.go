package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const gib = 1 << 30

// fakeRouter records every pg-router call and answers `gate list --json` from
// a configurable set of active gates.
type fakeRouter struct {
	calls   [][]string
	listing string // body returned for `gate list --json`
	failOn  string // a verb ("set", "clear", "list") that errors
}

func (f *fakeRouter) run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{bin}, args...))
	if len(args) >= 2 && args[0] == "gate" {
		if args[1] == f.failOn {
			return nil, errors.New("no running core")
		}
		if args[1] == "list" {
			return []byte(f.listing), nil
		}
	}
	return nil, nil
}

// verbs lists the `gate <verb>` calls made, in order.
func (f *fakeRouter) verbs() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c[2])
	}
	return out
}

func testConfig() config {
	return config{
		paths:        []string{"/", "/Volumes/data"},
		minFree:      20 * gib,
		recoverFree:  25 * gib,
		gateType:     "LOW_DISK_USAGE",
		owner:        "wd",
		ttl:          7 * time.Minute,
		pgRouterPath: "pg-router",
	}
}

func free(m map[string]uint64) func(string) (uint64, error) {
	return func(p string) (uint64, error) {
		v, ok := m[p]
		if !ok {
			return 0, errors.New("no such path")
		}
		return v, nil
	}
}

func runCheck(t *testing.T, cfg config, f *fakeRouter, disk map[string]uint64) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = check(context.Background(), cfg, deps{freeBytes: free(disk), run: f.run, stdout: &out, stderr: &errb})
	return code, out.String(), errb.String()
}

func TestCheck_lowSpaceSetsGateWithLeaseOwnerAndDescription(t *testing.T) {
	f := &fakeRouter{}
	code, out, _ := runCheck(t, testConfig(), f, map[string]uint64{"/": 12 * gib, "/Volumes/data": 12 * gib})
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v, want exactly one gate set (no list needed while low)", f.calls)
	}
	got := strings.Join(f.calls[0], " ")
	for _, want := range []string{"pg-router gate set LOW_DISK_USAGE", "--owner wd", "--ttl 7m0s", "12.0 GiB free on /", "minimum 20.0 GiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("gate set call %q missing %q", got, want)
		}
	}
	if !strings.Contains(out, "LOW_DISK_USAGE set") {
		t.Errorf("stdout = %q", out)
	}
}

func TestCheck_lowSpaceReSetsEveryPass_leaseRenewal(t *testing.T) {
	f := &fakeRouter{}
	for i := 0; i < 3; i++ {
		runCheck(t, testConfig(), f, map[string]uint64{"/": 5 * gib, "/Volumes/data": 5 * gib})
	}
	if got := strings.Join(f.verbs(), ","); got != "set,set,set" {
		t.Fatalf("verbs = %s, want a set on every low pass", got)
	}
}

func TestCheck_lowestPathWins(t *testing.T) {
	f := &fakeRouter{}
	runCheck(t, testConfig(), f, map[string]uint64{"/": 500 * gib, "/Volumes/data": 3 * gib})
	if len(f.calls) != 1 || !strings.Contains(strings.Join(f.calls[0], " "), "3.0 GiB free on /Volumes/data") {
		t.Fatalf("calls = %v, want a set naming the low volume", f.calls)
	}
}

func TestCheck_recoveryClearsOwnGate(t *testing.T) {
	f := &fakeRouter{listing: `[{"type":"LOW_DISK_USAGE","owner":"wd"}]`}
	code, _, _ := runCheck(t, testConfig(), f, map[string]uint64{"/": 40 * gib, "/Volumes/data": 40 * gib})
	if code != exitOK || strings.Join(f.verbs(), ",") != "list,clear" {
		t.Fatalf("code=%d verbs=%v, want list then clear", code, f.verbs())
	}
	if last := f.calls[1]; last[3] != "LOW_DISK_USAGE" || last[4] != "--by" || last[5] != "wd" {
		t.Errorf("clear call = %v", last)
	}
}

func TestCheck_healthyWithNoGateWritesNothing(t *testing.T) {
	f := &fakeRouter{listing: `[]`}
	code, out, _ := runCheck(t, testConfig(), f, map[string]uint64{"/": 100 * gib, "/Volumes/data": 100 * gib})
	if code != exitOK || strings.Join(f.verbs(), ",") != "list" || !strings.Contains(out, "ok") {
		t.Fatalf("code=%d verbs=%v out=%q, want a read-only pass", code, f.verbs(), out)
	}
}

func TestCheck_hysteresisBandHoldsOwnGateButDoesNotCreateOne(t *testing.T) {
	band := map[string]uint64{"/": 22 * gib, "/Volumes/data": 22 * gib} // 20 <= free < 25
	own := &fakeRouter{listing: `[{"type":"LOW_DISK_USAGE","owner":"wd"}]`}
	runCheck(t, testConfig(), own, band)
	if got := strings.Join(own.verbs(), ","); got != "list,set" {
		t.Errorf("own gate in band: verbs = %s, want list,set (lease renewed, not cleared)", got)
	}
	none := &fakeRouter{listing: `[]`}
	runCheck(t, testConfig(), none, band)
	if got := strings.Join(none.verbs(), ","); got != "list" {
		t.Errorf("no gate in band: verbs = %s, want list only", got)
	}
}

// A LOW_DISK_USAGE the operator set by hand has a different owner and must
// survive a healthy disk; manual gate set/clear keeps working alongside.
func TestCheck_neverClearsAnotherOwnersGate(t *testing.T) {
	f := &fakeRouter{listing: `[{"type":"LOW_DISK_USAGE","owner":"operator"},{"type":"SYSTEM_PAUSE","owner":"wd"}]`}
	code, out, _ := runCheck(t, testConfig(), f, map[string]uint64{"/": 100 * gib, "/Volumes/data": 100 * gib})
	if code != exitOK || strings.Join(f.verbs(), ",") != "list" || !strings.Contains(out, "another owner") {
		t.Fatalf("code=%d verbs=%v out=%q, want it left alone", code, f.verbs(), out)
	}
}

func TestCheck_failuresLeaveGateUntouched(t *testing.T) {
	t.Run("unmeasurable path", func(t *testing.T) {
		f := &fakeRouter{}
		code, _, errOut := runCheck(t, testConfig(), f, map[string]uint64{"/": 100 * gib})
		if code != exitError || len(f.calls) != 0 || !strings.Contains(errOut, "/Volumes/data") {
			t.Fatalf("code=%d calls=%v err=%q", code, f.calls, errOut)
		}
	})
	t.Run("set fails (no running core)", func(t *testing.T) {
		f := &fakeRouter{failOn: "set"}
		code, _, errOut := runCheck(t, testConfig(), f, map[string]uint64{"/": 1 * gib, "/Volumes/data": 1 * gib})
		if code != exitError || !strings.Contains(errOut, "no running core") {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
	})
	t.Run("list fails", func(t *testing.T) {
		f := &fakeRouter{failOn: "list"}
		code, _, _ := runCheck(t, testConfig(), f, map[string]uint64{"/": 100 * gib, "/Volumes/data": 100 * gib})
		if code != exitError || strings.Join(f.verbs(), ",") != "list" {
			t.Fatalf("code=%d verbs=%v", code, f.verbs())
		}
	})
	t.Run("clear fails", func(t *testing.T) {
		f := &fakeRouter{failOn: "clear", listing: `[{"type":"LOW_DISK_USAGE","owner":"wd"}]`}
		code, _, _ := runCheck(t, testConfig(), f, map[string]uint64{"/": 100 * gib, "/Volumes/data": 100 * gib})
		if code != exitError {
			t.Fatalf("code=%d", code)
		}
	})
	t.Run("unparseable list", func(t *testing.T) {
		f := &fakeRouter{listing: `not json`}
		code, _, _ := runCheck(t, testConfig(), f, map[string]uint64{"/": 100 * gib, "/Volumes/data": 100 * gib})
		if code != exitError {
			t.Fatalf("code=%d", code)
		}
	})
}

func TestParseSize(t *testing.T) {
	good := map[string]uint64{
		"20GiB": 20 * gib, "20 gib": 20 * gib, "512MiB": 512 << 20, "1.5TiB": 3 << 39,
		"1024": 1024, "2KiB": 2048, "7B": 7,
	}
	for in, want := range good {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "GiB", "20GB", "-1GiB", "1.2.3GiB", "abc"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) succeeded, want an error", in)
		}
	}
}

func TestRun_defaultsAndFlagValidation(t *testing.T) {
	var gotCfg config
	capture := func(stdout, stderr io.Writer) deps {
		return deps{
			freeBytes: func(p string) (uint64, error) { gotCfg.paths = append(gotCfg.paths, p); return 100 * gib, nil },
			run:       func(context.Context, string, ...string) ([]byte, error) { return []byte("[]"), nil },
			stdout:    stdout, stderr: stderr,
		}
	}
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb, capture); code != exitOK {
		t.Fatalf("defaults: exit %d, stderr %q", code, errb.String())
	}
	if len(gotCfg.paths) != 1 || gotCfg.paths[0] != "/" {
		t.Errorf("default path = %v, want [/]", gotCfg.paths)
	}

	cfg, err := buildConfig(nil, defaultMinFree, "", defaultGate, defaultOwner, defaultTTL, "pg-router", time.Second)
	if err != nil || cfg.minFree != 20*gib || cfg.recoverFree != 25*gib || cfg.ttl != 7*time.Minute || cfg.gateType != "LOW_DISK_USAGE" {
		t.Errorf("defaults = %+v, %v; want 20GiB min, 25GiB recover, 7m ttl, LOW_DISK_USAGE", cfg, err)
	}

	for name, args := range map[string][]string{
		"bad size":          {"--min-free", "lots"},
		"zero threshold":    {"--min-free", "0"},
		"recover below min": {"--min-free", "20GiB", "--recover-free", "10GiB"},
		"lowercase gate":    {"--gate", "low_disk"},
		"non-positive ttl":  {"--ttl", "0s"},
		"stray positional":  {"extra"},
		"unknown flag":      {"--nope"},
		"empty owner":       {"--owner", ""},
	} {
		if code := run(args, io.Discard, io.Discard, capture); code != exitUsage {
			t.Errorf("%s: exit = %d, want %d", name, code, exitUsage)
		}
	}
	if code := run([]string{"--version"}, &out, io.Discard, capture); code != exitOK {
		t.Errorf("--version exit = %d", code)
	}
}

func TestStatfsFree_realFilesystem(t *testing.T) {
	if got, err := statfsFree(t.TempDir()); err != nil || got == 0 {
		t.Fatalf("statfsFree(tempdir) = %d, %v; want a positive free-byte count", got, err)
	}
	if _, err := statfsFree("/definitely/not/a/path"); err == nil {
		t.Fatal("statfsFree of a missing path must error, not read as healthy")
	}
}

func TestExecRun_capturesStdoutAndStderrOnFailure(t *testing.T) {
	out, err := execRun(context.Background(), "sh", "-c", "printf hi")
	if err != nil || string(out) != "hi" {
		t.Fatalf("execRun ok path = %q, %v", out, err)
	}
	_, err = execRun(context.Background(), "sh", "-c", "echo no running core >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "no running core") {
		t.Fatalf("execRun failure = %v, want the child's stderr in the error", err)
	}
}
