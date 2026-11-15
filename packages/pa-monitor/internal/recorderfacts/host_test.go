package recorderfacts

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// TestRegistrationsCarryTheRecordersHostFactAndLabel: each Process's Host fact
// (read by the recorder from the process environment) reaches the Session, and
// session.HostLabel turns it into the terminal-host label with no process
// inspection by pa-monitor.
func TestRegistrationsCarryTheRecordersHostFactAndLabel(t *testing.T) {
	e := clienttest.Start(t)
	cases := []struct {
		pid  int
		sid  string
		env  map[string]string
		want string
	}{
		{601, "host-cmux", map[string]string{"CMUX_SURFACE_ID": "surf", "CMUX_WORKSPACE_ID": "ws"}, "cmux"},
		{602, "host-tmux", map[string]string{"TMUX": "/tmp/tmux-1/default,1,0", "TMUX_PANE": "%1"}, "tmux"},
		{603, "host-ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, "ghostty"},
		{604, "host-vscode", map[string]string{"TERM_PROGRAM": "VSCode"}, "vscode"},
		{605, "host-other", map[string]string{"TERM_PROGRAM": "example-term"}, "unknown"},
		{606, "host-bare", map[string]string{}, "unknown"},
	}
	for _, c := range cases {
		e.Procs.Add(c.pid, time.UnixMilli(startMs), c.env)
		writeRegistry(t, e, c.pid, c.sid, "busy", "")
	}
	f := New(client.Options{Socket: e.Socket})
	got, err := f.Registrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		s := byID(got, c.sid)
		if s == nil {
			t.Fatalf("%s: session missing", c.sid)
		}
		if label := session.HostLabel(s.Host); label != c.want {
			t.Errorf("%s: label = %q (host %+v), want %q", c.sid, label, s.Host, c.want)
		}
	}
	if h := byID(got, "host-cmux").Host; h == nil || h.Kind != "cmux" {
		t.Errorf("cmux Host = %+v", h)
	}
	if h := byID(got, "host-tmux").Host; h == nil || h.Kind != "tmux" {
		t.Errorf("tmux Host = %+v", h)
	}
	_ = fmt.Sprint
}
