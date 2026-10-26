package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// These tests drive the adapter's real dispatch path (serveDispatch with the
// production dispatchlib.BuildDeps wiring: a real ccpool CLIRunner and bd
// CLIRunner shelling out to a ccpool or bd on PATH). PATH holds only stubs plus
// /usr/bin and /bin for their shell, so the module's own unit-test isolation
// invariant holds: no real ccpool pool, tmux server or tracker is touched.

// A stub ccpool that lists an empty pool and fails every other subcommand: the
// prior-session read finds nothing in flight and `ccpool capacity` fails, the
// admission gate's fail-closed branch.
const ccpoolCapacityBroken = `if [ "$1" = list ]; then echo '[]'; exit 0; fi
echo "stub ccpool: $1 unavailable" >&2
exit 1
`

func TestDispatch_poolCapacityUnknownExitsBusyWithReason(t *testing.T) {
	t.Setenv("PATH", stubBin(t, map[string]string{"ccpool": ccpoolCapacityBroken})+":/usr/bin:/bin")
	name, cfg := workerRole("", "")
	code, got := dispatchWith(t, name, cfg, "", defaultEvent)
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (%d); stdout=%q", code, conformance.ExitBusy, got)
	}
	if reason := decodeReply(t, got)["reason"]; reason != wire.BusyReasonCapacityUnknown {
		t.Fatalf("reason = %v, want %q", reason, wire.BusyReasonCapacityUnknown)
	}
}

// With no ccpool on PATH at all the prior-session read itself fails: the pool
// counts as holding an attempt in flight, but with its own reason
// pool-unreadable (bead pg2-s3pjt), NOT the alert-excluded session-in-flight.
func TestDispatch_unreadablePoolExitsBusyPoolUnreadable(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	name, cfg := workerRole("", "")
	code, got := dispatchWith(t, name, cfg, "", defaultEvent)
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (%d); stdout=%q", code, conformance.ExitBusy, got)
	}
	reason := decodeReply(t, got)["reason"]
	if reason != "pool-unreadable" || wire.BusyReasonPoolUnreadable != "pool-unreadable" {
		t.Fatalf("reason = %v (constant %q), want pool-unreadable", reason, wire.BusyReasonPoolUnreadable)
	}
	if reason == wire.BusyReasonSessionInFlight {
		t.Fatalf("an unreadable pool must not hide behind the alert-excluded %q", wire.BusyReasonSessionInFlight)
	}
}

// ccpoolNewFails is a stub ccpool whose `new` fails before ready. Every call is
// recorded, one argument per line, a "--" line between calls.
func ccpoolNewFails(log string) string {
	return `{ printf '%s\n' "$@"; echo --; } >> '` + log + `'
case "$1" in
  list) echo '[]'; exit 0;;
  capacity) echo '{"max_sessions":4,"free":4,"counted":0,"live":0,"preserved":0}'; exit 0;;
  new) echo "did not reach ready (state=starting) Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF" >&2; exit 1;;
  close) exit 0;;
esac
echo "stub ccpool: $1 unavailable" >&2
exit 1
`
}

