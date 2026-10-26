package servetest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePjira is the counting stand-in for the pjira binary. It is a POSIX sh
// script and every answer comes from files in $FAKE_PJIRA_DIR (required), so a
// test scripts the origin by writing files (see Shim) and the daemon, which
// reaches the script only through the configured absolute pjira_path and the
// configured pjira_env, sees a faked origin it cannot tell from the real one.
//
//	calls.log           appended to: one line per call, the argv tab separated
//	                    (no argument a test passes carries a tab or a newline)
//	argv0.log           appended to: the path the script was run as, one line per call
//	env.<pid>.log       written when record_env exists: the call's whole
//	                    environment, sorted, one file per call; written to
//	                    env.<pid>.tmp and renamed, so a reader never sees a
//	                    half-written one (pg2-tqgth3)
//	issue.<KEY>.json    the issue document `issue -- KEY` prints and a search
//	                    carries as one item (one line)
//	query.<q>.keys      the keys (one per line) a search of the JQL <q> answers,
//	                    <q> being the JQL with every byte outside [A-Za-z0-9]
//	                    written as an underscore
//	query.default.keys  the keys a search answers that no query.<q>.keys names (and
//	                    that carries no --limit)
//	identity.json       what a search with --limit that names no key answers (the
//	                    operator identity lookup)
//	truncated           exists: every search answers "truncated": true
//	auth_state          what `auth-status` prints (default OK); anything but OK
//	                    also exits 1, as pjira does for a bad credential
//	create.key          the key `create` answers (default EXA-100)
//	fail                a status: every call answers it (stderr "status N", exit 1)
//	fail.<verb>         the same for one verb only (issue, search, ...)
//	hold                a text (or *): every call whose "<verb> <arguments>" contains
//	                    it holds, after it logged itself, until the file release
//	                    exists (at most 60 s); each held call appends a line to
//	                    held.log
//
// A search is answered with {"items":[...],"truncated":...} built from the
// issue files of its keys: a key with no file is simply absent, as an issue the
// origin no longer shows.
const fakePjira = `#!/bin/sh
dir="${FAKE_PJIRA_DIR:?FAKE_PJIRA_DIR is required}"
verb="$1"

tab=$(printf '\t')
line=$verb
shift_args=1
for a in "$@"; do
  if [ "$shift_args" = 1 ]; then shift_args=0; continue; fi
  line="$line$tab$a"
done
printf '%s\n' "$line" >>"$dir/calls.log"
printf '%s\n' "$0" >>"$dir/argv0.log"

if [ -f "$dir/record_env" ]; then
  env | sort >"$dir/env.$$.tmp" && mv "$dir/env.$$.tmp" "$dir/env.$$.log"
fi

if [ -f "$dir/hold" ]; then
  want=$(cat "$dir/hold")
  hold=0
  [ "$want" = "*" ] && hold=1
  case "$verb $*" in *"$want"*) hold=1 ;; esac
  if [ "$hold" = 1 ]; then
    printf 'held\n' >>"$dir/held.log"
    n=0
    while [ ! -f "$dir/release" ] && [ "$n" -lt 1200 ]; do
      sleep 0.05
      n=$((n + 1))
    done
  fi
fi

status=''
if [ -f "$dir/fail.$verb" ]; then status=$(cat "$dir/fail.$verb"); elif [ -f "$dir/fail" ]; then status=$(cat "$dir/fail"); fi
if [ -n "$status" ]; then
  echo "pjira: request failed: status $status" >&2
  exit 1
fi

emit_file() { # <file>: its single line, without a trailing newline
  while IFS= read -r line || [ -n "$line" ]; do printf '%s' "$line"; done <"$1"
}

case "$verb" in
auth-status)
  state=OK
  [ -f "$dir/auth_state" ] && state=$(cat "$dir/auth_state")
  echo "$state"
  [ "$state" = OK ] || exit 1
  ;;
issue)
  key=$3
  if [ -f "$dir/issue.$key.json" ]; then
    emit_file "$dir/issue.$key.json"
    echo
  else
    echo "pjira: issue $key not found" >&2
    exit 1
  fi
  ;;
search)
  jql=''
  limit=''
  prev=''
  for a in "$@"; do
    [ "$prev" = --jql ] && jql=$a
    [ "$prev" = --limit ] && limit=$a
    prev=$a
  done
  keys=''
  identity=0
  case "$jql" in
  'key = "'*'"')
    k=${jql#'key = "'}
    keys=${k%'"'}
    ;;
  'key in ('*')')
    k=${jql#'key in ('}
    k=${k%')'}
    keys=$(printf '%s' "$k" | tr -d ' "' | tr ',' ' ')
    ;;
  *)
    q=$(printf '%s' "$jql" | tr -c 'A-Za-z0-9' '_')
    if [ -f "$dir/query.$q.keys" ]; then
      keys=$(tr '\n' ' ' <"$dir/query.$q.keys")
    elif [ -n "$limit" ]; then
      identity=1
    elif [ -f "$dir/query.default.keys" ]; then
      keys=$(tr '\n' ' ' <"$dir/query.default.keys")
    fi
    ;;
  esac
  if [ "$identity" = 1 ]; then
    emit_file "$dir/identity.json"
    echo
    exit 0
  fi
  printf '{"items":['
  sep=''
  for k in $keys; do
    [ -f "$dir/issue.$k.json" ] || continue
    printf '%s' "$sep"
    sep=','
    emit_file "$dir/issue.$k.json"
  done
  if [ -f "$dir/truncated" ]; then t=true; else t=false; fi
  printf '],"truncated":%s}\n' "$t"
  ;;
comment | transition)
  printf '{"key":"%s"}\n' "$3"
  ;;
create)
  key=EXA-100
  [ -f "$dir/create.key" ] && key=$(cat "$dir/create.key")
  printf '{"key":"%s","url":"https://jira.example.test/browse/%s"}\n' "$key" "$key"
  ;;
*)
  echo "fake pjira: unexpected arguments: $*" >&2
  exit 2
  ;;
esac
`

