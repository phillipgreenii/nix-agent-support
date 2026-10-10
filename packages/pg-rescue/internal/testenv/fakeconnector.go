package testenv

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeConnectorScript stands in for pg-connector. Every call is recorded as
// call.N.args (NUL-separated argv) and call.N.env; its reply comes from
// resp.VERB / err.VERB / exit.VERB, where VERB is the word after "issue". A
// "block.VERB" file makes the call write its parent pid to "started" and
// hang, so a test can kill the caller while a child is in flight.
//
// A blocked call is ready to be killed the moment "started" exists: it forks
// its sleeping grandchild FIRST, then publishes "started.child" and finally
// "started", each by atomic rename. A test that waits for "started" therefore
// never races the fork (a SIGKILL of the process group landing mid-fork can
// leave the grandchild alive holding the output pipes, so the caller never
// returns) and never reads a half-written file.
const fakeConnectorScript = `#!/bin/bash
d="$FAKE_CONNECTOR_DIR"
n=1
while [ -e "$d/call.$n.args" ]; do n=$((n + 1)); done
printf '%s\0' "$@" > "$d/call.$n.args"
{
  echo "dir=$PG_CONNECTOR_ISSUE_BEADS_DIR"
  echo "beads_dir=$BEADS_DIR"
  echo "actor=$PG_CONNECTOR_ISSUE_BEADS_ACTOR"
} > "$d/call.$n.env"
verb="$2"
if [ -e "$d/block.$verb" ]; then
  sleep 60 &
  sleeper=$!
  echo "$$" > "$d/started.child.tmp" && mv "$d/started.child.tmp" "$d/started.child"
  echo "$PPID" > "$d/started.tmp" && mv "$d/started.tmp" "$d/started"
  wait "$sleeper"
fi
[ -e "$d/resp.$verb" ] && cat "$d/resp.$verb"
[ -e "$d/err.$verb" ] && cat "$d/err.$verb" >&2
if [ -e "$d/exit.$verb" ]; then exit "$(cat "$d/exit.$verb")"; fi
exit 0
`

// FakeConnector is a fake pg-connector on PATH. Its default replies are an
// empty list, a create that returns id "pg2-new1", and empty comment/update
// results.
type FakeConnector struct {
	// Dir holds the recorded calls and the canned replies.
	Dir string
	t   testing.TB
}

// Call is one recorded pg-connector invocation.
type Call struct {
	Args []string
	// TrackerDir and Actor are the PG_CONNECTOR_ISSUE_BEADS_* values the call
	// saw; BeadsDir is the BEADS_DIR it saw. Each is "" when the variable was
	// unset (or empty).
	TrackerDir string
	BeadsDir   string
	Actor      string
}

// NewFakeConnector writes the fake into a temp dir.
func NewFakeConnector(t testing.TB) *FakeConnector {
	t.Helper()
	root := t.TempDir()
	f := &FakeConnector{Dir: filepath.Join(root, "state"), t: t}
	bin := filepath.Join(root, "bin")
	for _, d := range []string{f.Dir, bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "pg-connector"), []byte(fakeConnectorScript), 0o700); err != nil {
		t.Fatal(err)
	}
	f.Respond("list", `{"entities":[]}`)
	f.Respond("create", `{"result":{"id":"pg2-new1"}}`)
	f.Respond("comment", `{"result":{}}`)
	f.Respond("update", `{"result":{}}`)
	return f
}

// Env returns the entries that put the fake on PATH and point it at its state.
func (f *FakeConnector) Env() []string {
	return []string{
		"PATH=" + filepath.Join(filepath.Dir(f.Dir), "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_CONNECTOR_DIR=" + f.Dir,
	}
}

// Install applies Env to this test's process environment.
func (f *FakeConnector) Install() {
	f.t.Helper()
	for _, kv := range f.Env() {
		k, v, _ := strings.Cut(kv, "=")
		f.t.Setenv(k, v)
	}
}

func (f *FakeConnector) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// Respond sets the stdout of VERB (list, create, comment, update).
func (f *FakeConnector) Respond(verb, stdout string) { f.write("resp."+verb, stdout) }

// Fail makes VERB print stderr and exit with code.
func (f *FakeConnector) Fail(verb string, code int, stderr string) {
	f.write("exit."+verb, strconv.Itoa(code))
	f.write("err."+verb, stderr)
}

// Block makes VERB hang after recording its parent pid in the "started" file.
func (f *FakeConnector) Block(verb string) { f.write("block."+verb, "") }

// Calls returns the recorded invocations in order.
func (f *FakeConnector) Calls() []Call {
	f.t.Helper()
	var out []Call
	for n := 1; ; n++ {
		raw, err := os.ReadFile(filepath.Join(f.Dir, "call."+strconv.Itoa(n)+".args"))
		if err != nil {
			return out
		}
		c := Call{Args: strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")}
		env, _ := os.ReadFile(filepath.Join(f.Dir, "call."+strconv.Itoa(n)+".env"))
		for _, line := range strings.Split(string(env), "\n") {
			switch k, v, _ := strings.Cut(line, "="); k {
			case "dir":
				c.TrackerDir = v
			case "beads_dir":
				c.BeadsDir = v
			case "actor":
				c.Actor = v
			}
		}
		out = append(out, c)
	}
}

// Verb is the word after "issue".
func (c Call) Verb() string {
	if len(c.Args) > 1 {
		return c.Args[1]
	}
	return ""
}

// Values returns every value given for --name, in either "--name V" or
// "--name=V" form.
func (c Call) Values(name string) []string {
	var out []string
	for i, a := range c.Args {
		switch {
		case a == "--"+name && i+1 < len(c.Args):
			out = append(out, c.Args[i+1])
		case strings.HasPrefix(a, "--"+name+"="):
			out = append(out, strings.TrimPrefix(a, "--"+name+"="))
		}
	}
	return out
}

// Value is the single value of --name, or "" when absent.
func (c Call) Value(name string) string {
	if v := c.Values(name); len(v) > 0 {
		return v[0]
	}
	return ""
}
