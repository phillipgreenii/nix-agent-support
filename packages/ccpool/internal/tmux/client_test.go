package tmux

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func newFake(t *testing.T) (*Client, *cmdruntest.Fake) {
	t.Helper()
	f := cmdruntest.New(t)
	return newClient("ccpool-test", f.Runner()), f
}

func TestNewSession_sortedEnvRemainOnExitAndUserConf(t *testing.T) {
	c, f := newFake(t)
	f.On(cmdruntest.Any)
	if err := c.NewSession("cc-a", "/example/cwd", map[string]string{"Z": "1", "A": "2"}, []string{"claude", "--x"}); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want one tmux invocation", len(calls))
	}
	want := []string{
		"-L", "ccpool-test", // no -f: the user's own tmux configuration is read
		"new-session", "-d", "-s", "cc-a", "-c", "/example/cwd",
		"-e", "A=2", "-e", "Z=1", "--", "claude", "--x",
		";", "set-option", "-t", "=cc-a:", "remain-on-exit", "failed",
	}
	if !slices.Equal(calls[0].Args, want) {
		t.Fatalf("args = %q, want %q", calls[0].Args, want)
	}
}

func TestNewSession_errorPropagates(t *testing.T) {
	c, f := newFake(t)
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "duplicate session: cc-a"))
	if err := c.NewSession("cc-a", "", nil, []string{"sh"}); err == nil {
		t.Fatal("want error")
	}
}

func TestHasSession_emptyTargetNeverLive(t *testing.T) {
	c, f := newFake(t)
	f.On(cmdruntest.Any)
	if c.HasSession("") {
		t.Fatal("empty target must never be live")
	}
	if n := len(f.Calls()); n != 0 {
		t.Fatalf("tmux called %d times for an empty target", n)
	}
}

func TestHasSession_liveDeadAndMissing(t *testing.T) {
	for _, tc := range []struct {
		name string
		seq  []cmdrun.Result
		want bool
	}{
		{"live", []cmdrun.Result{cmdruntest.Out(""), cmdruntest.Out("0\n")}, true},
		{"dead pane is not live", []cmdrun.Result{cmdruntest.Out(""), cmdruntest.Out("1\n")}, false},
		{"missing session", []cmdrun.Result{cmdruntest.Fail(1, "can't find session: cc-a")}, false},
		{"no server", []cmdrun.Result{cmdruntest.Fail(1, "no server running on /tmp/x")}, false},
		{"other error", []cmdrun.Result{cmdruntest.Fail(1, "boom")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := newFake(t)
			f.On(cmdruntest.Any).Seq(tc.seq...)
			if got := c.HasSession("cc-a"); got != tc.want {
				t.Fatalf("HasSession = %v, want %v", got, tc.want)
			}
			// has-session gates the pane query: display-message never runs
			// for a session that does not exist.
			if first := f.Calls()[0].Args; !slices.Contains(first, "has-session") {
				t.Fatalf("first call = %q, want has-session", first)
			}
		})
	}
}

func TestPaneStatus_emptyNameIsNoSession(t *testing.T) {
	c, f := newFake(t)
	f.On(cmdruntest.Any)
	ps, err := c.PaneStatus("")
	if err != nil || ps != (PaneStatus{}) || len(f.Calls()) != 0 {
		t.Fatalf("PaneStatus = %+v, %v, calls %d", ps, err, len(f.Calls()))
	}
}

func TestKillSession_goneIsNotAnError(t *testing.T) {
	c, f := newFake(t)
	f.On(cmdruntest.Any).Return(cmdruntest.Fail(1, "can't find session: cc-a"))
	if err := c.KillSession("cc-a"); err != nil {
		t.Fatal(err)
	}
}

func TestListPanes_mapsActivePane(t *testing.T) {
	c, f := newFake(t)
	f.On(cmdruntest.Any).Return(cmdruntest.Out("cc-a:1:1:0:/p/a\ncc-b:1:1:1:/p/b\n"))
	got, err := c.ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	if got["cc-a"] != (PaneInfo{Live: true, Path: "/p/a"}) || got["cc-b"] != (PaneInfo{Path: "/p/b"}) {
		t.Fatalf("ListPanes = %+v", got)
	}
}

func TestPackageHelpers_noServerNotLive(t *testing.T) {
	if HasSession("ccpool-no-such-socket-for-test", "cc-anything") {
		t.Fatal("HasSession true with no server running")
	}
}