// A failing `ccpool new` declines with launch-failed / fault: the reply carries
// a redacted detail, the abandoned session is purge-closed, the bead is not
// touched (no bd is on PATH, so a mutation would only show as an error), and one
// dispatch_decline record is written carrying the role's sessionEnv.
func TestDispatch_launchFailedDeclinesFaultAndRecords(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("PATH", stubBin(t, map[string]string{"ccpool": ccpoolNewFails(log)})+":/usr/bin:/bin")
	cfgPath, stateDir := launchConfig(t, "")
	name, cfg := workerRole(`,"sessionEnv":{"PG_WRAP_SPEC":"/etc/pg-wrap/worker.toml","K15":"v"}`, "")

	code, got := dispatchWith(t, name, cfg, cfgPath, defaultEvent)
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy; stdout=%q", code, got)
	}
	if err := schemas.ValidateBytes("handler.dispatch-reply", []byte(got)); err != nil {
		t.Fatalf("body %q does not match the schema: %v", got, err)
	}
	reply := decodeReply(t, got)
	if reply["reason"] != "launch-failed" || reply["kind"] != "fault" {
		t.Fatalf("reply = %v, want launch-failed/fault", reply)
	}
	detail, _ := reply["detail"].(string)
	if !strings.Contains(detail, "did not reach ready") || strings.Contains(detail, "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("detail = %q, want the redacted ccpool new error", detail)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "\nclose\n") && !strings.HasPrefix(string(calls), "close\n") {
		t.Errorf("the abandoned session must be purge-closed; ccpool calls:\n%s", calls)
	}

	recs := eventRecords(t, stateDir, "dispatch_decline")
	if len(recs) != 1 {
		t.Fatalf("want one dispatch_decline, got %d: %v", len(recs), recs)
	}
	rec := recs[0]
	for k, want := range map[string]any{
		"role": "worker", "item_id": "zr-w", "reason": "launch-failed", "decline_kind": "fault",
		"failure_signature": "launch-failed", "level": "warn",
	} {
		if rec[k] != want {
			t.Errorf("%s = %v, want %v", k, rec[k], want)
		}
	}
	if !reflect.DeepEqual(rec["session_env"], map[string]any{"PG_WRAP_SPEC": "/etc/pg-wrap/worker.toml", "K15": "v"}) {
		t.Errorf("session_env = %v, want the role's entries, values included", rec["session_env"])
	}
	if ev, _ := rec["evidence"].(string); ev != detail {
		t.Errorf("evidence %q differs from the reply detail %q", ev, detail)
	}
}

// An at-capacity decline is kind busy with no evidence and no signature.
func TestDispatch_atCapacityWritesDispatchDecline(t *testing.T) {
	t.Setenv("PATH", stubBin(t, map[string]string{"ccpool": `case "$1" in
  list) echo '[]'; exit 0;;
  capacity) echo '{"max_sessions":4,"free":0,"counted":4,"live":4,"preserved":0}'; exit 0;;
esac
exit 1
`})+":/usr/bin:/bin")
	cfgPath, stateDir := launchConfig(t, "")
	name, cfg := workerRole(`,"sessionEnv":{"K15":"v"}`, "")
	code, got := dispatchWith(t, name, cfg, cfgPath, defaultEvent)
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy; stdout=%q", code, got)
	}
	recs := eventRecords(t, stateDir, "dispatch_decline")
	if len(recs) != 1 {
		t.Fatalf("want one dispatch_decline, got %d: %v", len(recs), recs)
	}
	for k, want := range map[string]any{
		"role": "worker", "item_id": "zr-w", "reason": "at-capacity", "decline_kind": "busy",
		"failure_signature": "", "evidence": "", "level": "info",
	} {
		if recs[0][k] != want {
			t.Errorf("%s = %v, want %v", k, recs[0][k], want)
		}
	}
}

