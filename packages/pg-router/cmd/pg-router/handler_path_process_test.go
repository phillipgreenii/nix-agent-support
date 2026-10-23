//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/emit"
)

// Docket pg2-12gr4.10 (the handler split): a real-process proof of the new
// path's wiring. A real router process (the TestHelperRouterProcess re-exec of
// shutdown_reoffer_process_test.go, so signal handling, the durable queue, the
// unix-socket core and the real wireclient exec are the production ones) is
// started with TWO roles that each set handler to their own stub script, and NO
// global PG_ROUTER_HANDLER_COMMAND-based role. Each stub records its own argv and
// the stdin it was handed, so the assertions are made from the stub's own
// recording, not from core's. The two roles differ only in survives_shutdown:
//
//   - the role with survives_shutdown = true keeps running through shutdown (its
//     stub's heartbeat continues after the router has exited);
//   - the role without it is cancelled by the shutdown (its heartbeat stops).
//
// SAFETY MODEL: the same as TestShutdownReoffer_RealRouterProcess. The only
// process signalled is the router child this test started. The stubs never
// sleep longer than the release file allows and also end by themselves after
// stubMaxSeconds, so a leaked one cannot outlive the test by long.
const (
	stubMaxSeconds = 120
	keepRole       = "keeper"
	killRole       = "victim"
	keepType       = "keep.ready"
	killType       = "kill.ready"
)

// writeStub writes the per-role handler stub: it records argv, the stdin of
// every call, and which subcommands ran; a dispatch beats a heartbeat line every
// 0.2 s until the release marker appears.
func writeStub(t *testing.T, path, markers, role string) {
	t.Helper()
	// stderr is detached first: a surviving stub inherits the router's stderr pipe,
	// and the test's cmd.Wait would otherwise block until the stub exits.
	script := "#!/bin/sh\n" +
		"exec 2>/dev/null\n" +
		"M='" + markers + "'\n" +
		"R='" + role + "'\n" +
		"sub=\"$1\"\n" +
		"echo \"argc=$# argv=$*\" >> \"$M/argv-$R\"\n" +
		"cat > \"$M/stdin-$R-$sub.json\"\n" +
		"echo \"$sub\" >> \"$M/calls-$R\"\n" +
		"case \"$sub\" in\n" +
		"dispatch)\n" +
		"  n=0\n" +
		"  while [ ! -f \"$M/release\" ] && [ $n -lt " + fmt.Sprint(stubMaxSeconds*5) + " ]; do\n" +
		"    echo b >> \"$M/beat-$R\"\n" +
		"    sleep 0.2\n" +
		"    n=$((n+1))\n" +
		"  done\n" +
		"  ;;\n" +
		"validate) exit 2 ;;\n" +
		"esac\n" +
		"echo '{\"schemaVersion\":\"1\",\"id\":\"x\",\"outcome\":\"ok\"}'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", path, err)
	}
}

func (h *reofferHarness) injectJSON(ev string) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	loc := emit.Locator{Discover: emit.Discoverer(h.logDir)}
	if code := pushInject(&out, &errOut, true, loc, emit.SocketEnqueuer{}, ev); code != exitOK {
		h.t.Fatalf("push-inject exit = %d\nstdout=%s\nstderr=%s", code, out.String(), errOut.String())
	}
}

func readMarker(t *testing.T, h *reofferHarness, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.markers, name))
	if err != nil {
		t.Fatalf("read marker %s: %v", name, err)
	}
	return string(b)
}

