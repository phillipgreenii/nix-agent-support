package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clausesFixture writes a config with one level source whose command is a shell
// fixture honoring --raw and --bands, one resource per entry of resources
// (TOML [[resource]] bodies) and the roles of rolesTOML, and points the
// environment at it. sensor is the fixture's body: it runs with $1 set to the
// flag.
func clausesFixture(t *testing.T, sensor, resources, rolesTOML string) {
	t.Helper()
	dir := shortDir(t)
	script := filepath.Join(dir, "sensor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+sensor+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := resources + `
[[query]]
name = "host-probe"
type = "command"
level_source = "host-sensor"
[query.command]
argv = ["` + script + `"]
format = "jsonl"
` + rolesTOML
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("PG_ROUTER_GLOBAL_CONFIG", filepath.Join(dir, "no-global.toml"))
	t.Setenv("PG_ROUTER_LOG_DIR", logDir)
}

const clausesDiskResource = `
[[resource]]
name = "disk"
scale = ["PLENTY", "OK", "TIGHT", "EXHAUSTED"]
sensor = "host-sensor"
`

const clausesRoles = `
[[role]]
name = "worker"
handler = "/usr/bin/true"
binds = ["e"]
requires = ["disk >= OK"]

[[role]]
name = "free"
handler = "/usr/bin/true"
binds = ["e"]

[[query]]
name = "work"
type = "command"
emits = ["e"]
[query.command]
argv = ["/usr/bin/true"]
format = "jsonl"
`

// A sensor fixture that reads 19.9 GiB free (TIGHT) and prints the band table of
// disk: higher is better, OK needs >= 25.
const tightSensor = `case "$1" in
--bands) echo '{"resource":"disk","unit":"GiB","bands":[{"level":"PLENTY","min":40,"max":null},{"level":"OK","min":25,"max":40},{"level":"TIGHT","min":12,"max":25},{"level":"EXHAUSTED","min":null,"max":12}]}' ;;
--raw) printf '{"schemaVersion":"1","resource":"disk","level":"TIGHT","reading":{"value":19.9,"unit":"GiB"},"observed_at":"%s","valid_for_s":180,"owner":"host-sensor"}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" ;;
*) exit 3 ;;
esac`

// doctor --clauses prints BLOCKED NOW with the shortfall from the sensor's own
// band table, the summary line, every resource's level and the below-OK
// statement, runs each sensor with --raw, and exits 0.
func TestDoctorClauses_BlockedNowWithShortfall(t *testing.T) {
	clausesFixture(t, tightSensor, clausesDiskResource, clausesRoles)
	var out strings.Builder
	if code := runDoctorClauses(&out); code != exitOK {
		t.Fatalf("exit = %d, want 0:\n%s", code, out.String())
	}
	for _, want := range []string{
		"worker  BLOCKED NOW  disk >= OK: disk is TIGHT (19.9 GiB; needs >= 25 GiB; 5.1 GiB short)",
		"free  ok",
		"1 of 2 registrations blocked",
		"disk  TIGHT  19.9 GiB  (sensor host-sensor)",
		"the bands start this machine below OK on disk TIGHT",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// A sensor that fails leaves its resources at "no reading", never changes the
// exit status, and a resource that reads worst when silent blocks its roles.
func TestDoctorClauses_SensorFailureIsNoReadingAndStillExitsZero(t *testing.T) {
	res := clausesDiskResource + `
[[resource]]
name = "net"
scale = ["UP", "DOWN"]
sensor = "host-sensor"
unknown = "worst"
`
	roles := strings.Replace(clausesRoles, `requires = ["disk >= OK"]`, `requires = ["disk >= OK", "net == UP"]`, 1)
	clausesFixture(t, `echo "boom" >&2; exit 1`, res, roles)
	var out strings.Builder
	if code := runDoctorClauses(&out); code != exitOK {
		t.Fatalf("exit = %d, want 0 even with a failing sensor:\n%s", code, out.String())
	}
	for _, want := range []string{
		"disk  no reading  (sensor has no answer)",
		"net  no reading  (sensor has no answer)",
		"worker  BLOCKED NOW  net == UP: net is DOWN (no reading; a silent resource reads DOWN)",
		"1 of 2 registrations blocked",
		"sensor host-sensor: --raw failed",
		"no resource with a reading starts below OK",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// With nothing blocked the summary line still prints, with n = 0.
func TestDoctorClauses_NothingBlockedStillPrintsTheSummary(t *testing.T) {
	plenty := strings.ReplaceAll(strings.ReplaceAll(tightSensor, `"level":"TIGHT"`, `"level":"PLENTY"`), "19.9", "55")
	clausesFixture(t, plenty, clausesDiskResource, clausesRoles)
	var out strings.Builder
	if code := runDoctorClauses(&out); code != exitOK {
		t.Fatalf("exit = %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "0 of 2 registrations blocked") || strings.Contains(out.String(), "BLOCKED NOW") {
		t.Errorf("want the 0 of 2 summary and no BLOCKED NOW:\n%s", out.String())
	}
}

// A malformed answer is ignored with a note (the resource reads "no reading").
func TestDoctorClauses_MalformedAnswerIgnored(t *testing.T) {
	clausesFixture(t, `case "$1" in --raw) echo '{"schemaVersion":"1","resource":"disk","level":"tight"}' ;; *) exit 3 ;; esac`, clausesDiskResource, clausesRoles)
	var out strings.Builder
	if code := runDoctorClauses(&out); code != exitOK {
		t.Fatalf("exit = %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "malformed answer ignored") || !strings.Contains(out.String(), "disk  no reading") {
		t.Errorf("want the malformed note and no reading:\n%s", out.String())
	}
}

// A config that does not load cannot be evaluated: FAIL lines, exit 1.
func TestDoctorClauses_ConfigErrorExitsOne(t *testing.T) {
	clausesFixture(t, tightSensor, clausesDiskResource, clausesRoles+"\n[[role]]\nname = \"broken\"\n")
	var out strings.Builder
	if code := runDoctorClauses(&out); code != exitGeneric || !strings.Contains(out.String(), "FAIL config:") {
		t.Fatalf("exit = %d, want 1 with a FAIL config line:\n%s", code, out.String())
	}
}

func TestShortfall(t *testing.T) {
	min25, max40, max8 := 25.0, 40.0, 8.0
	disk := bandTable{Unit: "GiB", Bands: []bandRow{
		{Level: "PLENTY", Min: &max40}, {Level: "OK", Min: &min25, Max: &max40}, {Level: "TIGHT"},
	}}
	if n, s, ok := shortfall(19.9, disk, []string{"PLENTY", "OK"}); !ok || n != ">= 25 GiB" || s != "5.1 GiB" {
		t.Errorf("higher-is-better = %q %q %v", n, s, ok)
	}
	load := bandTable{Unit: "load", Bands: []bandRow{{Level: "PLENTY", Max: &max8}, {Level: "OK", Min: &max8}}}
	if n, s, ok := shortfall(9.5, load, []string{"PLENTY"}); !ok || n != "< 8 load" || s != "1.5 load" {
		t.Errorf("lower-is-better = %q %q %v", n, s, ok)
	}
	if _, _, ok := shortfall(1, bandTable{}, []string{"UP"}); ok {
		t.Error("a sensor without bands has no shortfall")
	}
}
