package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/pb/internal/unstick"
)

func TestMarker_generate(t *testing.T) {
	env := testEnv(run.NewFakeRunner())
	out, _, err := execUnstick(env, "", "marker", "--outcome", "undeferred", "--reason", "blocker a-1 closed", "--recheck-when", "a-9 closes")
	if err != nil {
		t.Fatal(err)
	}
	want := "[unstick 2026-10-10T12:00:00Z] undeferred: blocker a-1 closed; recheck-when: a-9 closes\n"
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
	if _, err := unstick.ParseMarker(out); err != nil {
		t.Errorf("generated marker does not parse: %v", err)
	}
	// --now pins the stamp; non-UTC offsets are normalised to Z.
	out, _, err = execUnstick(env, "", "marker", "--outcome", "unchanged", "--reason", "still waiting", "--recheck-when", "2027-02-03", "--now", "2026-01-02T05:04:03+02:00")
	if err != nil || out != "[unstick 2026-01-02T03:04:03Z] unchanged: still waiting; recheck-when: 2027-02-03\n" {
		t.Errorf("--now: %q, %v", out, err)
	}
	out, _, err = execUnstick(env, "", "marker", "--outcome", "closed", "--reason", "stale", "--recheck-when", "on-change", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var j map[string]string
	if err := json.Unmarshal([]byte(out), &j); err != nil || !strings.HasPrefix(j["marker"], "[unstick 2026-10-10T12:00:00Z] closed: stale;") {
		t.Errorf("json = %q, %v", out, err)
	}
}

func TestMarker_generateRejectsBadInput(t *testing.T) {
	ok := []string{"--outcome", "unchanged", "--reason", "fine", "--recheck-when", "on-change"}
	with := func(flag, val string) []string {
		args := append([]string{"marker"}, ok...)
		for i := 1; i < len(args); i++ {
			if args[i] == flag {
				args[i+1] = val
			}
		}
		return args
	}
	cases := map[string][]string{
		"outcome uppercase":     with("--outcome", "Unchanged"),
		"outcome newline":       with("--outcome", "un\nchanged"),
		"outcome colon":         with("--outcome", "a:b"),
		"reason newline":        with("--reason", "line1\nline2"),
		"reason backtick":       with("--reason", "has `tick`"),
		"reason dollar":         with("--reason", "costs $5"),
		"reason quote":          with("--reason", `say "hi"`),
		"reason apostrophe":     with("--reason", "it's"),
		"reason recheck inject": with("--reason", "x; recheck-when: 2020-01-01"),
		"recheck bad":           with("--recheck-when", "tomorrow"),
		"recheck bad id":        with("--recheck-when", "not an id closes"),
		"bad --now":             append(append([]string{}, ok...), "--now", "x"),
		"missing outcome":       {"marker", "--reason", "r", "--recheck-when", "on-change"},
		"missing reason":        {"marker", "--outcome", "o", "--recheck-when", "on-change"},
		"missing recheck":       {"marker", "--outcome", "o", "--reason", "r"},
		"export without check":  append(append([]string{"marker"}, ok...), "--export", "/x"),
	}
	cases["bad --now"] = append([]string{"marker"}, cases["bad --now"]...)
	for name, args := range cases {
		out, _, err := execUnstick(testEnv(run.NewFakeRunner()), "", args...)
		if err == nil || exitCodeFor(err) != 1 {
			t.Errorf("%s: want exit 1, got %d (%v)", name, exitCodeFor(err), err)
		}
		if out != "" {
			t.Errorf("%s: wrote output %q on failure", name, out)
		}
	}
}

func TestMarker_check(t *testing.T) {
	env := testEnv(run.NewFakeRunner())
	good := "[unstick 2026-10-10T12:00:00Z] unchanged: ok; recheck-when: on-change\n\n  [unstick 2026-10-10T12:00:01Z] closed: stale; recheck-when: 2027-01-01  \n"
	out, _, err := execUnstick(env, good, "marker", "--check")
	if err != nil || !strings.Contains(out, "checked 2 line(s): 0 non-conforming") {
		t.Errorf("good input: %v\n%s", err, out)
	}

	bad := "[unstick 2026-10-10T12:00:00Z] unchanged: ok; recheck-when: on-change\n" +
		"[unstick 2026-10-10] unchanged: date only; recheck-when: on-change\n" +
		"[unstick 2026-10-10T12:00:00Z] unchanged: no recheck\n"
	out, _, err = execUnstick(env, bad, "marker", "--check")
	if err == nil || exitCodeFor(err) != 1 || !strings.Contains(err.Error(), "2 non-conforming") {
		t.Errorf("bad input: exit %d, err %v", exitCodeFor(err), err)
	}
	if !strings.Contains(out, "line 2:") || !strings.Contains(out, "line 3:") || strings.Contains(out, "line 1:") {
		t.Errorf("report must name exactly lines 2 and 3:\n%s", out)
	}

	out, _, err = execUnstick(env, bad, "marker", "--check", "--json")
	if err == nil {
		t.Error("--json must keep the non-zero exit")
	}
	var res markerCheckResult
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil || res.Checked != 3 || len(res.NonConforming) != 2 || res.NonConforming[0].Line != 2 {
		t.Errorf("json = %+v, %v", res, jerr)
	}

	if _, _, err := execUnstick(env, "", "marker", "--check"); err != nil {
		t.Errorf("empty stdin is conforming: %v", err)
	}
	if _, _, err := execUnstick(env, "", "marker", "--check", "--outcome", "x"); err == nil {
		t.Error("--check with --outcome must be rejected")
	}
}

func TestMarker_checkExportListsMalformedBeads(t *testing.T) {
	w := preparedWorkdir(t)
	export := filepath.Join(w, "export.jsonl")
	out, _, err := execUnstick(testEnv(run.NewFakeRunner()), "", "marker", "--check", "--export", export)
	if err == nil || exitCodeFor(err) != 1 {
		t.Fatalf("a malformed marker in the export must fail: %v", err)
	}
	if !strings.Contains(out, "1 bead(s) with a malformed marker") || !strings.Contains(out, "a-6") || strings.Contains(out, "  a-5\n") {
		t.Errorf("output:\n%s", out)
	}

	// Closed beads are ignored; a clean export passes.
	clean := filepath.Join(t.TempDir(), "clean.jsonl")
	rows := `{"id":"c-1","status":"closed","notes":"[unstick 2026-10-01] unchanged: x; recheck-when: on-change"}` + "\n" +
		`{"id":"c-2","status":"open","comments":[{"id":1,"text":"[unstick 2026-10-10T12:00:00Z] unchanged: ok; recheck-when: on-change"}]}` + "\n"
	if err := os.WriteFile(clean, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, err := execUnstick(testEnv(run.NewFakeRunner()), "", "marker", "--check", "--export", clean); err != nil {
		t.Errorf("clean export: %v\n%s", err, out)
	}

	// A malformed marker in a comment is found too.
	cm := filepath.Join(t.TempDir(), "cm.jsonl")
	if err := os.WriteFile(cm, []byte(`{"id":"c-3","status":"open","comments":[{"id":1,"text":"note\n[unstick 2026-10-10] unchanged: x; recheck-when: on-change"}]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, err := execUnstick(testEnv(run.NewFakeRunner()), "", "marker", "--check", "--export", cm); err == nil || !strings.Contains(out, "c-3") {
		t.Errorf("comment marker: %v\n%s", err, out)
	}
	if _, _, err := execUnstick(testEnv(run.NewFakeRunner()), "", "marker", "--check", "--export", filepath.Join(w, "missing.jsonl")); err == nil {
		t.Error("unreadable export must fail")
	}
}