// Shim is the fake pjira of one daemon: the script above in its own directory,
// plus the issue, query and control files it reads and the call log it writes.
// The shim keeps the issues it was given, so SetComments can rewrite one.
type Shim struct {
	// Dir holds the script (as "pjira"), the fixtures and calls.log; Path is the
	// script the daemon's pjira_path names.
	Dir  string
	Path string

	t testing.TB

	mu     sync.Mutex
	issues map[string]Issue
	seq    int
}

func newShim(t testing.TB, dir string) *Shim {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Shim{Dir: dir, Path: filepath.Join(dir, "pjira"), t: t, issues: map[string]Issue{}}
	if err := os.WriteFile(s.Path, []byte(fakePjira), 0o700); err != nil {
		t.Fatal(err)
	}
	s.Set("identity.json", identityDocument())
	return s
}

// Set writes a control or fixture file atomically (a write to a temporary name
// and a rename), so the script never reads half of one.
func (s *Shim) Set(name, content string) {
	s.t.Helper()
	tmp := filepath.Join(s.Dir, ".tmp."+name)
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(s.Dir, name)); err != nil {
		s.t.Fatal(err)
	}
}

// Remove deletes a control or fixture file.
func (s *Shim) Remove(name string) {
	s.t.Helper()
	if err := os.Remove(filepath.Join(s.Dir, name)); err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
}

// SetIssue makes the origin show the issue (the answer of `issue -- KEY`, of a
// search by key and of every query that lists its key).
func (s *Shim) SetIssue(iss Issue) {
	s.t.Helper()
	s.mu.Lock()
	s.issues[iss.Key] = iss
	s.mu.Unlock()
	s.Set("issue."+iss.Key+".json", iss.document(s.t)+"\n")
}

// SetIssues makes the search of the JQL answer exactly issues, and each issue
// answerable on its own (see SetIssue).
func (s *Shim) SetIssues(jql string, issues ...Issue) {
	s.t.Helper()
	keys := make([]string, 0, len(issues))
	for _, iss := range issues {
		s.SetIssue(iss)
		keys = append(keys, iss.Key)
	}
	s.Set("query."+Sanitize(jql)+".keys", strings.Join(keys, "\n")+"\n")
}

// SetDefaultIssues makes every search no SetIssues call names answer exactly
// issues (the pass-through reads of a test whose JQL it cannot spell out).
func (s *Shim) SetDefaultIssues(issues ...Issue) {
	s.t.Helper()
	keys := make([]string, 0, len(issues))
	for _, iss := range issues {
		s.SetIssue(iss)
		keys = append(keys, iss.Key)
	}
	s.Set("query.default.keys", strings.Join(keys, "\n")+"\n")
}

// RemoveIssue makes the origin stop showing the issue: a search that lists its
// key no longer carries it, and `issue` answers not found.
func (s *Shim) RemoveIssue(key string) {
	s.t.Helper()
	s.mu.Lock()
	delete(s.issues, key)
	s.mu.Unlock()
	s.Remove("issue." + key + ".json")
}

// SetComments replaces the comments of an issue the shim shows.
func (s *Shim) SetComments(key string, comments ...Comment) {
	s.t.Helper()
	s.mu.Lock()
	iss, ok := s.issues[key]
	s.mu.Unlock()
	if !ok {
		s.t.Fatalf("servetest: SetComments of %s, which the shim does not show", key)
	}
	iss.Comments = comments
	s.SetIssue(iss)
}

// Fail makes every call answer the HTTP status (stderr "status N", exit 1); a
// status of 0 ends it.
func (s *Shim) Fail(status int) { s.failFile("fail", status) }

// FailVerb is Fail for the calls of one verb (issue, search, auth-status, ...).
func (s *Shim) FailVerb(verb string, status int) { s.failFile("fail."+verb, status) }

func (s *Shim) failFile(name string, status int) {
	s.t.Helper()
	if status == 0 {
		s.Remove(name)
		return
	}
	s.Set(name, fmt.Sprint(status))
}

// Truncated makes every search answer truncated (or not).
func (s *Shim) Truncated(on bool) {
	s.t.Helper()
	if on {
		s.Set("truncated", "1")
		return
	}
	s.Remove("truncated")
}

