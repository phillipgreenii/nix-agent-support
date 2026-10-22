package tmux

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
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
		// remain-on-exit=failed (pg2-olchz) is armed in the SAME tmux
		// invocation (after a ";" separator), so a payload that exits within
		// milliseconds still leaves an inspectable dead pane. A CLEAN exit is
		// still not preserved (which would break every HasSession-based liveness
		// check elsewhere — see client.go's doc comment).
		";", "set-option", "-t", "cc-alpha", "remain-on-exit", "failed",
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("calls = %v\nwant ONE call %v", got, want)
	}
}

// TestClient_NewSession_errorPropagates: a failing create-and-arm call surfaces.
func TestClient_NewSession_errorPropagates(t *testing.T) {
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		return []byte("duplicate session"), fmt.Errorf("boom")
	}}
	if err := c.NewSession("cc-a", "", nil, []string{"claude"}); err == nil {
		t.Error("NewSession: want error when tmux fails, got nil")
	}
}

// An argument ending in ";" must reach tmux escaped, or tmux would read it as a
// command separator and run the rest as a command of its own.
func TestClient_NewSession_escapesTrailingSemicolons(t *testing.T) {
	var got [][]string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) { got = append(got, args); return nil, nil }}
	err := c.NewSession("cc-a", "", map[string]string{"K": "v;"}, []string{"claude", ";", "x;"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-L", "ccpool", "new-session", "-d", "-s", "cc-a",
		"-e", `K=v\;`, "--", "claude", `\;`, `x\;`,
		";", "set-option", "-t", "cc-a", "remain-on-exit", "failed",
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v\nwant %v", got, want)
	}
}

func TestClient_PaneStatus(t *testing.T) {
	cases := []struct {
		name       string
		hasErr     bool
		display    string
		want       PaneStatus
		wantDispls int
	}{
		{name: "absent session", hasErr: true, want: PaneStatus{}, wantDispls: 0},
		{name: "alive", display: "0  \n", want: PaneStatus{Exists: true}, wantDispls: 1},
		{name: "exit status", display: "1 3 \n", want: PaneStatus{Exists: true, Dead: true, Status: 3}, wantDispls: 1},
		{name: "signal", display: "1  15\n", want: PaneStatus{Exists: true, Dead: true, Signal: 15}, wantDispls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			displays := 0
			c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
				switch args[2] {
				case "has-session":
					if tc.hasErr {
						return nil, fmt.Errorf("can't find session")
					}
					return nil, nil
				case "display-message":
					displays++
					return []byte(tc.display), nil
				}
				return nil, fmt.Errorf("unexpected %v", args)
			}}
			got, err := c.PaneStatus("cc-a")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("PaneStatus = %+v, want %+v", got, tc.want)
			}
			if displays != tc.wantDispls {
				t.Errorf("display-message calls = %d, want %d (has-session must gate it)", displays, tc.wantDispls)
			}
		})
	}
}

func TestClient_CaptureTail(t *testing.T) {
	var got []string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		got = args
		return []byte("one\ntwo\nthree\n\n\n"), nil
	}}
	tail, err := c.CaptureTail("cc-a", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-L", "ccpool", "capture-pane", "-p", "-t", "cc-a", "-S", "-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
	if tail != "two\nthree" {
		t.Errorf("tail = %q, want the last 2 non-blank lines", tail)
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

// TestClient_HasSession_queriesPaneDead: HasSession must gate a #{pane_dead}
// query behind a preceding `has-session` (pg2-olchz), so a crashed-but-
// preserved pane (remain-on-exit=failed) reads as NOT live — otherwise every
// liveness-based caller (reuse-live, phantom-prune reap, Close's fast path,
// pg-router-ccpool-handler's active()) would treat a crashed session as still
// running. Two calls, in this order: `has-session` first (exact-match
// existence), THEN `display-message -p '#{pane_dead}'` (liveness of the
// confirmed-existing pane) — never pane_dead alone; see
// TestClient_HasSession_neverQueriesPaneDeadOnNonexistentTarget for why.
func TestClient_HasSession_queriesPaneDead(t *testing.T) {
	var got [][]string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		got = append(got, args)
		if args[2] == "has-session" {
			return nil, nil
		}
		return []byte("0\n"), nil
	}}
	if !c.HasSession("cc-a") {
		t.Error("HasSession = false, want true for a live (pane_dead=0) session")
	}
	wantHas := []string{"-L", "ccpool", "has-session", "-t", "cc-a"}
	wantDead := []string{"-L", "ccpool", "display-message", "-p", "-t", "cc-a", "#{pane_dead}"}
	if len(got) != 2 || !reflect.DeepEqual(got[0], wantHas) || !reflect.DeepEqual(got[1], wantDead) {
		t.Errorf("argv = %v\nwant [%v %v]", got, wantHas, wantDead)
	}
}

func TestClient_HasSession_deadPaneNotLive(t *testing.T) {
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		if args[2] == "has-session" {
			return nil, nil // the session itself still exists (remain-on-exit=failed kept it)
		}
		return []byte("1\n"), nil // but the pane's process crashed
	}}
	if c.HasSession("cc-a") {
		t.Error("HasSession = true, want false for a dead (crashed) pane")
	}
}

