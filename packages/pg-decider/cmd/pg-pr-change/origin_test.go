package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runWith runs the command with no pg-desk on PATH: the flag parsing and the
// own-origin guard are exercised through the real run(), and any tool call that
// the guard should have prevented would show as a failure line.
func runWith(t *testing.T, args ...string) (code int, record string, stderr string) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rec := filepath.Join(t.TempDir(), "runs.jsonl")
	var out, errb bytes.Buffer
	code = run(context.Background(), append([]string{"--run-record", rec, "--lock-dir", t.TempDir()}, args...), &out, &errb)
	b, _ := os.ReadFile(rec)
	return code, string(b), errb.String()
}

func TestOriginFlagOfTheDeciderSkipsTheDelivery(t *testing.T) {
	// The flag may precede or follow the positionals, with = or a space.
	for _, args := range [][]string{
		{"--origin", "decider:pr-decider", "apply", "acme/api#7@0123456789ab"},
		{"--origin=decider:pr-decider", "apply", "acme/api#7@0123456789ab", "<no value>"},
		{"apply", "acme/api#7@0123456789ab", "--origin=decider:pr-decider"},
	} {
		code, record, stderr := runWith(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d, want 0: %s", args, code, stderr)
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(record)), &rec); err != nil {
			t.Fatalf("%v: record %q: %v", args, record, err)
		}
		if rec["skipped"] != true || rec["skip_reason"] != "own-origin" || rec["origin"] != "decider:pr-decider" {
			t.Errorf("%v: record = %v, want skipped own-origin", args, rec)
		}
		// pg-desk is not on PATH, so the counter could not be bumped: noted, not fatal.
		if rec["skip_counter"] != "failed" {
			t.Errorf("%v: skip_counter = %v, want failed (no pg-desk)", args, rec["skip_counter"])
		}
	}
}

func TestOtherOriginsAreNotSkippedByTheCommand(t *testing.T) {
	for _, origin := range []string{"pg-desk", "", "<no value>", "null"} {
		code, record, _ := runWith(t, "--origin="+origin, "apply", "acme/api#7")
		// The run proceeds to the refresh, which cannot start without pg-desk.
		if code == 0 {
			t.Errorf("origin %q: exit 0, want a hydrate failure (the run was skipped)", origin)
		}
		if strings.Contains(record, `"skipped":true`) {
			t.Errorf("origin %q: record says skipped: %s", origin, record)
		}
	}
}