func TestHandlerPath_RealRouterProcess_newPathWiring(t *testing.T) {
	if _, set := os.LookupEnv(routerSentinelEnv); set {
		t.Fatalf("recursion guard: %s is already set in the test process's own environment", routerSentinelEnv)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	h := newReofferHarness(t)
	keepStub := filepath.Join(h.base, "stub-keeper.sh")
	killStub := filepath.Join(h.base, "stub-victim.sh")
	writeStub(t, keepStub, h.markers, keepRole)
	writeStub(t, killStub, h.markers, killRole)
	cfg := `[[query]]
name = "idle-source"
emits = ["` + keepType + `", "` + killType + `"]
type = "command"
[query.command]
argv = ["true"]
format = "jsonl"

[[role]]
name = "` + keepRole + `"
enabled = true
binds = ["` + keepType + `"]
handler = "` + keepStub + `"
survives_shutdown = true
[role.config]
label = "keep-label"
[role.config.nested]
n = 3

[[role]]
name = "` + killRole + `"
enabled = true
binds = ["` + killType + `"]
handler = "` + killStub + `"
`
	if err := os.WriteFile(h.configPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	// The release marker ends any stub still beating when the test is over.
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(h.markers, "release"), []byte("1\n"), 0o644) })

	r := h.startRouter()
	h.waitCore(r)
	h.injectJSON(`{"schemaVersion":"1","id":"k-1","type":"` + keepType + `","expiresAt":"2099-01-01T00:00:00Z","payload":{"n":1}}`)
	h.injectJSON(`{"schemaVersion":"1","id":"v-1","type":"` + killType + `","expiresAt":"2099-01-01T00:00:00Z","payload":{"n":2}}`)
	h.waitFor(30*time.Second, "both stubs to be dispatched\nstderr:\n"+r.stderr.String(), func() bool {
		return h.markerLines("beat-"+keepRole) >= 2 && h.markerLines("beat-"+killRole) >= 2
	})

	// (1) argv is [stub, dispatch], with no --role-config; the global handler
	// (h.script, PG_ROUTER_HANDLER_COMMAND) was never invoked for either role.
	for _, role := range []string{keepRole, killRole} {
		for _, line := range strings.Split(strings.TrimSpace(readMarker(t, h, "argv-"+role)), "\n") {
			if strings.Contains(line, "--role-config") {
				t.Errorf("%s: stub was invoked with --role-config: %q", role, line)
			}
			if !strings.HasPrefix(line, "argc=1 ") {
				t.Errorf("%s: stub invoked with extra arguments: %q", role, line)
			}
		}
		if !strings.Contains(readMarker(t, h, "calls-"+role), "dispatch") {
			t.Errorf("%s: the stub never recorded a dispatch call", role)
		}
	}
	if n := h.markerLines(reofferStartedMarker); n != 0 {
		t.Errorf("the global PG_ROUTER_HANDLER_COMMAND handler ran %d time(s) for a role with its own handler", n)
	}

	// (2) stdin carries the role object with the configured [role.config].
	var req struct {
		SchemaVersion string `json:"schemaVersion"`
		ID            string `json:"id"`
		Event         struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"event"`
		Role struct {
			Name   string         `json:"name"`
			Config map[string]any `json:"config"`
		} `json:"role"`
	}
	if err := json.Unmarshal([]byte(readMarker(t, h, "stdin-"+keepRole+"-dispatch.json")), &req); err != nil {
		t.Fatalf("keeper dispatch stdin is not JSON: %v", err)
	}
	if req.Role.Name != keepRole || req.Event.ID != "k-1" {
		t.Errorf("keeper dispatch stdin: role=%q event=%q, want %q / k-1", req.Role.Name, req.Event.ID, keepRole)
	}
	wantCfg := map[string]any{"label": "keep-label", "nested": map[string]any{"n": float64(3)}}
	if !reflect.DeepEqual(req.Role.Config, wantCfg) {
		t.Errorf("keeper role.config = %#v, want %#v", req.Role.Config, wantCfg)
	}
	var vreq struct {
		Role map[string]json.RawMessage `json:"role"`
	}
	if err := json.Unmarshal([]byte(readMarker(t, h, "stdin-"+killRole+"-dispatch.json")), &vreq); err != nil {
		t.Fatalf("victim dispatch stdin is not JSON: %v", err)
	}
	if string(vreq.Role["config"]) != "{}" || string(vreq.Role["name"]) != `"`+killRole+`"` {
		t.Errorf("victim role object = %v, want name %q and config {}", vreq.Role, killRole)
	}
	// The lifecycle calls reached each role's own adapter, with the role object.
	for _, role := range []string{keepRole, killRole} {
		if !strings.Contains(readMarker(t, h, "calls-"+role), "postStartup") {
			t.Errorf("%s: its own adapter never received postStartup", role)
		}
		if !strings.Contains(readMarker(t, h, "stdin-"+role+"-postStartup.json"), `"role":{"name":"`+role+`"`) {
			t.Errorf("%s: postStartup stdin lacks the role object", role)
		}
	}

	// (3) shutdown: the survives_shutdown role keeps running, the other is cancelled.
	code, _ := h.stop(r, 90*time.Second)
	if code != exitOK {
		t.Fatalf("router exit = %d, want %d\nstderr:\n%s", code, exitOK, r.stderr.String())
	}
	for _, role := range []string{keepRole, killRole} {
		if !strings.Contains(readMarker(t, h, "calls-"+role), "preShutdown") {
			t.Errorf("%s: its own adapter never received preShutdown", role)
		}
	}
	keepBefore, killBefore := h.markerLines("beat-"+keepRole), h.markerLines("beat-"+killRole)
	time.Sleep(1500 * time.Millisecond)
	if got := h.markerLines("beat-" + keepRole); got < keepBefore+2 {
		t.Errorf("survives_shutdown role stopped with the router: heartbeats %d -> %d, want it to keep running\nstderr:\n%s", keepBefore, got, r.stderr.String())
	}
	if got := h.markerLines("beat-" + killRole); got > killBefore+1 {
		t.Errorf("role without survives_shutdown kept running after the router exited: heartbeats %d -> %d, want it cancelled", killBefore, got)
	}
	if !strings.Contains(r.stderr.String(), "role dispatch survives shutdown") {
		t.Errorf("router never logged that %s's dispatch survives shutdown\nstderr:\n%s", keepRole, r.stderr.String())
	}
}
