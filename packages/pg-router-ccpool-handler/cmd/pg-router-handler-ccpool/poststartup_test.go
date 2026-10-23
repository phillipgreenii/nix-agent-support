package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dispatchlib"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router/conformance"
)

func postStartupWith(t *testing.T, body, cfgPath string, mk func(config.Config, roles.Role) executor.Deps) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := servePostStartup(context.Background(), strings.NewReader(body), &out, cfgPath, mk)
	return code, out.String()
}

// TestServePostStartup_runsThePostStartupReconcile proves postStartup is the
// post-startup reconcile, not the old no-op: an idle session of this handler
// whose bead has closed (the daemon was down while the bead closed) is purged at
// boot, through the same pass a dispatch runs before its executor, and the reply
// is the conformant `ok`.
func TestServePostStartup_runsThePostStartupReconcile(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "") // skip the default-pool pass: it builds a real runner
	cc := &fakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pg-router-worker-zr-done", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-done"}},
		{ExternalID: "pg-router-worker-zr-open", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-open"}},
		{ExternalID: "other-worker-zr-done", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-done"}},
	}}}
	br := fakeBR{out: map[string]string{
		"show zr-done --json": `{"status":"closed"}`,
		"show zr-open --json": `{"status":"open"}`,
	}}
	cfgPath, _ := launchConfig(t, `,"SessionPrefix":"pg-router-"`)
	name, cfg := workerRole("", "")
	code, out := postStartupWith(t, request("", name, cfg), cfgPath, func(config.Config, roles.Role) executor.Deps {
		return executor.Deps{CC: cc, BD: br, LockDir: t.TempDir()}
	})
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
	if len(cc.Closed) != 1 || cc.Closed[0] != "pg-router-worker-zr-done" {
		t.Errorf("postStartup must close exactly the closed-bead session of this handler; closed=%v", cc.Closed)
	}
}

// A sweep that cannot run (ccpool is unavailable) never fails the call: every
// pass is best effort and the reply is still `ok`. With PATH broken the real
// BuildDeps runner fails each list; the log lines prove the passes ran.
func TestServePostStartup_sweepFailureIsNotAFailure(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("CCPOOL_POOL", "")
	cfgPath, _ := launchConfig(t, "")
	name, cfg := workerRole("", "")
	var code int
	var out string
	msgs := captureSlog(t, func() {
		code, out = postStartupWith(t, request("", name, cfg), cfgPath, dispatchlib.BuildDeps)
	})
	if code != conformance.ExitOK || decodeReply(t, out)["outcome"] != "ok" {
		t.Fatalf("exit = %d, reply = %s; want ok", code, out)
	}
	for _, want := range []string{"reconcile: list failed", "orphan reconcile: list failed"} {
		if !containsSubstring(msgs, want) {
			t.Errorf("postStartup must run the sweeps (missing %q); got messages=%v", want, msgs)
		}
	}
}

// The sweeps are idempotent: calling postStartup again (core calls it once per
// role) finds nothing left to do and still replies ok.
func TestServePostStartup_repeatedCallsAreSafe(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	cc := &fakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pg-router-worker-zr-done", State: ccpool.StateIdle, Meta: map[string]string{ccpool.MetaKeyBead: "zr-done"}}},
		nil,
	}}
	br := fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
	cfgPath, _ := launchConfig(t, `,"SessionPrefix":"pg-router-"`)
	name, cfg := workerRole("", "")
	mk := func(config.Config, roles.Role) executor.Deps {
		return executor.Deps{CC: cc, BD: br, LockDir: t.TempDir()}
	}
	for i := 0; i < 3; i++ {
		if code, out := postStartupWith(t, request("", name, cfg), cfgPath, mk); code != conformance.ExitOK {
			t.Fatalf("call %d: exit = %d, reply %s", i, code, out)
		}
	}
	if len(cc.Closed) != 1 {
		t.Errorf("the session must be closed once across repeated calls; closed=%v", cc.Closed)
	}
}

func noDeps(t *testing.T) func(config.Config, roles.Role) executor.Deps {
	return func(config.Config, roles.Role) executor.Deps {
		t.Error("deps were built for a request that must be refused first")
		return executor.Deps{}
	}
}

// TestServePostStartup_rejectsMalformedRequest proves the schema check runs
// before anything else.
func TestServePostStartup_rejectsMalformedRequest(t *testing.T) {
	code, out := postStartupWith(t, `{"schemaVersion":"1","role":{"name":"worker"}}`, "", noDeps(t)) // missing id
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
			code, out := postStartupWith(t, body, "", noDeps(t))
			if code != conformance.ExitUsage || !strings.Contains(out, "error") {
				t.Fatalf("exit = %d, out = %q; want exit %d and an error reply", code, out, conformance.ExitUsage)
			}
		})
	}
}

// A role whose config the strict decoder rejects, or a launch config that
// cannot be loaded, is an error reply (exit 1) and no sweep runs.
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
			code, out := postStartupWith(t, request("", "worker", tc.cfg), tc.cfgPath, noDeps(t))
			msg, _ := decodeReply(t, out)["error"].(string)
			if code != conformance.ExitError || !strings.Contains(msg, tc.want) {
				t.Fatalf("exit = %d, out = %q; want exit %d and %q in the error reply", code, out, conformance.ExitError, tc.want)
			}
		})
	}
}

// The CLI entry point reads --config and the request from stdin; the old
// --role-config flag is not accepted (the role comes from stdin).
func TestRunPostStartup_flags(t *testing.T) {
	if got := runPostStartup([]string{"--role-config", "/x"}); got != conformance.ExitUsage {
		t.Errorf("--role-config: exit %d, want %d", got, conformance.ExitUsage)
	}
	if got := runPostStartup([]string{"stray"}); got != conformance.ExitUsage {
		t.Errorf("a positional argument: exit %d, want %d", got, conformance.ExitUsage)
	}
}
