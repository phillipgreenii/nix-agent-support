package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dispatchlib"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/handlerstate"
)

// defaultEvent is a dispatch event for bead zr-w.
const defaultEvent = `{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}`

// workerRole is a minimal ccpool role: the name and the [role.config] a `worker`
// role carries. extraCC is spliced into the ccpool table, extraTop into the top
// level of the config.
func workerRole(extraTop, extraCC string) (name string, cfg map[string]any) {
	body := `{"ccpool":{"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello","isolation":{"Type":"none"}` + extraCC + `}` + extraTop + `}`
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		panic(err)
	}
	return "worker", m
}

// request builds a wire request body: a dispatch (event non-empty) or any other
// request, plus the role object. A nil cfg omits the config member, an empty
// name omits the role member altogether.
func request(event, name string, cfg map[string]any) string {
	body := map[string]any{"schemaVersion": "1", "id": "d-1"}
	if event != "" {
		body["event"] = json.RawMessage(event)
	}
	if name != "" {
		role := map[string]any{"name": name}
		if cfg != nil {
			role["config"] = cfg
		}
		body["role"] = role
	}
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// dispatchWith runs serveDispatch (real dispatchlib.BuildDeps: a real ccpool
// CLIRunner and bd CLIRunner shelling out to whatever is on PATH) for the role
// and returns the exit code and the reply.
func dispatchWith(t *testing.T, name string, cfg map[string]any, cfgPath, event string) (int, string) {
	t.Helper()
	return dispatchCtx(context.Background(), t, name, cfg, cfgPath, event)
}

func dispatchCtx(ctx context.Context, t *testing.T, name string, cfg map[string]any, cfgPath, event string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := serveDispatch(ctx, strings.NewReader(request(event, name, cfg)), &out, cfgPath, dispatchlib.BuildDeps)
	return code, out.String()
}

// stubBin writes executable shell scripts (name -> body after the shebang) into
// a new directory and returns it.
func stubBin(t *testing.T, scripts map[string]string) string {
	t.Helper()
	bin := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

// launchConfig writes a launch config JSON whose handler state directory is a
// temp dir (so the event log is the test's own) and returns its path and that
// state dir. extra is spliced into the top-level object.
func launchConfig(t *testing.T, extra string) (cfgPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	stateDir = filepath.Join(dir, "state")
	cfgPath = filepath.Join(dir, "launch.json")
	body := `{"originProbe":{"stateDir":"` + stateDir + `"}` + extra + `}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, stateDir
}

// eventRecords returns the handler event log's records of the given kind.
func eventRecords(t *testing.T, stateDir, kind string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, handlerstate.EventLogName))
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec map[string]any
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["kind"] == kind {
			out = append(out, rec)
		}
	}
	return out
}

func decodeReply(t *testing.T, reply string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(reply), &m); err != nil {
		t.Fatalf("reply %q is not a JSON object: %v", reply, err)
	}
	return m
}
