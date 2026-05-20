package detectors

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
)

func TestAgent_KindFromModel(t *testing.T) {
	d := Agent{}
	s := labels.Session{Model: "claude-opus-4-7"}
	if got := d.Detect(s); got["agent.kind"] != "claude" {
		t.Errorf("got %+v", got)
	}
}

func TestAgent_Codex(t *testing.T) {
	d := Agent{}
	s := labels.Session{Model: "codex-foo"}
	if got := d.Detect(s); got["agent.kind"] != "codex" {
		t.Errorf("got %+v", got)
	}
}

func TestAgent_UnknownModel(t *testing.T) {
	d := Agent{}
	s := labels.Session{Model: "gpt-x"}
	got := d.Detect(s)
	if got["agent.kind"] != "" {
		t.Errorf("unknown should be empty: %+v", got)
	}
}
