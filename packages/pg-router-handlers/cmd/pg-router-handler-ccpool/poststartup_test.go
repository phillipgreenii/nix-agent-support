package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/handlerstate"
	"github.com/phillipgreenii/pg-router/conformance"
)

// kickRecorder is a janitorKick that records its calls instead of spawning.
type kickRecorder struct {
	calls []string // "<cfgPath>|<roleFile>"
	err   error
}

func (k *kickRecorder) kick(cfgPath, roleFile string) error {
	k.calls = append(k.calls, cfgPath+"|"+roleFile)
	return k.err
}

func postStartupWith(t *testing.T, body, cfgPath string, kick janitorKick) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := servePostStartup(strings.NewReader(body), &out, cfgPath, kick)
	return code, out.String()
}

// TestServePostStartup_doesNotSweep proves postStartup no longer runs the
// housekeeping passes (INV-CCH-41, bead pg2-fx5x0l.6): servePostStartup takes no
// ccpool or bd seam at all, so nothing can be swept; the reply is the conformant
// `ok` and the janitor is kicked exactly once with the launch config and a role
// file naming this role. (The test below shows the sweep's absence does not cost
// the role's first dispatch.)
func TestServePostStartup_doesNotSweep(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	cfgPath, stateDir := launchConfig(t, `,"SessionPrefix":"pg-router-"`)
	name, cfg := workerRole("", "")
	k := &kickRecorder{}
	code, out := postStartupWith(t, request("", name, cfg), cfgPath, k.kick)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; reply %s", code, conformance.ExitOK, out)
	}
	reply := decodeReply(t, out)
	if reply["id"] != "d-1" || reply["outcome"] != "ok" {
		t.Errorf("reply = %+v, want id=d-1 outcome=ok", reply)
	}
	if err := conformance.Check("handler.postStartup-reply", reply); err != nil {
		t.Errorf("reply does not conform: %v", err)
	}
	if len(k.calls) != 1 {
		t.Fatalf("janitor kicks = %v, want exactly one", k.calls)
	}
	parts := strings.SplitN(k.calls[0], "|", 2)
	if parts[0] != cfgPath {
		t.Errorf("kick config = %q, want %q", parts[0], cfgPath)
	}
	if filepath.Dir(parts[1]) != stateDir {
		t.Errorf("role file %q must sit in the handler state dir %q", parts[1], stateDir)
	}
	body, err := os.ReadFile(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	ro, err := parseRole(body)
	if err != nil || ro.Name != name {
		t.Errorf("role file = %s (%v); want the %q role object the janitor can decode", body, err, name)
	}
}

// A janitor that cannot be started never fails the call: the scheduled janitor
// still runs, and the reply is still `ok`.
func TestServePostStartup_kickFailureIsNotAFailure(t *testing.T) {
	cfgPath, _ := launchConfig(t, "")
	name, cfg := workerRole("", "")
	k := &kickRecorder{err: errors.New("spawn refused")}
	var code int
	var out string
	msgs := captureSlog(t, func() { code, out = postStartupWith(t, request("", name, cfg), cfgPath, k.kick) })
	if code != conformance.ExitOK || decodeReply(t, out)["outcome"] != "ok" {
		t.Fatalf("exit = %d, reply = %s; want ok", code, out)
	}
	if !containsSubstring(msgs, "postStartup: janitor kick failed; the scheduled janitor still runs") {
		t.Errorf("a failed kick must be logged; got %v", msgs)
	}
}

// Calling postStartup again (core calls it once per role, and again after every
// daemon restart) is safe: each call replies ok and kicks the janitor, whose
// single-flight lock makes the extra runs harmless.
func TestServePostStartup_repeatedCallsAreSafe(t *testing.T) {
	cfgPath, _ := launchConfig(t, "")
	name, cfg := workerRole("", "")
	k := &kickRecorder{}
	for i := 0; i < 3; i++ {
		if code, out := postStartupWith(t, request("", name, cfg), cfgPath, k.kick); code != conformance.ExitOK {
			t.Fatalf("call %d: exit = %d, reply %s", i, code, out)
		}
	}
	if len(k.calls) != 3 {
		t.Errorf("kicks = %d, want 3", len(k.calls))
	}
}

