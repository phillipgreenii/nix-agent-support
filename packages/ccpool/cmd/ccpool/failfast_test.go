package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/tmux"
)

type fakePanes struct {
	status    tmux.PaneStatus
	statusErr error
	tail      string
	tailErr   error
	asked     string
}

func (f *fakePanes) PaneStatus(name string) (tmux.PaneStatus, error) {
	f.asked = name
	return f.status, f.statusErr
}

func (f *fakePanes) CaptureTail(string, int) (string, error) { return f.tail, f.tailErr }

func TestLaunchLiveness(t *testing.T) {
	cases := []struct {
		name    string
		panes   fakePanes
		wantErr []string // substrings; nil means no error
	}{
		{name: "alive", panes: fakePanes{status: tmux.PaneStatus{Exists: true}}},
		{name: "tmux read error is not evidence of death", panes: fakePanes{statusErr: errors.New("boom")}},
		{
			name:    "absent session exited cleanly",
			panes:   fakePanes{},
			wantErr: []string{"exited cleanly during launch"},
		},
		{
			name:    "exit status with tail",
			panes:   fakePanes{status: tmux.PaneStatus{Exists: true, Dead: true, Status: 1}, tail: "boom on stderr"},
			wantErr: []string{"exited with status 1", "output tail:", "boom on stderr"},
		},
		{
			name:    "signal wins over status",
			panes:   fakePanes{status: tmux.PaneStatus{Exists: true, Dead: true, Signal: 15}},
			wantErr: []string{"killed by signal 15"},
		},
		{
			name:    "dead pane with no output has no tail section",
			panes:   fakePanes{status: tmux.PaneStatus{Exists: true, Dead: true, Status: 2}},
			wantErr: []string{"exited with status 2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := launchLiveness(&tc.panes, "k19", "cc-k19")
			if tc.panes.asked != "cc-k19" {
				t.Errorf("probed %q, want the tmux name cc-k19", tc.panes.asked)
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			if tc.name == "dead pane with no output has no tail section" && strings.Contains(err.Error(), "output tail") {
				t.Errorf("error %q must not carry an empty tail", err)
			}
		})
	}
}

func TestPaneTail(t *testing.T) {
	if got := paneTail(&fakePanes{tail: "  \n"}, "x"); got != "" {
		t.Errorf("blank tail = %q, want empty", got)
	}
	if got := paneTail(&fakePanes{tailErr: errors.New("x")}, "x"); got != "" {
		t.Errorf("unreadable tail = %q, want empty", got)
	}
	if got := paneTail(&fakePanes{tail: "hi"}, "x"); got != "output tail:\nhi" {
		t.Errorf("tail = %q", got)
	}
}
