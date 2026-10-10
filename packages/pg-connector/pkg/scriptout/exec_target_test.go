package scriptout

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// recordingFactory wraps helperCmdFactory(behavior) and records the
// (name, args) the exec layer was asked to run.
func recordingFactory(t *testing.T, behavior string, gotName *string, gotArgs *[]string) {
	t.Helper()
	inner := helperCmdFactory(behavior)
	orig := execCmdFactory
	execCmdFactory = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		*gotName = name
		*gotArgs = append([]string(nil), args...)
		return inner(ctx, name, args...)
	}
	t.Cleanup(func() { execCmdFactory = orig })
}

func TestInvokeTarget_PassesCommandArgsBeforeStdin(t *testing.T) {
	var name string
	var args []string
	recordingFactory(t, "echo_request", &name, &args)

	tgt := Target{Name: "x-pg2", Command: []string{"bin", "--beads-dir", "/d"}}
	resp, err := InvokeTarget(context.Background(), tgt, "list", map[string]any{"n": 1}, json.RawMessage(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("InvokeTarget: %v", err)
	}
	if name != "bin" {
		t.Errorf("exec name = %q, want bin", name)
	}
	if want := []string{"--beads-dir", "/d"}; !reflect.DeepEqual(args, want) {
		t.Errorf("exec args = %v, want %v", args, want)
	}
	// The wire request is unchanged: exactly op/args/config, no instance
	// name and no extra field.
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(resp.Result, &wire); err != nil {
		t.Fatalf("decode echoed request: %v", err)
	}
	for k := range wire {
		if k != "op" && k != "args" && k != "config" {
			t.Errorf("unexpected request field %q", k)
		}
	}
	if string(wire["op"]) != `"list"` || string(wire["config"]) != `{"k":"v"}` {
		t.Errorf("request = %v", wire)
	}
}

func TestInvoke_PlainBinary_NoArgs(t *testing.T) {
	var name string
	args := []string{"sentinel"}
	recordingFactory(t, "ok_auth", &name, &args)

	if _, err := Invoke(context.Background(), "fake-binary", OpAuthStatus, nil, nil); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if name != "fake-binary" || len(args) != 0 {
		t.Errorf("got (%q, %v), want (fake-binary, no args)", name, args)
	}
}

func TestInvokeTarget_ErrorsCarryInstanceName(t *testing.T) {
	withFactory(t, "not_found")
	tgt := Target{Name: "x-zr", Command: []string{"real-binary", "--beads-dir", "/d"}}
	_, err := InvokeTarget(context.Background(), tgt, "get", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "x-zr:") || strings.Contains(err.Error(), "real-binary") {
		t.Errorf("error should name the instance, not the binary: %v", err)
	}

	withFactory(t, "stderr_only")
	_, err = InvokeTarget(context.Background(), tgt, "get", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "scriptout: x-zr:") {
		t.Errorf("exec failure should name the instance: %v", err)
	}
}

func TestInvokeTarget_EmptyCommand(t *testing.T) {
	for _, cmd := range [][]string{nil, {}, {""}} {
		_, err := InvokeTarget(context.Background(), Target{Name: "x", Command: cmd}, "op", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "empty backend binary") {
			t.Errorf("Command %q: expected empty-binary error, got %v", cmd, err)
		}
		_, err = InvokeCapabilitiesTarget(context.Background(), Target{Name: "x", Command: cmd})
		if err == nil || !strings.Contains(err.Error(), "empty backend binary") {
			t.Errorf("capabilities, Command %q: expected empty-binary error, got %v", cmd, err)
		}
	}
}

func TestInvokeCapabilitiesTarget_PassesArgs(t *testing.T) {
	var name string
	var args []string
	recordingFactory(t, "capabilities", &name, &args)

	tgt := Target{Name: "x-pg2", Command: []string{"bin", "--beads-dir", "/d"}}
	if _, err := InvokeCapabilitiesTarget(context.Background(), tgt); err != nil {
		t.Fatalf("InvokeCapabilitiesTarget: %v", err)
	}
	if name != "bin" || !reflect.DeepEqual(args, []string{"--beads-dir", "/d"}) {
		t.Errorf("got (%q, %v)", name, args)
	}
}
