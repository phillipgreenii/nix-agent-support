package detectors

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/labels"
)

func TestDefaultScope_AlwaysSetsPersonal(t *testing.T) {
	d := DefaultScope{}
	got := d.Detect(labels.Session{})
	if got["workspace.scope"] != "personal" {
		t.Errorf("workspace.scope = %q, want personal", got["workspace.scope"])
	}
}