// sessionEnv reaches `ccpool new --env` unchanged, one --env per entry, the
// value verbatim (spaces and an `=` included), alongside the markers the adapter
// sets itself.
func TestDispatch_sessionEnvReachesCcpoolNewUnchanged(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("PATH", stubBin(t, map[string]string{"ccpool": ccpoolNewFails(log)})+":/usr/bin:/bin")
	name, cfg := workerRole(`,"sessionEnv":{"PG_WRAP_SPEC":"/etc/pg-wrap/worker.toml","K15":"a b=c","EMPTY":""}`, "")
	if code, got := dispatchWith(t, name, cfg, "", defaultEvent); code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (the stub's new fails); stdout=%q", code, got)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var newCall []string
	for _, call := range strings.Split(string(calls), "--\n") {
		if args := strings.Split(strings.TrimSuffix(call, "\n"), "\n"); args[0] == "new" {
			newCall = args
		}
	}
	if newCall == nil {
		t.Fatalf("no `ccpool new` call recorded:\n%s", calls)
	}
	var envArgs []string
	for i, a := range newCall {
		if a == "--env" && i+1 < len(newCall) {
			envArgs = append(envArgs, newCall[i+1])
		}
	}
	for _, want := range []string{"PG_WRAP_SPEC=/etc/pg-wrap/worker.toml", "K15=a b=c", "EMPTY="} {
		found := false
		for _, e := range envArgs {
			found = found || e == want
		}
		if !found {
			t.Errorf("--env %q missing from ccpool new; env args: %q", want, envArgs)
		}
	}
	if !strings.Contains(strings.Join(envArgs, "\n"), "BEADS_ACTOR=test-actor") {
		t.Errorf("the adapter's own BEADS_ACTOR marker is missing; env args: %q", envArgs)
	}
}

// A sessionEnv key that collides with an adapter-set variable or the PG_ROUTER_
// prefix, or is empty, or contains =, fails the config load: an error reply, exit
// 1, and not one ccpool call.
func TestDispatch_badSessionEnvKeyFailsLoadWithoutAnyCall(t *testing.T) {
	for _, key := range []string{"BEADS_ACTOR", "BEADS_DIR", "WORKSPACE_ROOT", "PG_ROUTER_ROLE", "", "A=B"} {
		t.Run(key, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "calls.log")
			t.Setenv("PATH", stubBin(t, map[string]string{"ccpool": ccpoolNewFails(log)})+":/usr/bin:/bin")
			env, _ := json.Marshal(map[string]string{key: "v"})
			name, cfg := workerRole(`,"sessionEnv":`+string(env), "")
			code, got := dispatchWith(t, name, cfg, "", defaultEvent)
			if code != conformance.ExitError {
				t.Fatalf("exit = %d, want ExitError (%d); stdout=%q", code, conformance.ExitError, got)
			}
			if msg, _ := decodeReply(t, got)["error"].(string); !strings.Contains(msg, "sessionEnv") {
				t.Errorf("error = %q, want it to name sessionEnv", msg)
			}
			if _, err := os.Stat(log); err == nil {
				t.Errorf("a config that fails to load must make no ccpool call")
			}
		})
	}
}

