//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Real-process counterparts of the in-process fan-out tests in fanout_test.go.
// The unit tier proves the fan-out contract against in-process fakes; these
// run the same behaviours against genuine /bin/sh backend processes that the
// umbrella spawns through the real command runner, so the process boundary
// (stdin pipe, stdout capture, concurrent exec) is covered too. Each script
// coordinates through marker files in a shared directory, never through
// wall-clock margins. They run in integration-checks.pg-connector-integration-tests.

// realProcessBackend installs a /bin/sh backend named name on PATH. body runs
// after the request on stdin has been drained; dir is the shared marker
// directory.
func realProcessBackend(t *testing.T, binDir, name, body string) {
	t.Helper()
	script := "#!/bin/sh\ncat >/dev/null\n" + body
	if err := installExec(filepath.Join(binDir, name), script); err != nil {
		t.Fatalf("install %s: %v", name, err)
	}
}

// waitForShell is a shell snippet that polls (5 ms steps, about 10 s) until
// every file named in $@ exists in $DIR and sets ok=1, else ok=0.
const waitForShell = `waitfor() {
  i=0
  while [ $i -lt 2000 ]; do
    all=1
    for f in "$@"; do [ -e "$DIR/$f" ] || all=0; done
    if [ $all = 1 ]; then ok=1; return; fi
    i=$((i+1)); sleep 0.005
  done
  ok=0
}
`

func shellItem(id string) string { return activityResp("false", id) }

func realProcessSetup(t *testing.T) (binDir, dir string) {
	t.Helper()
	binDir = t.TempDir()
	dir = t.TempDir()
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir, dir
}

// All three sources must be inside their call at the same moment: each waits
// for every other's start marker, so a serial fan-out answers "-serial".
func TestFanOutRealProcess_GatedRendezvous(t *testing.T) {
	binDir, dir := realProcessSetup(t)
	names := []string{"rp-par-a", "rp-par-b", "rp-par-c"}
	var started []string
	for _, n := range names {
		started = append(started, n+".started")
	}
	for _, n := range names {
		realProcessBackend(t, binDir, n, "DIR='"+dir+"'\n"+waitForShell+
			": > \"$DIR/"+n+".started\"\nwaitfor "+strings.Join(started, " ")+"\n"+
			"if [ $ok = 1 ]; then printf '%s\\n' '"+shellItem(n+"-ok")+"'; else printf '%s\\n' '"+shellItem(n+"-serial")+"'; fi\n")
	}
	writeActivityListConfig(t, names, nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	if got, want := activityItemIDs(decodeActivityOut(t, stdout)), "rp-par-a/rp-par-a-ok,rp-par-b/rp-par-b-ok,rp-par-c/rp-par-c-ok"; got != want {
		t.Fatalf("items = %s, want %s (every source concurrent, registration order)", got, want)
	}
}

// The slow source is registered first and answers only after all others are
// done; a serial fan-out would leave it waiting on sources queued behind it.
func TestFanOutRealProcess_SlowBackendDoesNotDelayOthers(t *testing.T) {
	binDir, dir := realProcessSetup(t)
	others := []string{"rp-slow-b", "rp-slow-c", "rp-slow-d"}
	var done []string
	for _, n := range others {
		done = append(done, n+".done")
		realProcessBackend(t, binDir, n, "DIR='"+dir+"'\nprintf '%s\\n' '"+shellItem(n)+"'\n: > \"$DIR/"+n+".done\"\n")
	}
	realProcessBackend(t, binDir, "rp-slow-a", "DIR='"+dir+"'\n"+waitForShell+
		"waitfor "+strings.Join(done, " ")+"\n"+
		"if [ $ok = 1 ]; then printf '%s\\n' '"+shellItem("rp-slow-a-ok")+"'; else printf '%s\\n' '"+shellItem("rp-slow-a-serial")+"'; fi\n")
	writeActivityListConfig(t, append([]string{"rp-slow-a"}, others...), nil)

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	want := "rp-slow-a/rp-slow-a-ok,rp-slow-b/rp-slow-b,rp-slow-c/rp-slow-c,rp-slow-d/rp-slow-d"
	if got := activityItemIDs(decodeActivityOut(t, stdout)); got != want {
		t.Fatalf("items = %s, want %s", got, want)
	}
}

// With fanout_concurrency = cap and more sources than the cap, the peak number
// of simultaneously running backend processes equals the cap: each process
// registers itself, records how many are registered, holds until the cap is
// reached (or, when the cap is below the source count, briefly), then leaves.
func TestFanOutRealProcess_ConcurrencyCap(t *testing.T) {
	for _, cap := range []int{1, 2} {
		t.Run("cap"+strconv.Itoa(cap), func(t *testing.T) {
			binDir, dir := realProcessSetup(t)
			names := []string{"rp-cap-a", "rp-cap-b", "rp-cap-c", "rp-cap-d"}
			for _, n := range names {
				// count = processes currently registered; peaks.log keeps every count seen.
				realProcessBackend(t, binDir, n, "DIR='"+dir+"'\nmkdir -p \"$DIR/in\"\n: > \"$DIR/in/"+n+"\"\n"+
					"c=$(ls \"$DIR/in\" | wc -l)\necho $c >> \"$DIR/peaks.log\"\nsleep 0.3\nrm -f \"$DIR/in/"+n+"\"\n"+
					"printf '%s\\n' '"+shellItem(n)+"'\n")
			}
			writeActivityConfigWithState(t, names, "fanout_concurrency: "+strconv.Itoa(cap))

			stdout, _, code := executePr(t, []string{"activity", "list", "--since", "24h"})
			if code != 0 {
				t.Fatalf("exit = %d; stdout=%s", code, stdout)
			}
			data, err := os.ReadFile(filepath.Join(dir, "peaks.log"))
			if err != nil {
				t.Fatalf("read peaks: %v", err)
			}
			peak := 0
			for _, l := range strings.Fields(string(data)) {
				n, _ := strconv.Atoi(l)
				peak = max(peak, n)
			}
			if peak != cap {
				t.Fatalf("peak concurrent backend processes = %d, want exactly the cap %d (log %q)", peak, cap, strings.Fields(string(data)))
			}
			want := "rp-cap-a/rp-cap-a,rp-cap-b/rp-cap-b,rp-cap-c/rp-cap-c,rp-cap-d/rp-cap-d"
			if got := activityItemIDs(decodeActivityOut(t, stdout)); got != want {
				t.Fatalf("items = %s, want %s", got, want)
			}
		})
	}
}
