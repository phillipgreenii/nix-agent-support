package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// "pr refresh", "pr explain" and "ci explain": each sends exactly one op request
// through the optional-op targeted dispatch, prints the backend's answer (the
// wire envelope in JSON mode, a rendering in human mode), exits by the targeted
// scheme, and writes nothing to the umbrella's ledger or cache.

const (
	diagUnknownOp = `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unknown_op","message":"no such op"}}`
	diagNotFound  = `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"gone"}}`
	diagDown      = `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"daemon socket /run/example/daemon.sock is not reachable; start the daemon (restart hint: restart the daemon agent)"}}`

	diagRefreshResult = `{"queued":[{"id":"org/repo#7","groups":["summary","detail","files"]}]}`

	// diagExplainResult is a canned explain document: two groups (the second
	// with a last_error and a queue position), a pending settle row and two
	// last change rows.
	diagExplainResult = `{"instance":"pr","id":"org/repo#7","ref":"org/repo#7","node_id":"PR_node_7","keep_alive":true,` +
		`"last_access":"2026-01-02T03:04:05Z",` +
		`"groups":[` +
		`{"group":"summary","fetched_at":"2026-01-02T03:00:00Z","fresh_until":"2026-01-02T03:10:00Z","max_age_at":"2026-01-02T04:00:00Z","fresh":true,"expired_by_max_age":false,"fetched_for_head":"headsha1","fetched_for_base":"basesha1","status":"fresh"},` +
		`{"group":"files","fetched_at":"2026-01-01T22:00:00Z","fresh_until":"2026-01-01T22:30:00Z","max_age_at":"2026-01-02T01:00:00Z","fresh":false,"expired_by_max_age":true,"last_error":"upstream 502 from the origin","fetched_for_head":"oldhead","fetched_for_base":"oldbase","queue_position":3,"status":"hard_stale","reason":"older than its max age","stamps_match":false}` +
		`],` +
		`"pending_settle":{"awaited":[{"group":"files","since":"2026-01-02T03:01:00Z"},{"group":"commits","since":"2026-01-02T03:01:30Z"}],"detected_at":"2026-01-02T03:01:00Z","deadline":"2026-01-02T03:06:00Z","kinds":["head_changed"]},` +
		`"last_changes":[{"seq":42,"version":9,"kinds":["head_changed"],"origin":"poll","at":"2026-01-02T03:01:00Z"},{"seq":41,"version":8,"kinds":["label_changed","title_changed"],"origin":"interactive","at":"2026-01-01T20:00:00Z"}]}`
)

func diagOK(result string) string {
	return `{"protocolVersion":1,"schemaVersion":1,"result":` + result + `}`
}

// diagBackend is a fake backend that records every request it receives, one
// JSON line per call, and answers the ops named in answers (a full wire
// response line each); every other op answers unknown_op.
type diagBackend struct {
	dir  string
	name string
}

func newDiagBackend(t *testing.T, name string, answers map[string]string) *diagBackend {
	t.Helper()
	dir := t.TempDir()
	ops := make([]string, 0, len(answers))
	for op := range answers {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\nreq=$(cat)\nprintf '%s\\n' \"$req\" >> '" + filepath.Join(dir, "requests.log") + "'\n")
	sb.WriteString("case \"$req\" in\n")
	for _, op := range ops {
		sb.WriteString("*'\"op\":\"" + op + "\"'*) cat <<'DIAG_EOF'\n" + answers[op] + "\nDIAG_EOF\n;;\n")
	}
	sb.WriteString("*) cat <<'DIAG_EOF'\n" + diagUnknownOp + "\nDIAG_EOF\n;;\nesac\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("write fake backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &diagBackend{dir: dir, name: name}
}

// requests returns every recorded request as (op, args) in call order.
func (b *diagBackend) requests(t *testing.T) []struct{ Op, Args string } {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.dir, "requests.log"))
	if err != nil {
		return nil
	}
	var out []struct{ Op, Args string }
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var req struct {
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("recorded request %q is not JSON: %v", line, err)
		}
		out = append(out, struct{ Op, Args string }{req.Op, string(req.Args)})
	}
	return out
}