// The decode is strict on every call: an unknown key in the config, at the top
// level or inside the ccpool table, fails the dispatch before any call.
func TestDispatch_unknownConfigKeyFailsStrictDecode(t *testing.T) {
	for name, tc := range map[string]struct{ top, cc, want string }{
		"top level":     {`,"surviveShutdown":true`, "", `role.config: unknown key "surviveShutdown"`},
		"ccpool table":  {"", `,"actr":"x"`, `role.config.ccpool: unknown key "actr"`},
		"command block": {`,"command":{"argv":["x"]}`, "", `unknown key "command"`},
		"type":          {`,"type":"ccpool"`, "", `unknown key "type"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PATH", "/usr/bin")
			rname, cfg := workerRole(tc.top, tc.cc)
			code, got := dispatchWith(t, rname, cfg, "", defaultEvent)
			if code != conformance.ExitError {
				t.Fatalf("exit = %d, want ExitError; stdout=%q", code, got)
			}
			if msg, _ := decodeReply(t, got)["error"].(string); !strings.Contains(msg, tc.want) {
				t.Errorf("error = %q, want it to contain %q", msg, tc.want)
			}
		})
	}
}

// A request without a usable role object is a usage error (exit 2) with the
// error reply the schema allows; a missing config means {} and so fails on the
// required ccpool table rather than panicking.
func TestDispatch_requestWithoutUsableRole(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	code, got := dispatchWith(t, "", nil, "", defaultEvent) // no role member
	if code != conformance.ExitUsage {
		t.Fatalf("no role: exit = %d, want ExitUsage (%d); stdout=%q", code, conformance.ExitUsage, got)
	}
	if _, ok := decodeReply(t, got)["error"]; !ok {
		t.Errorf("no role: reply %q carries no error", got)
	}
	code, got = dispatchWith(t, "worker", nil, "", defaultEvent) // role without config
	if code != conformance.ExitError || !strings.Contains(got, "role.config.ccpool: required") {
		t.Fatalf("missing config: exit = %d stdout=%q, want ExitError naming the required ccpool table", code, got)
	}
}

// The launch config's handler-wide bounds are checked at dispatch, where both
// the role and the config are loaded.
func TestDispatch_roleDurationBoundedByLaunchConfig(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	cfgPath, _ := launchConfig(t, "")
	name, cfg := workerRole("", `,"needsInputStall":"1s"`)
	code, got := dispatchWith(t, name, cfg, cfgPath, defaultEvent)
	if code != conformance.ExitError || !strings.Contains(got, "needsInputStall") {
		t.Fatalf("exit = %d stdout=%q, want ExitError naming needsInputStall", code, got)
	}
}

// A cancelled handler context (shutdown) does not decline: the launch error
// returns as a plain failure, never as a launch-failed decline (core checks busy
// before shutdown, so a decline here would be counted as a fault).
func TestDispatch_cancelledContextDoesNotDecline(t *testing.T) {
	root := t.TempDir()
	started := filepath.Join(root, "new-started")
	t.Setenv("PATH", stubBin(t, map[string]string{"ccpool": `case "$1" in
  list) echo '[]'; exit 0;;
  capacity) echo '{"max_sessions":4,"free":4,"counted":0,"live":0,"preserved":0}'; exit 0;;
  new) : > '` + started + `'; while :; do :; done;;
  close) exit 0;;
