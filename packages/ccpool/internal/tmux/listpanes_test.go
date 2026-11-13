package tmux

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseListPanes_keepsOnlyTheActivePaneOfTheActiveWindow(t *testing.T) {
	out := "" +
		"cc-a:1:0:0:/old/pane\n" + // inactive pane of the active window
		"cc-a:1:1:0:/repo with space\n" + // the one display-message -t cc-a resolves
		"cc-a:0:1:1:/other/window\n" + // active pane of an inactive window
		"cc-b:1:1:1:\n" + // dead pane, empty path
		"cc-c:1:1:0:/p/with:colon\n" + // the path is the rest of the line
		"garbage\n" +
		"\n"
	want := map[string]PaneInfo{
		"cc-a": {Live: true, Path: "/repo with space"},
		"cc-b": {Live: false, Path: ""},
		"cc-c": {Live: true, Path: "/p/with:colon"},
	}
	if got := parseListPanes(out); !reflect.DeepEqual(got, want) {
		t.Errorf("parseListPanes = %v, want %v", got, want)
	}
}

func TestListPanes_argvAndNoServer(t *testing.T) {
	var gotArgs []string
	c := &Client{Socket: "s"}
	c.run = func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("no server running on /tmp/tmux-1/s\n"), errors.New("exit status 1")
	}
	panes, err := c.ListPanes()
	if err != nil || len(panes) != 0 {
		t.Errorf("no server = (%v, %v), want an empty map and no error", panes, err)
	}
	want := []string{"-L", "s", "list-panes", "-a", "-F", listPanesFormat}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("argv = %v, want %v", gotArgs, want)
	}

	c.run = func(...string) ([]byte, error) {
		return []byte("protocol version mismatch"), errors.New("exit status 1")
	}
	if panes, err := c.ListPanes(); err == nil || panes != nil {
		t.Errorf("another failure = (%v, %v), want an error so callers fall back per row", panes, err)
	}
}