// writeDiagConfig registers backends, in order, under connector.pr and
// connector.ci, and returns the XDG state dir the umbrella would write its
// ledger and cache under.
func writeDiagConfig(t *testing.T, backends ...*diagBackend) string {
	t.Helper()
	dir := t.TempDir()
	cfgDir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("connector:\n")
	for _, typ := range []string{"pr", "ci"} {
		sb.WriteString("  " + typ + ":\n")
		for _, b := range backends {
			sb.WriteString("    - " + b.name + "\n")
		}
	}
	cfg := filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

// diagVerb is one verb under test: its command line, the op it sends and the
// args object it sends.
type diagVerb struct {
	name string
	cmd  []string
	op   string
	args string
	ans  string
}

var diagVerbs = []diagVerb{
	{name: "pr explain", cmd: []string{"pr", "explain", "org/repo#7"}, op: "explain", args: `{"id":"org/repo#7"}`, ans: diagExplainResult},
	{name: "ci explain", cmd: []string{"ci", "explain", "org/repo#7"}, op: "explain", args: `{"id":"org/repo#7"}`, ans: diagExplainResult},
	{name: "pr refresh <id>", cmd: []string{"pr", "refresh", "org/repo#7"}, op: "refresh", args: `{"id":"org/repo#7"}`, ans: diagRefreshResult},
	{name: "pr refresh --query", cmd: []string{"pr", "refresh", "--query", "mine"}, op: "refresh", args: `{"query":"mine"}`, ans: diagRefreshResult},
}

func TestPRDiag_EachVerbSendsExactlyOneOpRequest(t *testing.T) {
	for _, v := range diagVerbs {
		t.Run(v.name, func(t *testing.T) {
			b := newDiagBackend(t, "diag-one", map[string]string{v.op: diagOK(v.ans)})
			writeDiagConfig(t, b)
			stdout, _, code := executePr(t, v.cmd)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
			}
			got := b.requests(t)
			if len(got) != 1 || got[0].Op != v.op || got[0].Args != v.args {
				t.Fatalf("requests = %+v, want exactly one %s with args %s", got, v.op, v.args)
			}
		})
	}
}

func TestPRDiag_JSONOutputIsTheWireResultUnchanged(t *testing.T) {
	for _, v := range diagVerbs {
		t.Run(v.name, func(t *testing.T) {
			b := newDiagBackend(t, "diag-json", map[string]string{v.op: diagOK(v.ans)})
			writeDiagConfig(t, b)
			stdout, _, code := executePr(t, v.cmd)
			if code != 0 {
				t.Fatalf("exit code = %d; stdout=%s", code, stdout)
			}
			var resp scriptout.Response
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
				t.Fatalf("stdout is not a wire envelope: %v\n%s", err, stdout)
			}
			var want bytes.Buffer
			if err := json.Compact(&want, []byte(v.ans)); err != nil {
				t.Fatal(err)
			}
			if string(resp.Result) != want.String() {
				t.Fatalf("result =\n%s\nwant the backend's result unchanged:\n%s", resp.Result, want.String())
			}
		})
	}
}

func TestPRDiag_RefreshNeedsExactlyOneOfIDAndQuery(t *testing.T) {
	for name, cmd := range map[string][]string{
		"both":    {"pr", "refresh", "org/repo#7", "--query", "mine"},
		"neither": {"pr", "refresh"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newDiagBackend(t, "diag-usage", map[string]string{"refresh": diagOK(diagRefreshResult)})
			writeDiagConfig(t, b)
			stdout, _, code := executePr(t, cmd)
			if code != TargetedExitCode(scriptout.WrapError(scriptout.ErrInvalidArgument, "x")) {
				t.Fatalf("exit code = %d, want the targeted scheme's code for invalid_argument; stdout=%s", code, stdout)
			}
			var resp scriptout.Response
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil || resp.Error == nil || resp.Error.Code != "invalid_argument" {
				t.Fatalf("stdout = %s, want an invalid_argument error envelope (decode err %v)", stdout, err)
			}
			if got := b.requests(t); len(got) != 0 {
				t.Fatalf("backend was called %d time(s) on a usage error: %+v", len(got), got)
			}
		})
	}
}

