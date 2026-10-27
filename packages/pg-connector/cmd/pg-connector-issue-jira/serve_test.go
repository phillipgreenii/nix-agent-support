package main

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
)

// reexecEnv marks a child process that is this test binary standing in for the
// real command: TestMain runs the real main() in it, so a test sees what a
// supervisor or a shell would see (a real exit status, a real process).
const reexecEnv = "PG_CONNECTOR_ISSUE_JIRA_TEST_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		main() // exits the process
		return
	}
	code := m.Run()
	removeBuilds()
	servetest.RemoveBuilds()
	os.Exit(code)
}

// syncBuffer is a goroutine-safe stderr.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSplitConfigFlag(t *testing.T) {
	cases := []struct {
		args  []string
		path  string
		rest  []string
		given bool
		err   bool
	}{
		{args: nil},
		{args: []string{"serve"}, rest: []string{"serve"}},
		{args: []string{"--config", "/p", "serve"}, path: "/p", rest: []string{"serve"}, given: true},
		{args: []string{"serve", "--config=/p"}, path: "/p", rest: []string{"serve"}, given: true},
		{args: []string{"-config", "/p"}, path: "/p", given: true},
		{args: []string{"--config"}, given: true, err: true},
	}
	for _, c := range cases {
		path, rest, given, err := splitConfigFlag(c.args)
		if path != c.path || given != c.given || (err != nil) != c.err || strings.Join(rest, " ") != strings.Join(c.rest, " ") {
			t.Errorf("splitConfigFlag(%v) = %q %v %v %v, want %q %v %v err=%v", c.args, path, rest, given, err, c.path, c.rest, c.given, c.err)
		}
	}
}

// ---- the assembled daemon as a real child, with a fake origin -----------------