esac
exit 1
`})+":/usr/bin:/bin")
	cfgPath, stateDir := launchConfig(t, "")
	name, cfg := workerRole("", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 600; i++ {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		cancel()
	}()
	code, got := dispatchCtx(ctx, t, name, cfg, cfgPath, defaultEvent)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want ExitError (%d): a cancelled launch is a plain failure; stdout=%q", code, conformance.ExitError, got)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("ccpool new never started: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(stateDir, "events.jsonl")); err == nil {
		if recs := eventRecords(t, stateDir, "dispatch_decline"); len(recs) != 0 {
			t.Errorf("a cancelled dispatch must not write a decline record: %v", recs)
		}
	}
}

// adoptingStubs builds stub ccpool and bd binaries with a pool holding one live,
// working session for the dispatched bead and role that records the dispatch's
// own event id (the one session a dispatch absorbs, INV-CCH-30). The stub bead
// reads in_progress until the absorbing dispatch refreshes the session's lease
// (ccpool meta set), then closed, so the wait ends at once. launchedAt is the
// session's pgrouter.launched_at ("" records none, as a session an older build
// launched). It returns the launch config path.
func adoptingStubs(t *testing.T, launchedAt string) string {
	t.Helper()
	root := t.TempDir()
	marker := filepath.Join(root, "absorbed")
	launchedMeta := ""
	if launchedAt != "" {
		launchedMeta = fmt.Sprintf(`,"pgrouter.launched_at":%q`, launchedAt)
	}
	session := fmt.Sprintf(`[{"external_id":"att-old","name":"pg-router-worker-zr-w","state":"working","live":true,"cwd":%q,"meta":{"pgrouter.event_id":"e-1"%s}}]`,
		filepath.Join(root, "wt"), launchedMeta)
	bin := stubBin(t, map[string]string{
		"ccpool": fmt.Sprintf("case \"$1\" in\nlist) echo '%s' ;;\nmeta) : > %q ;;\nesac\nexit 0\n", session, marker),
		"bd":     fmt.Sprintf("for a in \"$@\"; do\n  if [ \"$a\" = show ]; then\n    if [ -e %q ]; then s=closed; else s=in_progress; fi\n    echo \"[{\\\"id\\\":\\\"zr-w\\\",\\\"status\\\":\\\"$s\\\"}]\"\n    exit 0\n  fi\ndone\necho '[]'\nexit 0\n", marker),
	})
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("CCPOOL_POOL", "")
	cfgPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`{"PollInterval":%d}`, 20*time.Millisecond)), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func checkReplyAgainstSchema(t *testing.T, reply string) map[string]any {
	t.Helper()
	v := decodeReply(t, reply)
	if err := conformance.Check("handler.dispatch-reply", v); err != nil {
		t.Fatalf("reply %s failed the handler.dispatch-reply schema: %v", reply, err)
	}
	return v
}

// A dispatch that absorbs a session launched at T (the session's name, event id
// and launch time are the ones the OLD binary stamps) replies with startedAt = T:
// in-flight sessions started by the old handler are adopted across the cutover
// (ADR 0085).
func TestDispatch_adoptedSessionRepliesLaunchTimeAsStartedAt(t *testing.T) {
	const launched = "2026-10-12T23:03:46Z"
	cfgPath := adoptingStubs(t, launched)
	name, cfg := workerRole("", "")
	code, got := dispatchWith(t, name, cfg, cfgPath, defaultEvent)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout=%q", code, conformance.ExitOK, got)
	}
	v := checkReplyAgainstSchema(t, got)
	if _, ok := v["outcome"]; !ok {
		t.Fatalf("want an inline outcome reply; reply=%s", got)
	}
	if v["startedAt"] != launched {
		t.Fatalf("startedAt = %v, want the adopted session's launched_at %q; reply=%s", v["startedAt"], launched, got)
	}
}

// An adopted session that recorded no launch time replies with no startedAt.
func TestDispatch_adoptedSessionWithoutLaunchTimeRepliesNoStartedAt(t *testing.T) {
	cfgPath := adoptingStubs(t, "")
	name, cfg := workerRole("", "")
	code, got := dispatchWith(t, name, cfg, cfgPath, defaultEvent)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout=%q", code, conformance.ExitOK, got)
	}
	if v := checkReplyAgainstSchema(t, got); v["startedAt"] != nil {
		t.Fatalf("startedAt = %v, want the field absent; reply=%s", v["startedAt"], got)
	}
}

// readyLaunchStubs builds stub ccpool and bd binaries for a fresh launch that
// reaches ready: `ccpool new` succeeds and from then on the pool lists the new
// session live and working; replyExit is the exit status of `ccpool reply` (the
// prompt delivery); the bead reads in_progress until `new` ran, then closed, so
// the wait ends at once on the completion policy. It returns the launch config
// path and the ccpool call log.
func readyLaunchStubs(t *testing.T, replyExit int) (cfgPath, callLog string) {
	t.Helper()
	root := t.TempDir()
	ext := filepath.Join(root, "ext")
	callLog = filepath.Join(root, "calls.log")
	bin := stubBin(t, map[string]string{
		"ccpool": fmt.Sprintf(`echo "$*" >> %[1]q
case "$1" in
  list)
    if [ -s %[2]q ]; then
      read -r id < %[2]q
      printf '[{"external_id":"%%s","name":"pg-router-worker-zr-w","state":"working","live":true,"cwd":%[3]q,"meta":{}}]\n' "$id"
    else echo '[]'; fi ;;
  capacity) echo '{"max_sessions":4,"free":4,"counted":0,"live":0,"preserved":0}' ;;
  new) echo "$2" > %[2]q ;;
  reply) exit %[4]d ;;
