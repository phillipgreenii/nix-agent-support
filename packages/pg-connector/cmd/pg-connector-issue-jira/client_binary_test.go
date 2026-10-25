package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The client and status modes are tested as the real binary: built with go
// build (the budget seam linked in, so a daemon-down read does not wait out the
// production budget), run as a child with an otherwise empty environment and
// working directory "/", against an in-process daemon on a short socket (the
// real process shell, socket server and router), the way the umbrella execs it.

var (
	buildOnce sync.Once
	buildDir  string
	buildBin  string
	buildErr  error
)

// removeBuilds deletes the built binary; TestMain calls it after the run.
func removeBuilds() {
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
}

func realBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		goBin, err := exec.LookPath("go")
		if err != nil {
			buildErr = err
			return
		}
		buildDir, err = os.MkdirTemp("", "pcjbin")
		if err != nil {
			buildErr = err
			return
		}
		buildBin = filepath.Join(buildDir, "pg-connector-issue-jira")
		cmd := exec.Command(goBin, "build", "-o", buildBin, "-ldflags", "-X main.clientBudgetOverride=2s", ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = errors.New("go build: " + err.Error() + "\n" + string(out))
		}
	})
	if buildErr != nil {
		if _, err := exec.LookPath("go"); err != nil {
			t.Skipf("no go toolchain on PATH to build the real binary: %v", err)
		}
		t.Fatal(buildErr)
	}
	return buildBin
}

// clientRig is a config, a fake origin tool and the state they share.
type clientRig struct {
	cfg      string
	state    string
	socket   string
	marker   string
	fakeBin  string
	instance string
}

