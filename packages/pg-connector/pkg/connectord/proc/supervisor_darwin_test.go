//go:build darwin

package proc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// stubLaunchctl replaces launchctl for one test and records its arguments.
func stubLaunchctl(t *testing.T, out string, ran, ok bool) *[]string {
	t.Helper()
	var got []string
	old := launchctlRun
	launchctlRun = func(_ context.Context, args ...string) (string, bool, bool) {
		got = args
		return out, ran, ok
	}
	t.Cleanup(func() { launchctlRun = old })
	return &got
}

func TestDarwinSupervisorProbeAsksLaunchctlPrintForTheGUIDomainOfTheUID(t *testing.T) {
	args := stubLaunchctl(t, "state = running\n", true, true)
	SupervisorProbe("com.example.daemon")
	want := []string{"print", fmt.Sprintf("gui/%d/com.example.daemon", os.Getuid())}
	if strings.Join(*args, " ") != strings.Join(want, " ") {
		t.Errorf("launchctl args = %v, want %v", *args, want)
	}
}

func TestDarwinSupervisorProbeStates(t *testing.T) {
	running := "com.example.daemon = {\n\tactive count = 1\n\tstate = running\n}\n"
	waiting := "com.example.daemon = {\n\tactive count = 0\n\tstate = waiting\n}\n"
	for _, tc := range []struct {
		name    string
		out     string
		ran, ok bool
		want    SupervisorState
	}{
		{"running", running, true, true, SupervisorRunning},
		{"registered not running", waiting, true, true, SupervisorRegistered},
		{"not registered", "Could not find service", true, false, SupervisorNotRegistered},
		{"launchctl missing", "", false, false, SupervisorUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubLaunchctl(t, tc.out, tc.ran, tc.ok)
			if got := SupervisorProbe("com.example.daemon"); got != tc.want {
				t.Errorf("SupervisorProbe = %v, want %v", got, tc.want)
			}
		})
	}
}

// The real launchctl answers "no such job" for a label nothing registered.
// Inside the nix build sandbox launchctl cannot reach launchd, so it is skipped
// there (the stubbed tests above still run).
func TestDarwinSupervisorProbeRealLaunchctlForUnknownLabel(t *testing.T) {
	if os.Getenv("NIX_BUILD_TOP") != "" {
		t.Skip("launchctl is not usable inside the nix build sandbox")
	}
	if got := SupervisorProbe("com.example.pg-connector-test-no-such-job"); got != SupervisorNotRegistered {
		t.Errorf("real launchctl: SupervisorProbe = %v, want not_registered", got)
	}
}

func TestDarwinRestartHintIsLaunchctlKickstart(t *testing.T) {
	got := RestartHint("com.example.daemon", "/etc/example/config.json")
	want := fmt.Sprintf("launchctl kickstart -k gui/%d/com.example.daemon", os.Getuid())
	if !strings.Contains(got, want) {
		t.Errorf("RestartHint = %q, want it to contain %q", got, want)
	}
}
