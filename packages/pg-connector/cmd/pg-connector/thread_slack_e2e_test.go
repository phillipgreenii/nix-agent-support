package main

// thread_slack_e2e_test.go: the hermetic end-to-end proof for the thread
// capability's claude -p backend (bead pg2-apllc). It go-builds the REAL
// pg-connector umbrella and the REAL pg-connector-thread-slack backend,
// execs the umbrella as a separate OS process (which execs the backend in
// turn, over the real scriptout protocol, exactly as in production) with an
// ad-hoc backend config block of the shape a deployment supplies, and stands
// a fake `claude` executable where the backend looks for the real one. The
// fake answers canned reply envelopes and records the argv, working
// directory and prompt it received, so the test sees what a real claude
// would have been asked.
//
// Hermetic by construction: no network, no Slack, no Anthropic API and no
// real claude. The fake is reached through the backend's own binary
// override variable, never through PATH discovery, and the child environment
// is rebuilt from an allowlist. Every value in the config block is a generic
// placeholder; the real working directory, model and MCP declaration come
// from the consuming flake's runtime config, and this repo is public.
//
// Not behind a build tag: it is part of every ordinary `go test ./...` run
// of this module, like cmd/pg-connector-thread-slack's conformance test.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	slackE2EBackend = "pg-connector-thread-slack"
	// Hang guards, not performance assertions (a cold go build under load
	// takes minutes; same convention as the contract suite).
	slackE2EBuildDeadline = 8 * time.Minute
	slackE2ERunDeadline   = 2 * time.Minute

	// Generic placeholders for the deployment-supplied claude options.
	slackE2EModel     = "test-model"
	slackE2EMCPConfig = `{"mcpServers":{"slack":{"type":"http","url":"https://mcp.example.invalid/mcp"}}}`
	slackE2EReadTools = "mcp__slack__slack_search_public,mcp__slack__slack_search_channels," +
		"mcp__slack__slack_read_thread,mcp__slack__slack_read_channel,mcp__slack__slack_read_user_profile"

	// Canned replies, as the model's final JSON text. The list reply carries
	// an item with a bare ts (which the backend must repair to a canonical
	// <CHANNEL>_<ts> id from the reply's own channel, bead pg2-pl2jk), an item
	// already in canonical form, and an item with no id, which it must drop.
	slackE2EListReply = `{"items":[` +
		`{"id":"1700000000.000100","channel":"C01TESTAAAA","permalink":"https://slack.example.invalid/p1","started_by":"U0TEST1","participants":["U0TEST1","U0TEST2"],"last_reply_at":"1700000100.000200","reply_count":2,"text":"first canned thread","mentions_me":true},` +
		`{"id":"C01TESTBBBB_1700000500.000300","channel":"C01TESTBBBB","permalink":"https://slack.example.invalid/p2","reply_count":0,"text":"second canned thread","mentions_me":false},` +
		`{"channel":"C01TESTCCCC","permalink":"https://slack.example.invalid/p3","text":"no id, dropped"}]}`
	slackE2EShowReply = `{"found":true,"id":"C01TESTAAAA_1700000000.000100","channel":"C01TESTAAAA","permalink":"https://slack.example.invalid/p1","reply_count":2,"text":"first canned thread","mentions_me":true}`
)

// slackE2EEnvelope wraps a model reply in claude -p's --output-format json
// result envelope.
func slackE2EEnvelope(t *testing.T, result string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "result", "is_error": false, "result": result})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return string(b)
}

// buildSlackE2EBinaries go-builds the umbrella and the backend into one fresh
// directory and returns it. The build runs from the module root with no
// inherited GIT_* variable (a commit-time hook exports some).
func buildSlackE2EBinaries(t *testing.T) string {
	t.Helper()
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), slackE2EBuildDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", dir,
		"./cmd/pg-connector", "./cmd/"+slackE2EBackend)
	cmd.Dir = moduleRoot
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build umbrella + backend: %v\n%s", err, out)
	}
	return dir
}