func TestClient_HasSession_missingSessionNotLive(t *testing.T) {
	c := &Client{Socket: "ccpool", run: func(_ ...string) ([]byte, error) {
		return []byte("can't find session"), fmt.Errorf("exit status 1")
	}}
	if c.HasSession("cc-gone") {
		t.Error("HasSession = true, want false when the session doesn't exist")
	}
}

// TestClient_HasSession_neverQueriesPaneDeadOnNonexistentTarget is the
// regression this two-step shape exists to prevent (pg2-olchz): unlike
// `has-session -t <name>`, tmux's `display-message -p -t <name>` does NOT
// error on a target that doesn't exist — verified against tmux 3.6a, with
// exactly one real session on the socket it silently falls back to that
// session's own pane_dead instead of failing. Querying pane_dead first (or
// alone) on a brand-new external_id would therefore read some OTHER, unrelated
// session's liveness and falsely report the new one as already live (this
// broke `ccpool new second` while "first" was the only session actually on
// the socket: it read back route=reuse_live against first's pane instead of
// launching). HasSession must never reach the pane_dead call unless
// has-session already confirmed the exact target exists.
func TestClient_HasSession_neverQueriesPaneDeadOnNonexistentTarget(t *testing.T) {
	calledDisplayMessage := false
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		if args[2] == "has-session" {
			return []byte("can't find session: cc-gone"), fmt.Errorf("exit status 1")
		}
		calledDisplayMessage = true
		return []byte("0\n"), nil // simulates tmux's fallback-to-another-session behavior
	}}
	if c.HasSession("cc-gone") {
		t.Error("HasSession = true, want false when has-session reports the target missing")
	}
	if calledDisplayMessage {
		t.Error("HasSession queried display-message after has-session reported the target missing; want short-circuit")
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
	// The buffer name is per-call-unique (session name + uuid nonce, see
	// pg2-xz6es) rather than the old hardcoded "ccpool-paste" constant, so
	// pull the actual name out of the load-buffer argv (index 4: -L ccpool
	// load-buffer -b <name> -) and assert load/paste agree on it and that it
	// is scoped to this call's session name.
	if len(got[0]) != 6 || got[0][3] != "-b" {
		t.Fatalf("load argv shape = %v, want [-L ccpool load-buffer -b <name> -]", got[0])
	}
	buf := got[0][4]
	if !strings.HasPrefix(buf, "ccpool-paste-cc-a-") {
		t.Errorf("buffer name = %q, want prefix %q", buf, "ccpool-paste-cc-a-")
	}
	wantLoad := []string{"-L", "ccpool", "load-buffer", "-b", buf, "-"}
	wantPaste := []string{"-L", "ccpool", "paste-buffer", "-p", "-d", "-b", buf, "-t", "cc-a"}
	if !reflect.DeepEqual(got[0], wantLoad) {
		t.Errorf("load argv = %v want %v", got[0], wantLoad)
	}
	if !reflect.DeepEqual(got[1], wantPaste) {
		t.Errorf("paste argv = %v want %v", got[1], wantPaste)
	}
}

// TestClient_Paste_bufferNameUniquePerCall exercises concurrent Paste() calls
// (pg2-xz6es): the buffer name used to be a shared hardcoded constant
// ("ccpool-paste"), so two concurrent calls raced on load-buffer/paste-buffer
// -d against the SAME name — one side silently got the other's prompt body,
// the other got a "no buffer" error. Firing many goroutines concurrently and
// asserting every load-buffer name is distinct proves the race is gone
// without needing a real tmux socket.
func TestClient_Paste_bufferNameUniquePerCall(t *testing.T) {
	var mu sync.Mutex
	var loadBufs []string
	var pasteBufs []string
	c := &Client{Socket: "ccpool", run: func(args ...string) ([]byte, error) {
		// paste-buffer argv: -L ccpool paste-buffer -p -d -b <name> -t <session>
		mu.Lock()
		pasteBufs = append(pasteBufs, args[6])
		mu.Unlock()
		return nil, nil
	}}
	c.runStdin = func(_ string, args ...string) ([]byte, error) {
		// load-buffer argv: -L ccpool load-buffer -b <name> -
		mu.Lock()
		loadBufs = append(loadBufs, args[4])
		mu.Unlock()
		return nil, nil
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.Paste("cc-concurrent", "body")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Paste: %v", err)
		}
	}

	if len(loadBufs) != n || len(pasteBufs) != n {
		t.Fatalf("got %d load-buffer calls and %d paste-buffer calls, want %d each", len(loadBufs), len(pasteBufs), n)
	}
	seen := make(map[string]bool, n)
	for _, buf := range loadBufs {
		if seen[buf] {
			t.Fatalf("buffer name %q reused across concurrent Paste calls", buf)
		}
		seen[buf] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d unique buffer names across %d concurrent calls, want %d unique", len(seen), n, n)
	}
}
