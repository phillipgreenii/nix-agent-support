package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/hookdecisions/fixture"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
)

func exec(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(context.Background(), args, &o, &e)
	return code, o.String(), e.String()
}

func TestRunOutputConformsAndIsDeterministic(t *testing.T) {
	db := fixture.Build(t, fixture.Standard())
	args := []string{
		"--db", db, "--since", "2026-03-01T00:00:00Z", "--until", "2026-03-10T00:00:00Z",
		"--scope", "synthetic-scope", "--exclude-cwd-prefix", "/neutral/review",
	}
	c1, out1, e1 := exec(args...)
	if c1 != 0 {
		t.Fatalf("exit %d: %s", c1, e1)
	}
	if err := schema.Validate("evidence-rows", []byte(out1)); err != nil {
		t.Fatal(err)
	}
	if _, out2, _ := exec(args...); out2 != out1 {
		t.Fatal("two runs differ")
	}
}

func TestOffsetBoundsAreNormalisedToUTC(t *testing.T) {
	db := fixture.Build(t, fixture.Standard())
	// 2026-03-01T00:00:00Z == 2026-02-28T19:00:00-05:00
	code, out, e := exec("--db", db, "--since", "2026-02-28T19:00:00-05:00", "--until", "2026-03-09T19:00:00-05:00",
		"--scope", "s", "--query", "failed")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, e)
	}
	var rows []struct {
		Window struct{ From, To string }
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Window.From != "2026-03-01T00:00:00Z" || rows[0].Window.To != "2026-03-10T00:00:00Z" {
		t.Errorf("window = %+v", rows[0].Window)
	}
}

func TestExitCodes(t *testing.T) {
	db := fixture.Build(t, nil)
	good := []string{"--db", db, "--since", "2026-03-01T00:00:00Z", "--until", "2026-03-10T00:00:00Z", "--scope", "s"}
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"ok", good, 0},
		{"unknown flag", append([]string{"--bogus"}, good...), 2},
		{"missing window", []string{"--db", db, "--scope", "s"}, 2},
		{"bad time", []string{"--db", db, "--since", "yesterday", "--until", "2026-03-10T00:00:00Z", "--scope", "s"}, 2},
		{"unknown query", append([]string{"--query", "zzz"}, good...), 2},
		{"relative prefix", append([]string{"--cwd-prefix", "rel"}, good...), 2},
		{"stray arg", append([]string{"extra"}, good...), 2},
		{"unreadable db", append([]string{"--db", filepath.Join(t.TempDir(), "none.db")}, good[2:]...), 1},
	}
	for _, c := range cases {
		if got, _, _ := exec(c.args...); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
}