// slackE2EFakeClaude writes the fake claude into dir and returns its path. On
// each call it records, under dir/rec/, the argv (one argument per line), the
// working directory and the prompt (stdin) it received, in files suffixed
// with its own pid, then answers the canned list or show envelope according
// to what the prompt asks for.
func slackE2EFakeClaude(t *testing.T, dir, listEnvelope, showEnvelope string) string {
	t.Helper()
	rec := filepath.Join(dir, "rec")
	if err := os.MkdirAll(rec, 0o755); err != nil {
		t.Fatalf("mkdir rec: %v", err)
	}
	for name, body := range map[string]string{"list.json": listEnvelope, "show.json": showEnvelope} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	script := "#!/bin/sh\n" +
		"cat > '" + rec + "/prompt.'$$\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + rec + "/argv.'$$\n" +
		"pwd -P > '" + rec + "/cwd.'$$\n" +
		"if grep -q 'look up the Slack thread' '" + rec + "/prompt.'$$; then cat '" + dir + "/show.json'; else cat '" + dir + "/list.json'; fi\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

// slackE2ECall is one recorded fake-claude invocation.
type slackE2ECall struct {
	argv   []string
	cwd    string
	prompt string
}

// slackE2ECalls reads every invocation the fake claude has recorded so far.
func slackE2ECalls(t *testing.T, dir string) []slackE2ECall {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "rec", "argv.*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var calls []slackE2ECall
	for _, argvPath := range matches {
		suffix := strings.TrimPrefix(filepath.Base(argvPath), "argv.")
		read := func(kind string) string {
			b, err := os.ReadFile(filepath.Join(dir, "rec", kind+"."+suffix))
			if err != nil {
				t.Fatalf("read %s record: %v", kind, err)
			}
			return string(b)
		}
		calls = append(calls, slackE2ECall{
			argv:   strings.Split(strings.TrimSuffix(read("argv"), "\n"), "\n"),
			cwd:    strings.TrimSpace(read("cwd")),
			prompt: read("prompt"),
		})
	}
	return calls
}

// slackE2EFlagValue returns the argument following flag in argv, failing the
// test unless the flag appears exactly once.
func slackE2EFlagValue(t *testing.T, argv []string, flag string) string {
	t.Helper()
	got, n := "", 0
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			got = argv[i+1]
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d %s flags in %q, want exactly one", n, flag, argv)
	}
	return got
}

// slackE2ERun execs the built umbrella with args under env and returns its
// stdout and exit code; stderr is attached to a failure message only.
func slackE2ERun(t *testing.T, binDir string, env []string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), slackE2ERunDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "pg-connector"), args...)
	cmd.Env = env
	cmd.Dir = t.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("exec pg-connector %v: %v\nstderr=%s", args, err, stderr.String())
		}
		code = exitErr.ExitCode()
	}
	if code != 0 {
		t.Logf("pg-connector %v exited %d\nstderr=%s", args, code, stderr.String())
	}
	return stdout.String(), code
}

