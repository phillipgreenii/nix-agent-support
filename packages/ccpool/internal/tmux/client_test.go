package tmux

import (
	"reflect"
	"testing"
)

func TestClient_NewSession_argv(t *testing.T) {
	var got [][]string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		got = append(got, args)
		return nil, nil
	}}
	err := c.NewSession("cc-alpha", "/proj/dir",
		map[string]string{"CCPOOL_NAME": "alpha", "PA_MONITOR_NO_NUDGE": "1"},
		[]string{"claude", "--session-id", "u1"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	want := []string{
		"-L", "ccpool", "new-session", "-d", "-s", "cc-alpha", "-c", "/proj/dir",
		"-e", "CCPOOL_NAME=alpha", "-e", "PA_MONITOR_NO_NUDGE=1",
		"--", "claude", "--session-id", "u1",
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v\nwant %v", got, want)
	}
}

func TestClient_NewSession_omitsCwdWhenEmpty(t *testing.T) {
	var got [][]string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) { got = append(got, args); return nil, nil }}
	if err := c.NewSession("cc-a", "", nil, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range got[0] {
		if a == "-c" {
			t.Errorf("empty cwd must not add -c; argv=%v", got[0])
		}
	}
}

// An empty target must never read as live. `tmux has-session -t ""` matches the
// first/any session on the socket (exit 0), so a hook-created row with an empty
// TmuxSession would falsely read live whenever any session exists on the socket
// (nas-a95.5). HasSession must short-circuit on "" and never shell out.
func TestClient_HasSession_emptyTargetNeverLive(t *testing.T) {
	called := false
	c := &Client{Socket: "ccpool", run: func(_ ...string) ([]byte, error) {
		called = true
		return nil, nil // simulate tmux matching any session (exit 0)
	}}
	if c.HasSession("") {
		t.Error(`HasSession("") = true, want false (empty target must never be live)`)
	}
	if called {
		t.Error(`HasSession("") shelled out to tmux; want short-circuit without querying`)
	}
}

func TestClient_SendKeys_argv(t *testing.T) {
	var got [][]string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) { got = append(got, args); return nil, nil }}
	if err := c.SendKeys("cc-a", "Enter"); err != nil {
		t.Fatal(err)
	}
	want := []string{"-L", "ccpool", "send-keys", "-t", "cc-a", "Enter"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v want %v", got[0], want)
	}
}

func TestClient_PaneCurrentPath_argvAndTrim(t *testing.T) {
	var got [][]string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		got = append(got, args)
		return []byte("/live/path\n"), nil
	}}
	path, err := c.PaneCurrentPath("cc-a")
	if err != nil {
		t.Fatalf("PaneCurrentPath: %v", err)
	}
	want := []string{"-L", "ccpool", "display-message", "-p", "-t", "cc-a", "#{pane_current_path}"}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v want %v", got, want)
	}
	if path != "/live/path" {
		t.Errorf("path = %q, want %q (trailing newline trimmed)", path, "/live/path")
	}
}

func TestClient_KillSession_argv(t *testing.T) {
	var got [][]string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) { got = append(got, args); return nil, nil }}
	_ = c.KillSession("cc-a")
	want := []string{"-L", "ccpool", "kill-session", "-t", "cc-a"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v want %v", got[0], want)
	}
}

func TestClient_Paste_loadsBufferThenPastesBracketed(t *testing.T) {
	var got [][]string
	var stdinSeen string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		got = append(got, args)
		return nil, nil
	}}
	c.runStdin = func(stdin string, args ...string) ([]byte, error) {
		stdinSeen = stdin
		got = append(got, args)
		return nil, nil
	}
	if err := c.Paste("cc-a", "hello\nworld"); err != nil {
		t.Fatalf("Paste: %v", err)
	}
	// first call: load-buffer via stdin; second: paste-buffer -p
	if stdinSeen != "hello\nworld" {
		t.Errorf("load-buffer stdin = %q, want the body", stdinSeen)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tmux calls, got %d: %v", len(got), got)
	}
	wantLoad := []string{"-L", "ccpool", "load-buffer", "-b", "ccpool-paste", "-"}
	wantPaste := []string{"-L", "ccpool", "paste-buffer", "-p", "-d", "-b", "ccpool-paste", "-t", "cc-a"}
	if !reflect.DeepEqual(got[0], wantLoad) {
		t.Errorf("load argv = %v want %v", got[0], wantLoad)
	}
	if !reflect.DeepEqual(got[1], wantPaste) {
		t.Errorf("paste argv = %v want %v", got[1], wantPaste)
	}
}
