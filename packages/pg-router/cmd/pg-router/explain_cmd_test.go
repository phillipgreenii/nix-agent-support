package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

var explainNow = time.Date(2026, 11, 11, 23, 0, 0, 0, time.UTC)

// pausedStatus is a status reply with one role blocked by disk and by a
// credential, one role not blocked, and one disabled role.
func pausedStatus(t *testing.T) statusReply {
	t.Helper()
	const body = `{
	  "pausedRoles": [
	    {"role": "worker", "failing": [
	      {"clause": "disk >= OK", "resource": "disk", "level": "TIGHT", "kind": "level", "source": "sensor", "owner": "host-sensor",
	       "since": "2026-11-11T22:10:00Z", "reading": {"value": 19.9, "unit": "GiB"}},
	      {"clause": "git_auth == AVAILABLE", "resource": "git_auth", "level": "UNAVAILABLE", "kind": "level", "source": "sensor",
	       "owner": "git-auth-sensor", "since": "2026-11-10T08:00:00Z"}]},
	    {"role": "silent", "failing": [
	      {"clause": "net == UP", "resource": "net", "level": "DOWN", "kind": "unknown"}]}
	  ],
	  "listeners": [
	    {"role": "worker", "enabled": true}, {"role": "silent", "enabled": true},
	    {"role": "free", "enabled": true}, {"role": "off", "enabled": false}, {"role": "skipped", "enabled": true, "excluded": true}]
	}`
	var st statusReply
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// explain prints the paused-role line of a blocked role: N counts the failing
// clauses, each clause names its level, reading, since time (HH:MMZ today,
// RFC 3339 otherwise) and sensor, and the line says running work is not affected.
func TestExplainRole_PausedRoleLine(t *testing.T) {
	var out, errb strings.Builder
	if code := explainRole(&out, &errb, "worker", pausedStatus(t), explainNow); code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	want := "worker is paused by 2 requirements: disk >= OK now TIGHT (19.9 GiB), since 22:10Z, host-sensor; " +
		"git_auth == AVAILABLE now UNAVAILABLE (no numeric reading), since 2026-11-10T08:00:00Z, git-auth-sensor; " +
		"running work is not affected; see 'pg-router explain worker'\n"
	if out.String() != want {
		t.Fatalf("explain =\n%q\nwant\n%q", out.String(), want)
	}
}

func TestExplainRole_NoReadingClause(t *testing.T) {
	var out, errb strings.Builder
	explainRole(&out, &errb, "silent", pausedStatus(t), explainNow)
	if !strings.Contains(out.String(), "silent is paused by 1 requirement: net == UP now DOWN (no reading)") {
		t.Fatalf("explain = %q", out.String())
	}
}

func TestExplainRole_NotPausedDisabledExcludedAndUnknown(t *testing.T) {
	st := pausedStatus(t)
	for role, want := range map[string]string{
		"free":    "free is not paused by levels\n",
		"off":     "off is not paused by levels (the role is disabled)\n",
		"skipped": "skipped is not paused by levels (the role is excluded from this run)\n",
	} {
		var out, errb strings.Builder
		if code := explainRole(&out, &errb, role, st, explainNow); code != exitOK || out.String() != want {
			t.Errorf("%s: code=%d out=%q, want %q", role, code, out.String(), want)
		}
	}
	var out, errb strings.Builder
	if code := explainRole(&out, &errb, "nope", st, explainNow); code != conformance.ExitError || errb.String() != "unknown role nope\n" || out.Len() != 0 {
		t.Errorf("unknown role: code=%d out=%q err=%q, want exit 1 and 'unknown role nope'", code, out.String(), errb.String())
	}
}

// An override and a stale value say so in the clause.
func TestFailingClause_OverrideAndStaleWording(t *testing.T) {
	op := failingClauseView{Clause: "disk >= OK", Resource: "disk", Level: "EXHAUSTED", Kind: "level", Source: "operator", Owner: "operator"}
	if got := op.explainText(explainNow); !strings.Contains(got, ", OVERRIDE by operator") {
		t.Errorf("override = %q", got)
	}
	st := failingClauseView{Clause: "disk >= OK", Resource: "disk", Level: "TIGHT", Kind: "stale", Source: "grace_fallback", Owner: "host-sensor"}
	if got := st.explainText(explainNow); !strings.Contains(got, ", host-sensor, stale") {
		t.Errorf("stale = %q", got)
	}
}

func TestFormatSince(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-11T22:10:00Z":      "22:10Z",
		"2026-11-11T22:10:00+02:00": "20:10Z",
		"2026-11-10T22:10:00Z":      "2026-11-10T22:10:00Z",
		"not a time":                "not a time",
	} {
		if got := formatSince(in, explainNow); got != want {
			t.Errorf("formatSince(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunExplain_Usage(t *testing.T) {
	if code := runExplain(nil); code != exitUsage {
		t.Errorf("explain with no role = %d, want usage", code)
	}
	if code := runExplain([]string{"a", "b"}); code != exitUsage {
		t.Errorf("explain with two roles = %d, want usage", code)
	}
	if r := route([]string{"pg-router", "explain", "worker"}); r.kind != routeExplain || len(r.rest) != 1 {
		t.Errorf("route(explain) = %+v", r)
	}
}

// resume names the roles that stay paused by levels, with their resources.
func TestPausedByLevelsText(t *testing.T) {
	if got := pausedByLevelsText(nil); got != "" {
		t.Errorf("none paused = %q, want empty", got)
	}
	got := pausedByLevelsText(pausedStatus(t).PausedRoles)
	want := "; still paused by levels: worker (disk, git_auth), silent (net) (see 'pg-router explain <role>')"
	if got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// The status text carries the levels block (with "no sensor configured" and "no
// reading"), the override text, and the paused roles block with its count; with
// no paused role the block says (none).
func TestRenderStatusText_LevelsAndPausedRolesBlocks(t *testing.T) {
	var st statusReply
	body := `{
	  "levels": [
	    {"resource": "disk", "level": "TIGHT", "source": "sensor", "owner": "host-sensor", "age_s": 41, "reading": {"value": 19.9, "unit": "GiB"}},
	    {"resource": "beads", "level": "UP", "source": "operator", "owner": "operator", "age_s": 5, "override": {"until_cleared": true}},
	    {"resource": "claude_5h"},
	    {"resource": "git_auth", "no_sensor": true}
	  ]}`
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	renderStatusText(&out, "/s", st)
	for _, want := range []string{
		"LEVELS:", "  claude_5h: no reading (sensor has no answer)", "  git_auth: no sensor configured",
		"OVERRIDE by operator, until cleared", "PAUSED ROLES (0):\n  (none)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}

	st.PausedRoles = pausedStatus(t).PausedRoles
	out.Reset()
	renderStatusText(&out, "/s", st)
	want := "PAUSED ROLES (2):\n  worker  disk >= OK (disk TIGHT, 19.9 GiB since "
	if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "silent  net == UP (no reading, reads DOWN)") {
		t.Errorf("status paused roles block wrong:\n%s", out.String())
	}
}

// The router resource with no level in force is running: status says so rather
// than "no reading".
func TestRenderLevels_RouterWithNoPauseIsRunning(t *testing.T) {
	var out strings.Builder
	renderLevels(&out, []levelView{{Resource: "router"}, {Resource: "disk"}}, explainNow)
	if !strings.Contains(out.String(), "  router: RUNNING (no pause in force)\n") || !strings.Contains(out.String(), "  disk: no reading (sensor has no answer)\n") {
		t.Fatalf("levels = %q", out.String())
	}
}