// TestThreadSlackE2E_ConfiguredClaudeThroughRealBinaries is bead pg2-apllc's
// hermetic end-to-end proof: with a config block of the deployed shape, the
// real umbrella and backend deliver the fake claude's canned threads as Thread
// v1 over list, changes and show, the claude child received the configured
// working directory, model, MCP declaration, turn limit and read-only
// allow-list, a relative after:-Nd became an absolute date, and the ledger
// holds the expected entries.
func TestThreadSlackE2E_ConfiguredClaudeThroughRealBinaries(t *testing.T) {
	binDir := buildSlackE2EBinaries(t)

	work := t.TempDir()
	claudeCwd := filepath.Join(work, "claude-cwd")
	if err := os.Mkdir(claudeCwd, 0o755); err != nil {
		t.Fatalf("mkdir claude cwd: %v", err)
	}
	wantCwd, err := filepath.EvalSymlinks(claudeCwd)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	fakeDir := filepath.Join(work, "fake")
	if err := os.Mkdir(fakeDir, 0o755); err != nil {
		t.Fatalf("mkdir fake dir: %v", err)
	}
	fakeClaude := slackE2EFakeClaude(t, fakeDir,
		slackE2EEnvelope(t, slackE2EListReply), slackE2EEnvelope(t, slackE2EShowReply))

	mcp, _ := json.Marshal(slackE2EMCPConfig)
	cwdJSON, _ := json.Marshal(claudeCwd)
	// The registry config the umbrella reads. JSON is YAML, so the block is
	// written as the JSON a deployment's rendered config would hold.
	registry := `{"connector":{"thread":["` + slackE2EBackend + `"]},` +
		`"backends":{"` + slackE2EBackend + `":{` +
		`"queries":{"involving-me":["is:thread after:-2d"]},` +
		`"claude":{"cwd":` + string(cwdJSON) + `,"model":"` + slackE2EModel + `","mcp_config":` + string(mcp) + `,"max_turns":16},` +
		`"timeouts":{"list":"60s","show":"60s"}}}}`
	cfgPath := filepath.Join(work, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(registry), 0o644); err != nil {
		t.Fatalf("write registry config: %v", err)
	}
	stateDir := filepath.Join(work, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	sysPath := os.Getenv("PATH")
	env := []string{
		"PATH=" + binDir + string(os.PathListSeparator) + sysPath,
		"HOME=" + filepath.Join(work, "home"),
		"PG_PR_CONFIG=" + cfgPath,
		"XDG_STATE_HOME=" + stateDir,
		// The backend's own overrides: the fake claude, and its event log
		// kept out of the real state directory.
		"PG_CONNECTOR_THREAD_SLACK_BINARY=" + fakeClaude,
		"PG_CONNECTOR_THREAD_SLACK_EVENTS_FILE=" + filepath.Join(work, "events.jsonl"),
	}

	// ---- thread list --------------------------------------------------
	dayBefore := time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02")
	stdout, code := slackE2ERun(t, binDir, env, "thread", "list", "--query", "involving-me")
	dayAfter := time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02")
	if code != 0 {
		t.Fatalf("thread list: exit %d, want 0; stdout=%s", code, stdout)
	}
	var list threadListOutcome
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("decode thread list: %v (stdout=%s)", err, stdout)
	}
	if len(list.Sources) != 1 || list.Sources[0].Status != SourceSucceeded || list.Sources[0].Count != 2 {
		t.Fatalf("sources = %+v, want one succeeded source with count 2", list.Sources)
	}
	wantIDs := []string{"C01TESTAAAA_1700000000.000100", "C01TESTBBBB_1700000500.000300"}
	if len(list.PresentIDs) != 2 || list.PresentIDs[0] != wantIDs[0] || list.PresentIDs[1] != wantIDs[1] {
		t.Fatalf("present_ids = %v, want %v (the id-less item dropped, the bare ts repaired)", list.PresentIDs, wantIDs)
	}
	first := list.Entities[0]
	if first.ID != wantIDs[0] || first.Channel != "C01TESTAAAA" || first.ReplyCount != 2 || !first.MentionsMe ||
		first.Permalink != "https://slack.example.invalid/p1" || first.Text != "first canned thread" || first.AsOf == "" {
		t.Fatalf("entities[0] = %+v, want the first canned thread parsed into Thread v1", first)
	}

	calls := slackE2ECalls(t, fakeDir)
	if len(calls) != 1 {
		t.Fatalf("fake claude ran %d times for one list, want 1", len(calls))
	}
	c := calls[0]
	if c.cwd != wantCwd {
		t.Fatalf("claude cwd = %q, want the configured %q", c.cwd, wantCwd)
	}
	for flag, want := range map[string]string{
		"--model":           slackE2EModel,
		"--mcp-config":      slackE2EMCPConfig,
		"--max-turns":       "16",
		"--allowed-tools":   slackE2EReadTools,
		"--setting-sources": "user",
		"--output-format":   "json",
	} {
		if got := slackE2EFlagValue(t, c.argv, flag); got != want {
			t.Fatalf("claude %s = %q, want %q (argv %q)", flag, got, want, c.argv)
		}
	}
	for _, flag := range []string{"-p", "--strict-mcp-config", "--disable-slash-commands", "--json-schema"} {
		found := false
		for _, a := range c.argv {
			if a == flag {
				found = true
			}
			if a == "--bare" {
				t.Fatalf("claude argv carries --bare: %q", c.argv)
			}
		}
		if !found {
			t.Fatalf("claude argv lacks %s: %q", flag, c.argv)
		}
	}
	if !strings.Contains(c.prompt, "after:"+dayBefore) && !strings.Contains(c.prompt, "after:"+dayAfter) {
		t.Fatalf("prompt carries neither after:%s nor after:%s:\n%s", dayBefore, dayAfter, c.prompt)
	}
	if strings.Contains(c.prompt, "after:-2d") {
		t.Fatalf("prompt still carries the relative after:-2d:\n%s", c.prompt)
	}

	// ---- thread changes + ledger ----------------------------------------
	stdout, code = slackE2ERun(t, binDir, env, "thread", "changes", "--query", "involving-me", "--consumer", "e2e")
	if code != 0 {
		t.Fatalf("thread changes: exit %d, want 0; stdout=%s", code, stdout)
	}
	w := decodeChangesWire(t, stdout)
	if kinds := changeKindsFor(w.Changes); len(w.Changes) != 2 || kinds["added"] != 2 {
		t.Fatalf("first changes = %+v, want two added threads", w.Changes)
	}
	ledgerFile := filepath.Join(stateDir, "pg-connector", "ledger", "thread__"+slackE2EBackend+"__involving-me.json")
	raw, err := os.ReadFile(ledgerFile)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var ledger Ledger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatalf("decode ledger: %v (raw=%s)", err, raw)
	}
	if len(ledger.Entries) != 2 {
		t.Fatalf("ledger has %d entries, want 2: %s", len(ledger.Entries), raw)
	}
	for _, id := range wantIDs {
		if e, ok := ledger.Entries[id]; !ok || e.RemovedAtVersion != nil {
			t.Fatalf("ledger entry %q missing or tombstoned: %s", id, raw)
		}
	}
	// A repeat poll against the same canned list reports nothing new.
	stdout, code = slackE2ERun(t, binDir, env, "thread", "changes", "--query", "involving-me", "--consumer", "e2e")
	if code != 0 {
		t.Fatalf("second thread changes: exit %d, want 0; stdout=%s", code, stdout)
	}
	if w := decodeChangesWire(t, stdout); len(w.Changes) != 0 {
		t.Fatalf("second changes = %+v, want none for an unchanged list", w.Changes)
	}

	// ---- thread show ------------------------------------------------------
	before := len(slackE2ECalls(t, fakeDir))
	stdout, code = slackE2ERun(t, binDir, env, "thread", "show", wantIDs[0])
	if code != 0 {
		t.Fatalf("thread show: exit %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode show response: %v (stdout=%s)", err, stdout)
	}
	var th schema.Thread
	if err := scriptout.Decode(resp.Result, &th); err != nil {
		t.Fatalf("decode Thread: %v", err)
	}
	if th.ID != wantIDs[0] || th.Channel != "C01TESTAAAA" || th.ReplyCount != 2 || !th.MentionsMe {
		t.Fatalf("shown thread = %+v", th)
	}
	// The show child was configured exactly like the list child: the config
	// block reaches BOTH ops.
	all := slackE2ECalls(t, fakeDir)
	if len(all) != before+1 {
		t.Fatalf("fake claude ran %d times, want %d after one show", len(all), before+1)
	}
	var showCall *slackE2ECall
	for i := range all {
		if strings.Contains(all[i].prompt, "look up the Slack thread") {
			showCall = &all[i]
		}
	}
	if showCall == nil {
		t.Fatal("no recorded claude call carried the show prompt")
	}
	if showCall.cwd != wantCwd || slackE2EFlagValue(t, showCall.argv, "--model") != slackE2EModel ||
		slackE2EFlagValue(t, showCall.argv, "--max-turns") != "16" ||
		slackE2EFlagValue(t, showCall.argv, "--mcp-config") != slackE2EMCPConfig {
		t.Fatalf("show call = %+v, want the same configured cwd, model, turns and MCP declaration", *showCall)
	}
}