// AuthState sets what `auth-status` prints; anything but OK also exits 1.
func (s *Shim) AuthState(state string) { s.Set("auth_state", state) }

// CreateKey sets the key `create` answers.
func (s *Shim) CreateKey(key string) { s.Set("create.key", key) }

// RecordEnv makes the shim append its whole environment to env.log on every
// call, so a test can see exactly what the daemon gave the process.
func (s *Shim) RecordEnv() { s.Set("record_env", "1") }

// Hold makes every call whose "<verb> <arguments>" contains match ("*" for any
// call) wait, after it is logged, until Release; for example `search` holds
// every search and `key = "` only the detail group's. WaitHeld waits for the
// first of them to be holding.
func (s *Shim) Hold(match string) {
	s.t.Helper()
	s.Remove("release")
	s.Remove("held.log")
	s.Set("hold", match)
}

// Release lets the held calls go and holds no further call.
func (s *Shim) Release() {
	s.t.Helper()
	s.Remove("hold")
	s.Set("release", "1")
}

// WaitHeld waits until a held call is waiting.
func (s *Shim) WaitHeld(t testing.TB, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(filepath.Join(s.Dir, "held.log")); err == nil && len(raw) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("servetest: no pjira call was held within %s", max)
}

// Argv0 returns the path the shim was run as on each call, oldest first.
func (s *Shim) Argv0() []string {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "argv0.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// Env returns the recorded environments of the calls, one sorted list of
// NAME=value lines each (empty without RecordEnv); the order of the calls is not
// kept, since each call writes a file of its own.
func (s *Shim) Env() [][]string {
	s.t.Helper()
	files, err := filepath.Glob(filepath.Join(s.Dir, "env.*.log"))
	if err != nil {
		s.t.Fatal(err)
	}
	var out [][]string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			s.t.Fatal(err)
		}
		var env []string
		for _, line := range strings.Split(string(raw), "\n") {
			if line != "" {
				env = append(env, line)
			}
		}
		out = append(out, env)
	}
	return out
}

// Call is one recorded pjira invocation.
type Call struct {
	// Verb is the first argument (issue, search, auth-status, comment, ...) and
	// Args the arguments after it.
	Verb string
	Args []string
}

// Kind classifies the call: "issue", "auth_status", "write" (comment,
// transition or create), "search_key" (the detail group's search of one key),
// "search_batch" (the summary group's key in (...) search), "search_identity"
// (the operator identity lookup), "search_query" (any other search, so a
// configured query or a pass-through) or "other".
func (c Call) Kind() string {
	switch c.Verb {
	case "issue":
		return "issue"
	case "auth-status":
		return "auth_status"
	case "comment", "transition", "create":
		return "write"
	case "search":
		jql := c.JQL()
		switch {
		case strings.HasPrefix(jql, `key = "`):
			return "search_key"
		case strings.HasPrefix(jql, "key in ("):
			return "search_batch"
		case c.has("--limit"):
			return "search_identity"
		}
		return "search_query"
	}
	return "other"
}

func (c Call) has(flag string) bool {
	for _, a := range c.Args {
		if a == flag {
			return true
		}
	}
	return false
}

// JQL is the value of --jql, "" for a call without one.
func (c Call) JQL() string {
	for i, a := range c.Args {
		if a == "--jql" && i+1 < len(c.Args) {
			return c.Args[i+1]
		}
	}
	return ""
}

// Calls returns every recorded call, oldest first.
func (s *Shim) Calls() []Call {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	var out []Call
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		out = append(out, Call{Verb: fields[0], Args: fields[1:]})
	}
	return out
}

// CountKind counts the recorded calls of one kind.
func (s *Shim) CountKind(kind string) int {
	n := 0
	for _, c := range s.Calls() {
		if c.Kind() == kind {
			n++
		}
	}
	return n
}

// ClearCalls forgets the recorded calls, so a test can count the calls of one
// step on its own.
func (s *Shim) ClearCalls() {
	s.t.Helper()
	s.Remove("calls.log")
}

// WaitQuiet waits until the shim has recorded no new call for quiet (the daemon
// has caught up with whatever work it started) and returns how many calls it
// has recorded; it fails the test if that does not happen within max. With
// kinds only the calls of those kinds count.
func (s *Shim) WaitQuiet(t testing.TB, quiet, max time.Duration, kinds ...string) int {
	t.Helper()
	count := func() int {
		if len(kinds) == 0 {
			return len(s.Calls())
		}
		n := 0
		for _, k := range kinds {
			n += s.CountKind(k)
		}
		return n
	}
	deadline := time.Now().Add(max)
	last := count()
	since := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		n := count()
		if n != last {
			last, since = n, time.Now()
			continue
		}
		if time.Since(since) >= quiet {
			return n
		}
	}
	t.Fatalf("servetest: the daemon kept calling pjira for %s (%d calls)", max, last)
	return last
}

// Sanitize is the shim's file-name key of a JQL: every byte outside [A-Za-z0-9]
// becomes an underscore (the script does the same with tr).
func Sanitize(jql string) string {
	b := []byte(jql)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '_'
		}
	}
	return string(b)
}
