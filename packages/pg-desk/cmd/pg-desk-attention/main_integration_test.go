//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Integration tier: every test here runs the plugin as its own process (the
// test binary re-executed under the plugin name), so none belongs in the unit
// tier. Run through checks.<system>.pg-desk-integration-tests.

// pluginName is the bare name the umbrella registry uses for this plugin.
const pluginName = "pg-desk-attention"

// TestMain lets the test binary double as the plugin: when it is started
// through a path whose base name is pluginName (see buildPlugin) it runs the
// production entry point, run, exactly as main does. This is the repo's
// reentrant test-helper-process shape; it keeps pg-desk's composition
// chokepoint (cmd/pg-desk/composition_test.go) satisfied, because no test here
// names a binary to exec as a literal.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == pluginName {
		os.Exit(run())
	}
	os.Exit(m.Run())
}

// buildPlugin installs the plugin under its bare registry name in dir, as a
// symlink to this test binary (see TestMain), and returns its path. The
// process it starts runs the same run() as the shipped binary: real config
// loading, a real read-only store, the real dispatch table and ServeLoop. The
// shipped, linked binary is built and smoke-tested by the nix packaging.
func buildPlugin(t *testing.T, dir string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, pluginName)
	if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}
	return bin
}

// writePluginEnv writes the pg-desk config the plugin reads and returns the
// environment that points the real binary at it and at the store.
func writePluginEnv(t *testing.T, storePath string) []string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "pg-desk.yaml")
	if err := os.WriteFile(cfgPath, []byte("self_login: me\nrepos:\n  - remote: "+fixtureRepo+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// store.DefaultPath() is $XDG_STATE_HOME/pg-desk/store.db, and
	// fixtureStore wrote exactly that shape.
	return []string{
		"PG_DESK_CONFIG=" + cfgPath,
		"XDG_STATE_HOME=" + filepath.Dir(filepath.Dir(storePath)),
		"HOME=" + dir,
	}
}

// TestConformance_RealBinary runs the shared suite against the plugin
// process (see buildPlugin), driven through the real config and store seams.
func TestConformance_RealBinary(t *testing.T) {
	bin := buildPlugin(t, t.TempDir())
	env := writePluginEnv(t, standardFixture(t))
	requireConformant(t, envBackend{env: env, bin: bin})
}

// envBackend is a conformance.Backend that runs the plugin process with an
// explicit environment (conformance.ExecBackend inherits the test process's).
type envBackend struct {
	env []string
	bin string
}

func (b envBackend) Invoke(ctx context.Context, request []byte) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, b.bin)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, b.env...)
	cmd.Stdin = bytes.NewReader(request)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), 0, nil
	case errors.As(err, &ee):
		return out.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), -1, fmt.Errorf("exec %s: %w (stderr: %s)", b.bin, err, bytes.TrimSpace(stderr.Bytes()))
}

// TestRealBinary_ListAttentionWireShape checks the list_attention response of
// the plugin process: schemaVersion 3, items in evaluator order, group
// passed in the v3 shape.
func TestRealBinary_ListAttentionWireShape(t *testing.T) {
	path := standardFixture(t)
	bin := buildPlugin(t, t.TempDir())
	b := envBackend{env: writePluginEnv(t, path), bin: bin}
	out, code, err := b.Invoke(context.Background(), []byte(`{"op":"list_attention"}`))
	if err != nil || code != 0 {
		t.Fatalf("exit=%d err=%v out=%s", code, err, out)
	}
	var resp struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Result        []schema.AttentionItem `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if resp.SchemaVersion != schema.AttentionSchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", resp.SchemaVersion, schema.AttentionSchemaVersion)
	}
	want := evaluate(t, path)
	if len(resp.Result) != len(want.Items) {
		t.Fatalf("got %d items, want %d: %s", len(resp.Result), len(want.Items), out)
	}
	for i, w := range want.Items {
		if resp.Result[i].ID != w.ID || resp.Result[i].Group == nil || resp.Result[i].Group.Key != w.Group {
			t.Errorf("item %d = %+v, want id %q group %q", i, resp.Result[i], w.ID, w.Group)
		}
	}
}

// TestRealBinary_UnreadableStoreIsAnErrorResponse proves the failure path over
// the wire: a missing store answers an unavailable error, not an empty list.
func TestRealBinary_UnreadableStoreIsAnErrorResponse(t *testing.T) {
	bin := buildPlugin(t, t.TempDir())
	env := writePluginEnv(t, filepath.Join(t.TempDir(), "never-created", "pg-desk", "store.db"))
	b := envBackend{env: env, bin: bin}
	out, code, err := b.Invoke(context.Background(), []byte(`{"op":"list_attention"}`))
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Errorf("exit = 0, want the taxonomy code for unavailable; out=%s", out)
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || resp.Error.Code != "unavailable" {
		t.Errorf("response = %s (err %v), want an error with code unavailable", out, err)
	}
}

// TestRealBinary_ExecsNothing runs the plugin process with a PATH holding
// tripwire executables under every name pg-desk and the connector family could
// plausibly exec. Any invocation leaves a marker file, and none may appear.
func TestRealBinary_ExecsNothing(t *testing.T) {
	path := standardFixture(t)
	bin := buildPlugin(t, t.TempDir())

	trip := t.TempDir()
	marker := filepath.Join(t.TempDir(), "tripped")
	for _, name := range []string{"pg-connector", "git", "gh", "bd", "open", "sh", "bash", "jq", "curl", "pg-desk"} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> " + marker + "\nexit 99\n"
		if err := os.WriteFile(filepath.Join(trip, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(context.Background(), bin)
	cmd.Env = append([]string{"PATH=" + trip}, writePluginEnv(t, path)...)
	cmd.Stdin = strings.NewReader(`{"op":"list_attention"}`)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("plugin failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "pr") {
		t.Fatalf("plugin answered no items, so the tripwire run proves nothing: %s", out.String())
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Fatalf("the plugin exec'd a binary: %s", b)
	}
}