// newClientRig writes a config with a short socket path and a fake origin tool
// that records the ACTOR variable of its environment into a marker file.
func newClientRig(t *testing.T) clientRig {
	t.Helper()
	root := shortTempDir(t)
	r := clientRig{
		state:    filepath.Join(root, "state"),
		socket:   filepath.Join(root, "s"),
		marker:   filepath.Join(root, "marker"),
		fakeBin:  filepath.Join(root, "fake-pjira"),
		cfg:      filepath.Join(root, "config.json"),
		instance: "jira-test",
	}
	script := "#!/bin/sh\nprintf 'actor=%s args=%s\\n' \"$PJIRA_TEST_ACTOR\" \"$1\" >> '" + r.marker + "'\necho '{}'\n"
	if err := os.WriteFile(r.fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"state_dir": r.state, "socket_path": r.socket, "pjira_path": r.fakeBin, "instance": r.instance,
		"supervisor_label":     "com.example.jira-daemon",
		"write_notice_timeout": "500ms",
		"queries": map[string]any{
			"mine":  map[string]any{"expr": []string{"assignee = currentUser()"}, "watched": true},
			"other": "project = X",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

// runBinary runs the real binary. The environment is empty but for the actor
// the fake origin tool reports and the override naming the fake origin tool.
func (r clientRig) runBinary(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(realBinary(t), args...)
	cmd.Dir = "/"
	cmd.Env = []string{"PJIRA_TEST_ACTOR=alice-actor", "PG_CONNECTOR_ISSUE_JIRA_BINARY=" + r.fakeBin, "PG_CONNECTOR_ISSUE_JIRA_PROJECT=ABC"}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

func (r clientRig) clientArgs() []string {
	return []string{"--config", r.cfg, "--instance", r.instance}
}

func (r clientRig) markerLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(r.marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestRealClientCapabilitiesWithNoDaemon(t *testing.T) {
	r := newClientRig(t)
	code, out, errs := r.runBinary(t, `{"op":"capabilities"}`, r.clientArgs()...)
	if code != 0 {
		t.Fatalf("exit %d stdout=%q stderr=%q", code, out, errs)
	}
	var caps struct {
		Vocabulary map[string]any `json:"vocabulary"`
		Ops        []string       `json:"ops"`
	}
	if err := json.Unmarshal([]byte(out), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Vocabulary["owns_changes"] != true || caps.Vocabulary["cache_opt_out"] != true {
		t.Errorf("vocabulary %v", caps.Vocabulary)
	}
	queries, _ := caps.Vocabulary["queries"].(map[string]any)
	if q, _ := queries["mine"].(map[string]any); q["watched"] != true {
		t.Errorf("queries.mine.watched: %v", queries)
	}
	if q, _ := queries["other"].(map[string]any); q["watched"] != false {
		t.Errorf("queries.other.watched: %v", queries)
	}
}

// A comment with no daemon succeeds, the origin tool saw the caller's
// environment (the actor) and no marker names the daemon.
func TestRealClientCommentWithNoDaemonUsesTheCallersEnvironment(t *testing.T) {
	r := newClientRig(t)
	start := time.Now()
	code, out, errs := r.runBinary(t, `{"op":"comment","args":{"id":"ABC-7","body":"hello"}}`, r.clientArgs()...)
	if code != 0 {
		t.Fatalf("exit %d stdout=%q stderr=%q", code, out, errs)
	}
	if got := r.markerLines(t); len(got) != 1 || got[0] != "actor=alice-actor args=comment" {
		t.Errorf("the origin tool saw %v, want one comment call under the caller's actor", got)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

// With a daemon that answers, it sees write_begin before the origin call and
// invalidate after it.
func TestRealClientWriteNoticesBracketTheOriginCall(t *testing.T) {
	r := newClientRig(t)
	type seen struct {
		op       string
		args     string
		original bool // the origin tool's marker existed at the notice
	}
	var mu sync.Mutex
	var notices []seen
	rt := router.New(router.Options{})
	for _, op := range []string{"write_begin", "invalidate"} {
		rt.Register("issue", op, scriptout.OpHandler{SchemaVersion: 1, Handle: func(_ context.Context, args json.RawMessage) (any, error) {
			_, err := os.Stat(r.marker)
			mu.Lock()
			notices = append(notices, seen{op, string(args), err == nil})
			mu.Unlock()
			return nil, nil
		}})
	}
	pc := proc.Config{StateDir: filepath.Dir(r.socket), SocketPath: r.socket, DrainTimeout: time.Second}
	srv, err := proc.Start(pc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, socket.Handler(rt, socket.ServerConfig{Build: "d"}), nil)
	}()
	t.Cleanup(func() { cancel(); <-done })

	code, out, errs := r.runBinary(t, `{"op":"comment","args":{"id":"ABC-7","body":"hello"}}`, r.clientArgs()...)
	if code != 0 {
		t.Fatalf("exit %d stdout=%q stderr=%q", code, out, errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 2 || notices[0].op != "write_begin" || notices[1].op != "invalidate" {
		t.Fatalf("notices %+v", notices)
	}
	if notices[0].original || !notices[1].original {
		t.Errorf("write_begin must precede the origin call and invalidate follow it: %+v", notices)
	}
	for _, n := range notices {
		if n.args != `{"id":"ABC-7"}` {
			t.Errorf("%s args %s", n.op, n.args)
		}
	}
}

func TestRealClientReadWithNoDaemonIsUnavailableAndLogsIt(t *testing.T) {
	r := newClientRig(t)
	code, out, _ := r.runBinary(t, `{"op":"show","args":{"id":"ABC-7"}}`, r.clientArgs()...)
	if code != 4 {
		t.Fatalf("exit %d, want 4 (unavailable): %s", code, out)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Error == nil || resp.Error.Code != "unavailable" {
		t.Fatalf("answer %q (%v)", out, err)
	}
	for _, w := range []string{r.socket, "com.example.jira-daemon"} {
		if !strings.Contains(resp.Error.Message, w) {
			t.Errorf("message %q lacks %q", resp.Error.Message, w)
		}
	}
	if strings.Contains(out, "served_from") {
		t.Errorf("never from a cache: %s", out)
	}
	raw, err := os.ReadFile(filepath.Join(r.state, "client.log.jsonl"))
	if err != nil || !strings.Contains(string(raw), `"outcome":"daemon_unreachable"`) {
		t.Errorf("client log %q (%v)", raw, err)
	}
	if len(r.markerLines(t)) != 0 {
		t.Error("a read must never call the origin tool")
	}
}

func TestRealClientInstanceThatDiffersFromTheConfigsExitsFour(t *testing.T) {
	r := newClientRig(t)
	code, out, errs := r.runBinary(t, `{"op":"capabilities"}`, "--config", r.cfg, "--instance", "someone-else")
	if code != 4 || out != "" || !strings.Contains(errs, "someone-else") {
		t.Errorf("exit %d stdout=%q stderr=%q, want 4 naming the instance", code, out, errs)
	}
	code, _, errs = r.runBinary(t, "", "--config", r.cfg, "--instance")
	if code != 2 || !strings.Contains(errs, "usage:") {
		t.Errorf("a missing instance name: exit %d stderr=%q, want usage", code, errs)
	}
}

func TestRealStatusWithNoDaemonPrintsTheStaticReportAndExitsThree(t *testing.T) {
	r := newClientRig(t)
	code, out, errs := r.runBinary(t, "", "--config", r.cfg, "status")
	if code != 3 || !strings.Contains(out, "health: down") || !strings.Contains(out, r.socket) {
		t.Errorf("text: exit %d stdout=%q stderr=%q", code, out, errs)
	}
	code, out, errs = r.runBinary(t, "", "--config", r.cfg, "status", "--json")
	if code != 3 {
		t.Fatalf("json: exit %d stdout=%q stderr=%q", code, out, errs)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc["health"] != "down" || doc["source"] != "local" || doc["socket"] != r.socket {
		t.Errorf("json report %q (%v)", out, err)
	}
	if code, _, errs := r.runBinary(t, "", "--config", r.cfg, "status", "--bogus"); code != 2 || !strings.Contains(errs, "usage:") {
		t.Errorf("a bad status argument: exit %d stderr=%q", code, errs)
	}
}

func TestRealVersionAndHelpWorkInEveryForm(t *testing.T) {
	r := newClientRig(t)
	for _, args := range [][]string{
		{"--version"},
		{"--config", r.cfg, "--version"},
		{"--config", r.cfg, "serve", "--version"},
		{"--config", r.cfg, "--instance", r.instance, "--version"},
		{"--config", r.cfg, "status", "--version"},
	} {
		code, out, errs := r.runBinary(t, "", args...)
		if code != 0 || !strings.HasPrefix(out, "pg-connector-issue-jira ") {
			t.Errorf("%v: exit %d stdout=%q stderr=%q", args, code, out, errs)
		}
	}
	for _, args := range [][]string{{"--help"}, {"--config", r.cfg, "status", "--help"}} {
		code, out, _ := r.runBinary(t, "", args...)
		if code != 0 || !strings.Contains(out, "usage:") {
			t.Errorf("%v: exit %d stdout=%q", args, code, out)
		}
	}
}
