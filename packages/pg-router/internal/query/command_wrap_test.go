package query

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envProbe writes a /bin/sh producer that emits one JSONL record whose title is
// its own sorted environment.
func envProbe(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe.sh")
	script := `#!/bin/sh
printf '{"id":"probe","title":"%s"}\n' "$(/usr/bin/env | /usr/bin/sort | /usr/bin/tr '\n' ' ')"
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func runProbe(t *testing.T, q CommandQuery) string {
	t.Helper()
	evts, err := q.Run(context.Background(), Env{Cmd: OSCommander{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 1 {
		t.Fatalf("events = %d, want 1", len(evts))
	}
	return evts[0].Item.Title
}

func TestCommandQuery_strictBaseEnvKeepsOnlyTheBaseList(t *testing.T) {
	t.Setenv("PG_ROUTER_TEST_MODE", "1")
	t.Setenv("PG_ROUTER_LEAKY", "secret")
	t.Setenv("HOME", "/tmp/k06-home")
	q := CommandQuery{
		Argv:          []string{envProbe(t)},
		Format:        FormatJSONL,
		StrictBaseEnv: true,
	}
	title := runProbe(t, q)
	for _, want := range []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "PG_ROUTER_TEST_MODE=1", "HOME=/tmp/k06-home"} {
		if !strings.Contains(title, want) {
			t.Errorf("strict env lacks %q; env = %s", want, title)
		}
	}
	if strings.Contains(title, "PG_ROUTER_LEAKY") {
		t.Errorf("strict env leaked an inherited variable; env = %s", title)
	}
	allowed := map[string]bool{
		"PATH": true, "PG_ROUTER_TEST_MODE": true, "HOME": true, "USER": true, "LOGNAME": true,
		"TMPDIR": true, "LANG": true, "TERM": true, "COLORTERM": true, "PWD": true, "SHLVL": true, "_": true, "OLDPWD": true,
	}
	for _, kv := range strings.Fields(title) {
		name, _, _ := strings.Cut(kv, "=")
		if !allowed[name] {
			t.Errorf("variable %q outside the base list; env = %s", name, title)
		}
	}
}

// clearAmbientEnv removes every variable from the test process's environment
// (restored at cleanup), so a probe that dumps the inherited env sees only what
// the test sets. The nix build sandbox exports multi-line and quote-bearing
// variables (buildPhase, shellHook, ...) that a JSONL title cannot carry.
func clearAmbientEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		t.Setenv(k, "") // registers the restore
		_ = os.Unsetenv(k)
	}
}

func TestCommandQuery_nonStrictInheritsTheAmbientEnv(t *testing.T) {
	clearAmbientEnv(t)
	t.Setenv("PG_ROUTER_LEAKY", "secret")
	q := CommandQuery{Argv: []string{envProbe(t)}, Format: FormatJSONL}
	if title := runProbe(t, q); !strings.Contains(title, "PG_ROUTER_LEAKY=secret") {
		t.Fatalf("non-strict query lost the inherited env; env = %s", title)
	}
}

// A fake Commander (no RunEnv) receives the query's argv unchanged.
func TestCommandQuery_fakeCommanderSeesTheArgv(t *testing.T) {
	var got []string
	q := CommandQuery{Argv: []string{"/bin/x", "a"}, Format: FormatJSONL}
	_, err := q.Run(context.Background(), Env{Cmd: recordCmd{&got}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/bin/x", "a"}; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

type recordCmd struct{ argv *[]string }

func (r recordCmd) Run(_ context.Context, argv []string) ([]byte, error) {
	*r.argv = argv
	return nil, nil
}
