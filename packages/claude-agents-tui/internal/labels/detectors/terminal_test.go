package detectors

import (
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/labels"
)

func TestTerminal_Cmux(t *testing.T) {
	d := Terminal{}
	s := labels.Session{Env: map[string]string{"CMUX_WORKSPACE_ID": "ws1"}}
	got := d.Detect(s)
	if got["workspace.terminal"] != "cmux" {
		t.Errorf("got %+v", got)
	}
}

func TestTerminal_Tmux(t *testing.T) {
	d := Terminal{}
	s := labels.Session{Env: map[string]string{"TMUX": "/tmp/tmux-501/default,1234,0"}}
	got := d.Detect(s)
	if got["workspace.terminal"] != "tmux" {
		t.Errorf("got %+v", got)
	}
}

func TestTerminal_CmuxBeatsTmux(t *testing.T) {
	d := Terminal{}
	s := labels.Session{Env: map[string]string{
		"CMUX_WORKSPACE_ID": "ws1",
		"TMUX":              "/tmp/tmux-501/default,1234,0",
	}}
	got := d.Detect(s)
	if got["workspace.terminal"] != "cmux" {
		t.Errorf("cmux should win, got %+v", got)
	}
}

func TestTerminal_Direct(t *testing.T) {
	d := Terminal{}
	s := labels.Session{Env: map[string]string{}}
	got := d.Detect(s)
	if got["workspace.terminal"] != "direct" {
		t.Errorf("got %+v", got)
	}
}
