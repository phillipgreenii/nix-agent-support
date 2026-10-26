package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

// TestLifecycle_AreNoOpOkReplies: postStartup and preShutdown answer the schema's
// ok reply, echoing the request id, and run nothing.
func TestLifecycle_AreNoOpOkReplies(t *testing.T) {
	for _, name := range []string{"postStartup", "preShutdown"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			req := request(t, nil, "cmdrole", cmdConfig(false, "/bin/sh", "-c", ":"), true)
			if exit := serveLifecycle(name, strings.NewReader(req), &out); exit != conformance.ExitOK {
				t.Fatalf("exit = %d; %s", exit, out.String())
			}
			m := decodeReply(t, out.String())
			if err := conformance.Check("handler."+name+"-reply", m); err != nil {
				t.Fatalf("reply %s is not conformant: %v", out.String(), err)
			}
			if m["id"] != "req-1" || m["outcome"] != "ok" {
				t.Fatalf("reply = %v, want id req-1 outcome ok", m)
			}
		})
	}
}

// TestLifecycle_RequestWithoutRoleIsUsageError: a lifecycle request needs the
// role object like every other request.
func TestLifecycle_RequestWithoutRoleIsUsageError(t *testing.T) {
	for _, name := range []string{"postStartup", "preShutdown"} {
		var out bytes.Buffer
		if exit := serveLifecycle(name, strings.NewReader(request(t, nil, "", nil, false)), &out); exit != conformance.ExitUsage {
			t.Fatalf("%s: exit = %d, want %d", name, exit, conformance.ExitUsage)
		}
	}
	var out bytes.Buffer
	if exit := serveLifecycle("postStartup", strings.NewReader(`{"schemaVersion":"2"}`), &out); exit != conformance.ExitError {
		t.Fatalf("schema violation: exit = %d, want %d", exit, conformance.ExitError)
	}
}
