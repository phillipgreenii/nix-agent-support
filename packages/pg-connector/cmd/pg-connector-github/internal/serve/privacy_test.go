package serve_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Identity and privacy: tokens stay with gh. A fixture token the shim hands out
// (and the only one it accepts) never reaches the store, the event log or the
// structured log, whatever the daemon went through. The viewer-change half of
// this row is TestErrorTable_ViewerChanged_FlushesIdentityScopedQueriesAndLogsIt.

// filesWith returns the files under dir (recursively) that contain needle.
func filesWith(t *testing.T, dir string, needle []byte) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(raw, needle) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func TestPrivacy_AFixtureTokenNeverAppearsInTheStoreOrTheLogs(t *testing.T) {
	const token = "tok-fixture-9f3a1c77e2b84d05a6c1"
	d := servetest.NewDaemon(t, servetest.Options{Token: token, Before: seed, Hooks: "viewer_interval=300ms"})
	d.Start(t)

	// A workload that touches every path that could carry the token: successful
	// fetches, the viewer, local ops, and each kind of origin failure.
	mustList(t, d, servetest.QueryMine, nil)
	mustList(t, d, servetest.QueryTeam, map[string]any{"ids_only": true})
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second, "search", "nodes")
	for _, req := range []scriptout.Request{
		{Op: scriptout.OpAuthStatus},
		{Op: "status"},
		{Op: "explain", Args: []byte(`{"id":"` + prA.Ref() + `"}`)},
	} {
		if resp := d.MustCall(t, "pr", req, 10*time.Second); resp.Error != nil {
			t.Fatalf("%s: %+v", req.Op, resp.Error)
		}
	}
	for _, mode := range []string{"502", "secondary", "401", "ok"} {
		d.Shim.Mode(mode)
		list(t, d, servetest.QueryMine, 20*time.Second, map[string]any{"fresh": true})
		time.Sleep(700 * time.Millisecond) // a viewer refresh or two under this mode
	}
	if d.Shim.CountKind("auth_token") == 0 {
		t.Fatal("fixture: the token was never requested, so this test proves nothing")
	}
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}

	// Everything the daemon wrote: the store (with its WAL and any sidecar), the
	// event log, the structured log and their rotations, and what it printed.
	if hits := filesWith(t, d.StateDir, []byte(token)); len(hits) != 0 {
		t.Errorf("the token appears in %v", hits)
	}
	if strings.Contains(d.Stderr(), token) {
		t.Errorf("the token appears in the daemon's output: %s", d.Stderr())
	}
	for _, f := range []string{d.StorePath(), filepath.Join(d.StateDir, "events.jsonl"), filepath.Join(d.StateDir, "daemon.log.jsonl")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("fixture: %s was not written: %v", f, err)
		}
	}

	// The scan can find a token: plant one and see it found.
	planted := filepath.Join(d.StateDir, "planted.txt")
	if err := os.WriteFile(planted, []byte("x"+token+"y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hits := filesWith(t, d.StateDir, []byte(token)); len(hits) != 1 || hits[0] != planted {
		t.Errorf("control: the scan must find the planted token, got %v", hits)
	}
}
