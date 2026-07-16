package detectors

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
)

func TestProject_WorkspaceFallback(t *testing.T) {
	d := Project{}
	s := labels.Session{Env: map[string]string{"WORKSPACE": "ws1"}}
	if got := d.Detect(s); got["workspace.project"] != "ws1" {
		t.Errorf("got %+v", got)
	}
}

func TestProject_NoneOmitted(t *testing.T) {
	d := Project{}
	s := labels.Session{Env: map[string]string{}}
	if got := d.Detect(s); len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}