esac
exit 0
`, callLog, ext, root, replyExit),
		"bd": fmt.Sprintf("for a in \"$@\"; do\n  if [ \"$a\" = show ]; then\n    if [ -s %q ]; then s=closed; else s=in_progress; fi\n    echo \"[{\\\"id\\\":\\\"zr-w\\\",\\\"status\\\":\\\"$s\\\"}]\"\n    exit 0\n  fi\ndone\necho '[]'\nexit 0\n", ext),
	})
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cfgPath = filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`{"PollInterval":%d,"repoRoot":%q}`, 20*time.Millisecond, root)), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, callLog
}

// A launch that reaches ready is ACCEPTED: the dispatch delivers the prompt
// through `ccpool reply`, waits, reaches the completion policy and answers with
// an inline outcome (exit 0), never a decline.
func TestDispatch_readyLaunchIsAcceptedAndReachesCompletion(t *testing.T) {
	cfgPath, callLog := readyLaunchStubs(t, 0)
	name, cfg := workerRole("", "")
	code, got := dispatchWith(t, name, cfg, cfgPath, defaultEvent)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout=%q", code, conformance.ExitOK, got)
	}
	v := checkReplyAgainstSchema(t, got)
	if _, ok := v["outcome"]; !ok {
		t.Fatalf("want an inline outcome reply; reply=%s", got)
	}
	calls, _ := os.ReadFile(callLog)
	for _, want := range []string{"new pg-router-worker-zr-w-", "reply pg-router-worker-zr-w-"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("ccpool calls lack %q (external id format <prefix><role>-<bead>-<stamp>):\n%s", want, calls)
		}
	}
	if !strings.Contains(string(calls), "hello") {
		t.Errorf("the rendered prompt body must be delivered through ccpool reply:\n%s", calls)
	}
}

// A failure just AFTER ready (the prompt is confirmed dropped, ccpool reply exit
// 7) stays an ACCEPTED dispatch, exactly as in the old handler: it is a handler
// failure the core records (error reply, exit 1) after the handler has handed the
// bead back, never a pre-accept decline the core would re-offer.
func TestDispatch_failureAfterReadyStaysAccepted(t *testing.T) {
	cfgPath, callLog := readyLaunchStubs(t, 7)
	name, cfg := workerRole("", "")
	code, got := dispatchWith(t, name, cfg, cfgPath, defaultEvent)
	if code == conformance.ExitBusy {
		t.Fatalf("a failure after ready must not decline; stdout=%q", got)
	}
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d (an accepted dispatch that failed); stdout=%q", code, conformance.ExitError, got)
	}
	if msg, _ := decodeReply(t, got)["error"].(string); !strings.Contains(msg, "prompt not ingested") {
		t.Errorf("error = %q, want the dropped-nudge failure", msg)
	}
	if calls, _ := os.ReadFile(callLog); !strings.Contains(string(calls), "reply ") {
		t.Errorf("the prompt delivery was never attempted:\n%s", calls)
	}
}

// A retry-windowed source suffixes the event's item id (<bead>@<12 hex>) and
// keeps the bead id in metadata.entity_id. The ccpool dispatch works on the
// bead: the session it launches is named for the bare id, with no suffix.
func TestDispatch_retryWindowedEventLaunchesWithTheBareBeadID(t *testing.T) {
	cfgPath, callLog := readyLaunchStubs(t, 0)
	name, cfg := workerRole("", "")
	event := `{"id":"e-1","type":"dispatch","payload":{"id":"zr-w@46d60fca149d","metadata":{"entity_id":"zr-w"}}}`
	code, got := dispatchWith(t, name, cfg, cfgPath, event)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout=%q", code, conformance.ExitOK, got)
	}
	calls, _ := os.ReadFile(callLog)
	if !strings.Contains(string(calls), "new pg-router-worker-zr-w-") {
		t.Errorf("ccpool new must name the session for the bare bead id:\n%s", calls)
	}
	if strings.Contains(string(calls), "@") {
		t.Errorf("the retry-window suffix must not reach ccpool:\n%s", calls)
	}
}