func TestPRDiag_RefreshReturnsAtOnceWithTheQueuedListInHumanOutput(t *testing.T) {
	b := newDiagBackend(t, "diag-human-refresh", map[string]string{"refresh": diagOK(diagRefreshResult)})
	writeDiagConfig(t, b)
	stdout, _, code := executePr(t, []string{"pr", "refresh", "org/repo#7", "--output", "human"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	for _, want := range []string{"org/repo#7", "summary", "detail", "files"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human refresh output lacks %q:\n%s", want, stdout)
		}
	}
	if got := b.requests(t); len(got) != 1 {
		t.Fatalf("requests = %+v, want exactly one (no polling)", got)
	}
}

func TestPRDiag_HumanExplainShowsEveryFieldTheSpecNames(t *testing.T) {
	for _, cmd := range [][]string{
		{"pr", "explain", "org/repo#7", "--output", "human"},
		{"ci", "explain", "org/repo#7", "--output", "human"},
	} {
		t.Run(cmd[0], func(t *testing.T) {
			b := newDiagBackend(t, "diag-human-explain", map[string]string{"explain": diagOK(diagExplainResult)})
			writeDiagConfig(t, b)
			stdout, _, code := executePr(t, cmd)
			if code != 0 {
				t.Fatalf("exit code = %d; stdout=%s", code, stdout)
			}
			for _, want := range []string{
				// header
				"org/repo#7",
				// group summary, in order, with every spec-named field
				"summary", "2026-01-02T03:00:00Z", "2026-01-02T03:10:00Z", "2026-01-02T04:00:00Z", "headsha1", "basesha1",
				// group files: last_error, head and base, queue position, max-age expiry
				"files", "2026-01-01T22:00:00Z", "2026-01-01T22:30:00Z", "2026-01-02T01:00:00Z",
				"upstream 502 from the origin", "oldhead", "oldbase", "queue_position: 3", "expired by max age",
				// pending settle row: awaited groups with since, deadline
				"pending settle", "commits", "2026-01-02T03:01:30Z", "2026-01-02T03:06:00Z", "head_changed",
				// last change rows
				"seq 42", "seq 41", "label_changed, title_changed", "interactive",
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("human explain output lacks %q:\n%s", want, stdout)
				}
			}
			if strings.Index(stdout, "summary") > strings.Index(stdout, "- files") {
				t.Errorf("groups are not in the document's order:\n%s", stdout)
			}
		})
	}
}

func TestPRDiag_HumanExplainOmitsAbsentOptionalFields(t *testing.T) {
	const minimal = `{"instance":"pr","id":"org/repo#7","ref":"org/repo#7","node_id":"n","keep_alive":false,"groups":[{"group":"summary","fresh":true,"expired_by_max_age":false,"fetched_at":"2026-01-02T03:00:00Z"}]}`
	b := newDiagBackend(t, "diag-human-minimal", map[string]string{"explain": diagOK(minimal)})
	writeDiagConfig(t, b)
	stdout, _, code := executePr(t, []string{"pr", "explain", "org/repo#7", "--output", "human"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	for _, absent := range []string{"last_error", "queue_position", "max_age_at", "fresh_until", "pending settle", "last changes", "fetched_for_head", "0001-01-01"} {
		if strings.Contains(stdout, absent) {
			t.Errorf("human output prints the absent optional field %q:\n%s", absent, stdout)
		}
	}
}

func TestPRDiag_BackendFlagPins(t *testing.T) {
	for _, v := range diagVerbs {
		t.Run(v.name, func(t *testing.T) {
			first := newDiagBackend(t, "diag-pin-first", map[string]string{v.op: diagOK(v.ans)})
			second := newDiagBackend(t, "diag-pin-second", map[string]string{v.op: diagOK(v.ans)})
			writeDiagConfig(t, first, second)
			cmd := append(append([]string{}, v.cmd...), "--backend", "diag-pin-second")
			stdout, _, code := executePr(t, cmd)
			if code != 0 {
				t.Fatalf("exit code = %d; stdout=%s", code, stdout)
			}
			if got := first.requests(t); len(got) != 0 {
				t.Fatalf("pinned-over backend was called: %+v", got)
			}
			if got := second.requests(t); len(got) != 1 || got[0].Op != v.op || got[0].Args != v.args {
				t.Fatalf("pinned backend requests = %+v, want one %s %s", got, v.op, v.args)
			}
		})
	}
}

func TestPRDiag_BackendFlagNamingAnUnregisteredBackendFails(t *testing.T) {
	b := newDiagBackend(t, "diag-pin-unreg", map[string]string{"explain": diagOK(diagExplainResult)})
	writeDiagConfig(t, b)
	stdout, _, code := executePr(t, []string{"pr", "explain", "org/repo#7", "--backend", "nope"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	if got := b.requests(t); len(got) != 0 {
		t.Fatalf("a backend was called for an unregistered pin: %+v", got)
	}
}

func TestPRDiag_OldBackendAnsweringUnknownOpIsSkipped(t *testing.T) {
	for _, v := range diagVerbs {
		t.Run(v.name, func(t *testing.T) {
			old := newDiagBackend(t, "diag-old", map[string]string{})
			current := newDiagBackend(t, "diag-current", map[string]string{v.op: diagOK(v.ans)})
			writeDiagConfig(t, old, current)
			stdout, _, code := executePr(t, v.cmd)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (the old backend is skipped); stdout=%s", code, stdout)
			}
			if got := old.requests(t); len(got) != 1 || got[0].Op != v.op {
				t.Fatalf("old backend requests = %+v, want it asked once and skipped", got)
			}
			if got := current.requests(t); len(got) != 1 || got[0].Args != v.args {
				t.Fatalf("current backend requests = %+v, want one with args %s", got, v.args)
			}
		})
	}
}

func TestPRDiag_NoBackendImplementingTheOpIsTheCallsUnknownOpOutcome(t *testing.T) {
	for _, v := range diagVerbs {
		t.Run(v.name, func(t *testing.T) {
			oldA := newDiagBackend(t, "diag-old-a", map[string]string{})
			oldB := newDiagBackend(t, "diag-old-b", map[string]string{})
			writeDiagConfig(t, oldA, oldB)
			stdout, _, code := executePr(t, v.cmd)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
			}
			var resp scriptout.Response
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil || resp.Error == nil || resp.Error.Code != "unknown_op" {
				t.Fatalf("stdout = %s, want an unknown_op error envelope (decode err %v)", stdout, err)
			}
		})
	}
}

func TestPRDiag_UnavailableIsTheOrdinaryTargetedFailureWithItsMessage(t *testing.T) {
	for _, v := range diagVerbs {
		t.Run(v.name, func(t *testing.T) {
			b := newDiagBackend(t, "diag-down", map[string]string{v.op: diagDown})
			writeDiagConfig(t, b)
			stdout, _, code := executePr(t, v.cmd)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
			}
			var resp scriptout.Response
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil || resp.Error == nil {
				t.Fatalf("stdout = %s, want an error envelope (decode err %v)", stdout, err)
			}
			if resp.Error.Code != "unavailable" || !strings.Contains(resp.Error.Message, "daemon.sock") || !strings.Contains(resp.Error.Message, "restart hint") {
				t.Fatalf("error = %+v, want unavailable carrying the backend's socket and restart hint", resp.Error)
			}
		})
	}
}

func TestPRDiag_NotFoundExitsFour(t *testing.T) {
	b := newDiagBackend(t, "diag-nf", map[string]string{"explain": diagNotFound})
	writeDiagConfig(t, b)
	if stdout, _, code := executePr(t, []string{"pr", "explain", "org/repo#7"}); code != 4 {
		t.Fatalf("exit code = %d, want 4; stdout=%s", code, stdout)
	}
}

func TestPRDiag_WritesNothingToTheLedgerOrCache(t *testing.T) {
	for _, v := range diagVerbs {
		t.Run(v.name, func(t *testing.T) {
			b := newDiagBackend(t, "diag-nowrite", map[string]string{v.op: diagOK(v.ans)})
			state := writeDiagConfig(t, b)
			if stdout, _, code := executePr(t, v.cmd); code != 0 {
				t.Fatalf("exit code = %d; stdout=%s", code, stdout)
			}
			var files []string
			_ = filepath.Walk(state, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					files = append(files, p)
				}
				return nil
			})
			if len(files) != 0 {
				t.Fatalf("the verb wrote under the umbrella's state dir: %v", files)
			}
		})
	}
}

