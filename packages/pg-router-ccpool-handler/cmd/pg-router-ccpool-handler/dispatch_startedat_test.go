package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

// The dispatch reply's optional startedAt (INV-CCH-30, DEC-EVENT-5): a dispatch
// that adopts a session an earlier offer launched replies with that session's
// launch time, so the core times the dispatch from its real start; every other
// dispatch replies with no such field.

func checkReplyAgainstSchema(t *testing.T, reply []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(reply, &v); err != nil {
		t.Fatalf("reply %q is not a JSON object: %v", reply, err)
	}
	if err := conformance.Check("handler.dispatch-reply", v); err != nil {
		t.Fatalf("reply %s failed the handler.dispatch-reply schema: %v", reply, err)
	}
	return v
}

func TestOutcomeReply_carriesStartedAtOnlyWhenReported(t *testing.T) {
	launched := time.Date(2026, 10, 12, 23, 3, 46, 480_000_000, time.FixedZone("EDT", -4*3600))
	for _, tc := range []struct {
		name      string
		startedAt time.Time
		want      string // "" = the field must be absent
	}{
		{"an adopted session's launch time, as UTC to the second", launched, "2026-10-13T03:03:46Z"},
		{"a fresh session reports none", time.Time{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(outcomeReply("d-1", `{"actions":[]}`, tc.startedAt))
			if err != nil {
				t.Fatal(err)
			}
			v := checkReplyAgainstSchema(t, b)
			got, present := v["startedAt"]
			if tc.want == "" {
				if present {
					t.Fatalf("startedAt = %v, want the field absent; reply=%s", got, b)
				}
			} else if got != tc.want {
				t.Fatalf("startedAt = %v, want %q; reply=%s", got, tc.want, b)
			}
			if v["id"] != "d-1" || v["outcome"] != `{"actions":[]}` || v["schemaVersion"] != "1" {
				t.Fatalf("the rest of the reply must be unchanged; reply=%s", b)
			}
		})
	}
}

// runAdoptingDispatch runs the real `dispatch` subcommand against stub ccpool
// and bd binaries, with a pool holding one live, working session for the
// dispatched bead and role that records the dispatch's own event id (the one
// session a dispatch absorbs, INV-CCH-30). The stub bead reads in_progress
// until the absorbing dispatch refreshes the session's lease (ccpool meta set),
// then closed, so the wait ends at once. launchedAt is the session's
// pgrouter.launched_at ("" records none, as a session an older build launched).
func runAdoptingDispatch(t *testing.T, launchedAt string) (int, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "absorbed")
	launchedMeta := ""
	if launchedAt != "" {
		launchedMeta = fmt.Sprintf(`,"pgrouter.launched_at":%q`, launchedAt)
	}
	session := fmt.Sprintf(`[{"external_id":"att-old","name":"pg-router-worker-zr-w","state":"working","live":true,"cwd":%q,"meta":{"pgrouter.event_id":"e-1"%s}}]`,
		filepath.Join(root, "wt"), launchedMeta)
	ccpoolStub := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nlist) echo '%s' ;;\nmeta) : > %q ;;\nesac\nexit 0\n", session, marker)
	bdStub := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = show ]; then\n    if [ -e %q ]; then s=closed; else s=in_progress; fi\n    echo \"[{\\\"id\\\":\\\"zr-w\\\",\\\"status\\\":\\\"$s\\\"}]\"\n    exit 0\n  fi\ndone\necho '[]'\nexit 0\n", marker)
	for name, body := range map[string]string{"ccpool": ccpoolStub, "bd": bdStub} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("CCPOOL_POOL", "")

	rolePath := filepath.Join(root, "role.json")
	roleJSON := `{"name":"worker","type":"ccpool","ccpool":{"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello"}}`
	if err := os.WriteFile(rolePath, []byte(roleJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`{"PollInterval":%d}`, 20*time.Millisecond)), 0o644); err != nil {
		t.Fatal(err)
	}

	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}}`)
	defer restoreIn()
	var code int
	got := captureStdout(t, func() {
		code = runDispatch([]string{"--role-config", rolePath, "--config", cfgPath})
	})
	return code, got
}

// The wire-level guarantee: a dispatch that absorbs a session launched at T
// replies with startedAt = T, and the reply is a valid handler.dispatch-reply.
func TestRunDispatch_adoptedSessionRepliesLaunchTimeAsStartedAt(t *testing.T) {
	const launched = "2026-10-12T23:03:46Z"
	code, got := runAdoptingDispatch(t, launched)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout=%q", code, conformance.ExitOK, got)
	}
	v := checkReplyAgainstSchema(t, []byte(got))
	if _, ok := v["outcome"]; !ok {
		t.Fatalf("want an inline outcome reply; reply=%s", got)
	}
	if v["startedAt"] != launched {
		t.Fatalf("startedAt = %v, want the adopted session's launched_at %q; reply=%s", v["startedAt"], launched, got)
	}
}

// An adopted session that recorded no launch time (one an older build launched)
// replies with no startedAt, and the reply is still a valid handler.dispatch-reply.
func TestRunDispatch_adoptedSessionWithoutLaunchTimeRepliesNoStartedAt(t *testing.T) {
	code, got := runAdoptingDispatch(t, "")
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout=%q", code, conformance.ExitOK, got)
	}
	v := checkReplyAgainstSchema(t, []byte(got))
	if _, present := v["startedAt"]; present {
		t.Fatalf("startedAt = %v, want the field absent; reply=%s", v["startedAt"], got)
	}
}