// TestKickJanitor_startsADetachedRealJanitor runs the production kick against the
// real tool: the test binary stands in for the adapter (adapterRunEnv), the
// janitor it starts runs against this test's own state dir, and its janitor_run
// event proves the process ran. The kick itself returns without waiting for it.
func TestKickJanitor_startsADetachedRealJanitor(t *testing.T) {
	t.Setenv(adapterRunEnv, "1")
	t.Setenv("PATH", "/usr/bin") // ccpool and bd are unreachable: the passes fail best-effort
	t.Setenv("CCPOOL_POOL", "")
	cfgPath, stateDir := launchConfig(t, "")
	name, cfg := workerRole("", "")
	var out bytes.Buffer
	k := func(c, r string) error { return kickJanitor(c, r) }
	if code := servePostStartup(strings.NewReader(request("", name, cfg)), &out, cfgPath, k); code != conformance.ExitOK {
		t.Fatalf("exit = %d, reply %s", code, out.String())
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(stateDir, handlerstate.EventLogName))
		if strings.Contains(string(b), `"janitor_run"`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the kicked janitor wrote no janitor_run event; log=%s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestFirstDispatchAfterSimulatedBootLaunchesWithAFullPoolOfClosedBeadSessions
// pins I-4 (pg2-fx5x0l.6): postStartup sweeps nothing, so a role's first dispatch
// after a boot meets a pool full of sessions whose beads closed while the daemon
// was down, and still launches, because the capacity-pressure reconcile frees a
// slot (admission.go). The boot is postStartup, then the dispatch, on one launch
// config and one pool.
func TestFirstDispatchAfterSimulatedBootLaunchesWithAFullPoolOfClosedBeadSessions(t *testing.T) {
	e := newAdmissionEnv(t, "", pressureRow("a", "zr-x"), pressureRow("b", "zr-x"))
	name, cfg := workerRole("", "")
	k := &kickRecorder{}
	if code, out := postStartupWith(t, request("", name, cfg), e.cfgPath, k.kick); code != conformance.ExitOK {
		t.Fatalf("postStartup exit = %d, reply %s", code, out)
	}
	if got := e.cc.closed(); len(got) != 0 {
		t.Fatalf("postStartup closed %v; the boot must leave the full pool for the dispatch to meet", got)
	}
	if _, caps := e.cc.counts(); caps != 0 {
		t.Fatalf("postStartup touched ccpool capacity (%d calls)", caps)
	}
	code, reply := e.dispatch(t, defaultEvent)
	if code != conformance.ExitBusy || reply["reason"] != "launch-failed" {
		t.Fatalf("exit=%d reply=%v, want the first dispatch to pass the capacity gate and reach the (failing) launch", code, reply)
	}
	got := e.cc.closed()
	if len(got) == 0 || got[0] != "pg-router-worker-a" {
		t.Fatalf("closed = %v, want a closed-bead session freed for the dispatch", got)
	}
}

func noKick(t *testing.T) janitorKick {
	return func(string, string) error {
		t.Error("the janitor was kicked for a request that must be refused first")
		return nil
	}
}

// TestServePostStartup_rejectsMalformedRequest proves the schema check runs
// before anything else.
func TestServePostStartup_rejectsMalformedRequest(t *testing.T) {
	code, out := postStartupWith(t, `{"schemaVersion":"1","role":{"name":"worker"}}`, "", noKick(t)) // missing id
	if code != conformance.ExitError || !strings.Contains(out, "error") {
		t.Fatalf("exit = %d, out = %q; want exit %d and an error reply", code, out, conformance.ExitError)
	}
}

// A request without a usable role object is a usage error with an error reply.
func TestServePostStartup_requestWithoutRoleIsUsageError(t *testing.T) {
	for name, body := range map[string]string{
		"no role":    `{"schemaVersion":"1","id":"hs-1"}`,
		"empty name": `{"schemaVersion":"1","id":"hs-1","role":{"name":""}}`,
	} {
		t.Run(name, func(t *testing.T) {
			code, out := postStartupWith(t, body, "", noKick(t))
			if code != conformance.ExitUsage || !strings.Contains(out, "error") {
				t.Fatalf("exit = %d, out = %q; want exit %d and an error reply", code, out, conformance.ExitUsage)
			}
		})
	}
}

// A role whose config the strict decoder rejects, or a launch config that
// cannot be loaded, is an error reply (exit 1) and the janitor is not kicked.
func TestServePostStartup_invalidRoleOrLaunchConfigIsAnError(t *testing.T) {
	cfgPath, _ := launchConfig(t, "")
	_, good := workerRole("", "")
	bad := mustCfg(`{"bogus":1}`)
	for name, tc := range map[string]struct {
		cfg     map[string]any
		cfgPath string
		want    string
	}{
		"unknown role config key": {bad, cfgPath, `unknown key "bogus"`},
		"no ccpool table":         {map[string]any{}, cfgPath, "role.config.ccpool: required"},
		"unreadable launch cfg":   {good, "/does/not/exist/launch.json", "launch.json"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := postStartupWith(t, request("", "worker", tc.cfg), tc.cfgPath, noKick(t))
			msg, _ := decodeReply(t, out)["error"].(string)
			if code != conformance.ExitError || !strings.Contains(msg, tc.want) {
				t.Fatalf("exit = %d, out = %q; want exit %d and %q in the error reply", code, out, conformance.ExitError, tc.want)
			}
		})
	}
}

// The CLI entry point reads --config and the request from stdin; a flag that
// would name a role file is not accepted (the role comes from stdin).
func TestRunPostStartup_flags(t *testing.T) {
	if got := runPostStartup([]string{"--role-file", "/x"}); got != conformance.ExitUsage {
		t.Errorf("--role-file: exit %d, want %d", got, conformance.ExitUsage)
	}
	if got := runPostStartup([]string{"stray"}); got != conformance.ExitUsage {
		t.Errorf("a positional argument: exit %d, want %d", got, conformance.ExitUsage)
	}
}