func TestPRDiag_HelpDocumentsEachVerb(t *testing.T) {
	for _, tc := range []struct {
		cmd  []string
		want []string
	}{
		{[]string{"pr", "explain", "--help"}, []string{"explain <id>", "--backend", "settle row"}},
		{[]string{"ci", "explain", "--help"}, []string{"explain <pr-id>", "--backend", "settle row"}},
		{[]string{"pr", "refresh", "--help"}, []string{"refresh [<id>]", "--query", "--backend", "return at once", "giving both or neither"}},
	} {
		t.Run(strings.Join(tc.cmd, " "), func(t *testing.T) {
			stdout, _, code := executePr(t, tc.cmd)
			if code != 0 {
				t.Fatalf("--help exit %d", code)
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout, want) {
					t.Errorf("--help does not mention %q:\n%s", want, stdout)
				}
			}
		})
	}
}

func TestPRDiag_ArgumentShapes(t *testing.T) {
	b := newDiagBackend(t, "diag-shapes", map[string]string{"explain": diagOK(diagExplainResult), "refresh": diagOK(diagRefreshResult)})
	writeDiagConfig(t, b)
	for _, cmd := range [][]string{
		{"pr", "explain"},
		{"pr", "explain", "a", "b"},
		{"ci", "explain"},
		{"ci", "explain", "a", "b"},
		{"pr", "refresh", "a", "b"},
	} {
		if _, _, code := executePr(t, cmd); code == 0 {
			t.Errorf("%v: exit 0, want a usage failure", cmd)
		}
	}
	if got := b.requests(t); len(got) != 0 {
		t.Fatalf("a malformed command line reached the backend: %+v", got)
	}
}
