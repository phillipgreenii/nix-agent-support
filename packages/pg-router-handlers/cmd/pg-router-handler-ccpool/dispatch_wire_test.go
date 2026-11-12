package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
)

// callArgs returns the argument list of the first recorded stub call whose first
// argument is sub (calls are separated by a "--" line, see ccpoolNewFails).
func callArgs(t *testing.T, log, sub string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(string(b), "--\n") {
		if args := strings.Split(strings.TrimSuffix(call, "\n"), "\n"); args[0] == sub {
			return args
		}
	}
	t.Fatalf("no `ccpool %s` call recorded:\n%s", sub, b)
	return nil
}

// In-flight sessions the OLD handler started MUST be adopted by this adapter
// across the cutover (ADR 0085), and the reverse: the external id format, the
// session naming and the dispatch metadata this adapter writes are pinned here
// to the old handler's (both are the shared library's, so this is the guard
// against anyone giving the adapter its own).
func TestDispatch_externalIDSessionNamingAndMetadataUnchanged(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("PATH", stubBin(t, map[string]string{"ccpool": ccpoolNewFails(log)})+":/usr/bin:/bin")
	name, cfg := workerRole("", "")
	if code, got := dispatchWith(t, name, cfg, "", defaultEvent); code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (the stub's new fails); stdout=%q", code, got)
	}
	args := callArgs(t, log, "new")

	// <prefix><role>-<bead>-<stamp>, the stamp being 20060102T150405.000000000.
	if !regexp.MustCompile(`^pg-router-worker-zr-w-\d{8}T\d{6}\.\d{9}$`).MatchString(args[1]) {
		t.Errorf("external id = %q, want pg-router-worker-zr-w-<yyyymmddThhmmss.nnnnnnnnn>", args[1])
	}
	flagValues := func(flag string) []string {
		var out []string
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				out = append(out, args[i+1])
			}
		}
		sort.Strings(out)
		return out
	}
	if got := flagValues("--name"); len(got) != 1 || got[0] != "pg-router-worker-zr-w" {
		t.Errorf("--name = %q, want the stable per-bead-and-role display name pg-router-worker-zr-w", got)
	}
	var metaKeys []string
	for _, m := range flagValues("--meta") {
		metaKeys = append(metaKeys, strings.SplitN(m, "=", 2)[0])
	}
	want := []string{"pgrouter.bead", "pgrouter.event_id", "pgrouter.launched_at", "pgrouter.lease_until", "pgrouter.pool", "pgrouter.role"}
	if strings.Join(metaKeys, ",") != strings.Join(want, ",") {
		t.Errorf("--meta keys = %q, want %q", metaKeys, want)
	}
	meta := strings.Join(flagValues("--meta"), "\n")
	for _, frag := range []string{"pgrouter.bead=zr-w", "pgrouter.event_id=e-1", "pgrouter.pool=pg-router", "pgrouter.role=worker"} {
		if !strings.Contains(meta, frag) {
			t.Errorf("--meta lacks %q:\n%s", frag, meta)
		}
	}
	if got := strings.Join(flagValues("--label"), ","); got != "pgrouter.pool,pgrouter.role" {
		t.Errorf("--label = %q, want pgrouter.pool,pgrouter.role", got)
	}
}

// brokenPipeBareEnv selects the control child (see TestMain's helper hook).
const brokenPipeBareEnv = "PG_ROUTER_HANDLER_CCPOOL_TEST_BARE_WRITE"

// runBareWrite is the control child: a reply write with no SIGPIPE handling.
func runBareWrite() int {
	_, _ = io.ReadAll(os.Stdin)
	wire.WriteReply(os.Stdout, map[string]any{"ok": true})
	return 0
}
