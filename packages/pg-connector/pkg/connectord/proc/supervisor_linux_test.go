//go:build linux

package proc

import (
	"strings"
	"testing"
)

func TestLinuxSupervisorProbeReportsNone(t *testing.T) {
	if got := SupervisorProbe("example.service"); got != SupervisorNone {
		t.Errorf("SupervisorProbe = %v, want none until pg2-opk5g", got)
	}
}

func TestLinuxRestartHintIsABackgroundedNohupCommand(t *testing.T) {
	got := RestartHint("example", "/etc/example/config.json")
	want := "nohup pg-connector-github --config /etc/example/config.json serve >/dev/null 2>&1 &"
	if !strings.Contains(got, want) {
		t.Errorf("RestartHint = %q, want it to contain %q", got, want)
	}
	if !strings.HasSuffix(got, "&") {
		t.Errorf("the hint must end in & so it never blocks a shell: %q", got)
	}
	if q := RestartHint("example", "/has space/c.json"); !strings.Contains(q, "'/has space/c.json'") {
		t.Errorf("a config path with a space must be quoted: %q", q)
	}
}
