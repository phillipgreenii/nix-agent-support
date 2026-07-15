//go:build hostile

// Sandbox-hostile decorator tests (bead pg2-ymi3l). These write a `#!/bin/sh`
// fake-decorator script to a temp dir and exec it through Detect/DetectOK,
// depending on a working shell and fast fork/exec within a hard-coded timeout.
// In the nix build sandbox the first shell fork/exec can exceed the 2s decorator
// timeout (observed: TestDecorator_RoundTripJSON timing out at 2.00s), making the
// gate flaky. They are gated behind the `hostile` build tag so the default
// `pa-monitor-go-tests` check (plain `go test ./...`) runs only the sandbox-safe
// path validation below; run the exec behaviour locally with
// `go test -tags hostile ./...`.
package labels

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecorator_RoundTripJSON(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-decorator")
	script := "#!/bin/sh\necho '{\"labels\":{\"workspace.scope\":\"zr\",\"agent.role\":\"reviewer\"}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDecoratorRaw("fake", bin, 2000)
	got := d.Detect(Session{ID: "s1"})
	if got["workspace.scope"] != "zr" || got["agent.role"] != "reviewer" {
		t.Errorf("got %+v", got)
	}
}

func TestDecorator_TimeoutReturnsNil(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "slow-decorator")
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDecoratorRaw("slow", bin, 100) // 100ms < 5s
	if got := d.Detect(Session{ID: "s1"}); got != nil {
		t.Errorf("expected nil on timeout, got %+v", got)
	}
}

func TestDecorator_NonZeroExitReturnsNil(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fail-decorator")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDecoratorRaw("fail", bin, 1000)
	if got := d.Detect(Session{ID: "s1"}); got != nil {
		t.Errorf("expected nil on non-zero exit, got %+v", got)
	}
}

// TestDecorator_DetectOK_SuccessReportsOK confirms DetectOK returns ok=true on
// a successful run — even when the decorator legitimately produces no labels.
// The label cache must treat a successful-empty result as cacheable (distinct
// from a failure, which must NOT be cached).
func TestDecorator_DetectOK_SuccessReportsOK(t *testing.T) {
	dir := t.TempDir()

	full := filepath.Join(dir, "full")
	if err := os.WriteFile(full, []byte("#!/bin/sh\necho '{\"labels\":{\"workspace.scope\":\"zr\"}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	set, ok := newDecoratorRaw("full", full, 2000).DetectOK(Session{ID: "s1"})
	if !ok {
		t.Fatal("expected ok=true on success")
	}
	if set["workspace.scope"] != "zr" {
		t.Errorf("got %+v", set)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("#!/bin/sh\necho '{\"labels\":{}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	set, ok = newDecoratorRaw("empty", empty, 2000).DetectOK(Session{ID: "s1"})
	if !ok {
		t.Fatal("expected ok=true on successful-empty result")
	}
	if len(set) != 0 {
		t.Errorf("expected empty labels, got %+v", set)
	}
}

// TestDecorator_DetectOK_FailureReportsNotOK confirms DetectOK returns
// ok=false on a transient failure (non-zero exit / timeout), so callers can
// decline to cache the (wrong) empty result and retry next tick.
func TestDecorator_DetectOK_FailureReportsNotOK(t *testing.T) {
	dir := t.TempDir()

	fail := filepath.Join(dir, "fail")
	if err := os.WriteFile(fail, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if set, ok := newDecoratorRaw("fail", fail, 1000).DetectOK(Session{ID: "s1"}); ok || set != nil {
		t.Errorf("expected (nil,false) on non-zero exit, got (%+v,%v)", set, ok)
	}

	slow := filepath.Join(dir, "slow")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if set, ok := newDecoratorRaw("slow", slow, 100).DetectOK(Session{ID: "s1"}); ok || set != nil {
		t.Errorf("expected (nil,false) on timeout, got (%+v,%v)", set, ok)
	}
}

// TestDecorator_PassesArgs confirms end-to-end that argv split from the config
// command reaches the child process (bead pg2-r1f1j.10). The fake decorator
// echoes its first two positional args back as labels.
func TestDecorator_PassesArgs(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "args-decorator")
	script := "#!/bin/sh\nprintf '{\"labels\":{\"a1\":\"%s\",\"a2\":\"%s\"}}' \"$1\" \"$2\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDecoratorRawArgv("args", []string{bin, "-rule", "scope"}, nil, 2000)
	got := d.Detect(Session{ID: "s1"})
	if got["a1"] != "-rule" || got["a2"] != "scope" {
		t.Errorf("args not forwarded, got %+v", got)
	}
}

// TestDecorator_ForwardsConfigEnv confirms end-to-end that a config-provided env
// var reaches the child (bead pg2-r1f1j.10). The fake decorator echoes the
// forwarded var back as a label; the base env alone would leave it empty.
func TestDecorator_ForwardsConfigEnv(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "env-decorator")
	script := "#!/bin/sh\nprintf '{\"labels\":{\"scope\":\"%s\"}}' \"$PA_MONITOR_SCOPE_RULES\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newDecoratorRawArgv("env", []string{bin}, map[string]string{"PA_MONITOR_SCOPE_RULES": "zr-rules"}, 2000)
	got := d.Detect(Session{ID: "s1"})
	if got["scope"] != "zr-rules" {
		t.Errorf("config env not forwarded, got %+v", got)
	}
}
