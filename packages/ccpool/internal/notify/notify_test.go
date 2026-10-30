package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/ccpool/internal/notify/notifytest"
)

func TestNone_isNoOp(t *testing.T) {
	if err := (None{}).Notify(Event{Name: "a", State: "failed"}); err != nil {
		t.Errorf("None.Notify = %v, want nil", err)
	}
}

func TestExec_runsCommandWithEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "fired")
	// a tiny shell command that records the env it received
	ex := Exec{Argv: []string{"sh", "-c", "printf '%s %s' \"$CCPOOL_NAME\" \"$CCPOOL_STATE\" > " + out}}
	if err := ex.Notify(Event{Name: "alpha", UUID: "u", State: "needs_input", CWD: "/x"}); err != nil {
		t.Fatalf("Exec.Notify: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "alpha needs_input" {
		t.Errorf("got %q, want 'alpha needs_input'", string(b))
	}
}

func TestFromConfig_selectsAdapter(t *testing.T) {
	if _, ok := FromConfig("none", "").(None); !ok {
		t.Error("none → None")
	}
	if _, ok := FromConfig("exec", "sh -c true").(Exec); !ok {
		t.Error("exec → Exec")
	}
	if _, ok := FromConfig("desktop", "").(Desktop); !ok {
		t.Error("desktop → Desktop")
	}
	// unknown falls back to None (never error on a misconfig).
	if _, ok := FromConfig("bogus", "").(None); !ok {
		t.Error("unknown adapter should fall back to None")
	}
}

func TestShouldNotify_edgeAndMembership(t *testing.T) {
	on := []string{"needs_input", "errored"}
	if !ShouldNotify(on, "working", "needs_input") {
		t.Error("working→needs_input should notify")
	}
	if ShouldNotify(on, "needs_input", "needs_input") {
		t.Error("needs_input→needs_input (no edge) should NOT notify")
	}
	if ShouldNotify(on, "working", "done") {
		t.Error("done is not in On; should NOT notify")
	}
}

func TestExec_emptyArgvIsNoOp(t *testing.T) {
	if err := (Exec{}).Notify(Event{Name: "a", State: "needs_input"}); err != nil {
		t.Errorf("empty argv = %v, want nil", err)
	}
}

func TestExec_errorTextNamesCommandFailureAndOutput(t *testing.T) {
	err := (Exec{Argv: []string{"sh", "-c", "echo boom >&2; exit 3"}}).Notify(Event{Name: "a"})
	if err == nil {
		t.Fatal("want an error from a failing command")
	}
	if got := err.Error(); !strings.HasPrefix(got, "notify exec: ") || !strings.Contains(got, "exit status 3") || !strings.HasSuffix(got, " (boom)") {
		t.Errorf("error text = %q", got)
	}
}

func TestNone_postsNothing(t *testing.T) {
	srv := notifytest.Start(t)
	_ = FromConfig("none", "").Notify(Event{Name: "a", State: "needs_input"})
	_ = FromConfig("bogus", "").Notify(Event{Name: "a", State: "needs_input"})
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("none/unknown posted %d requests, want 0", n)
	}
}

// CP-2 / CP-6 / CP-8: desktop posts through the daemon with the contract
// fields, and only those.
func TestDesktop_postsContractFields(t *testing.T) {
	cases := []struct {
		ev        Event
		wantTitle string
		wantBody  string
	}{
		{Event{Name: "alpha", State: "needs_input", CWD: "/work/x"}, "ccpool: alpha", "needs_input — /work/x"},
		{Event{Name: "beta", State: "errored"}, "ccpool: beta", "errored"},
	}
	for _, tc := range cases {
		srv := notifytest.Start(t)
		if err := FromConfig("desktop", "").Notify(tc.ev); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		reqs := srv.Requests()
		if len(reqs) != 1 {
			t.Fatalf("requests = %d, want 1", len(reqs))
		}
		r := reqs[0]
		if r.ProtocolVersion != 1 || r.Service != "notify" || r.Op != "post" {
			t.Errorf("envelope = %+v", r)
		}
		want := map[string]any{"title": tc.wantTitle, "body": tc.wantBody, "source": "ccpool"}
		if len(r.Args) != len(want) {
			t.Errorf("args = %v, want exactly %v (no subtitle, sound, url, id, group, level)", r.Args, want)
		}
		for k, v := range want {
			if r.Args[k] != v {
				t.Errorf("args[%s] = %v, want %v", k, r.Args[k], v)
			}
		}
	}
}

// CP-11: name and cwd are cut on a rune boundary; title <= 256 B, body <= 2048 B.
func TestDesktop_capsFieldsOnRuneBoundary(t *testing.T) {
	srv := notifytest.Start(t)
	name := strings.Repeat("é", 300) // 2-byte runes
	cwd := strings.Repeat("日", 2000) // 3-byte runes
	if err := (Desktop{}).Notify(Event{Name: name, State: "needs_input", CWD: cwd}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	a := srv.Requests()[0].Args
	title, body := a["title"].(string), a["body"].(string)
	if len(title) > 256 || len(body) > 2048 {
		t.Errorf("title %d B, body %d B exceed caps", len(title), len(body))
	}
	if !utf8.ValidString(title) || !utf8.ValidString(body) {
		t.Error("cut split a rune")
	}
	if !strings.HasPrefix(title, "ccpool: é") || !strings.HasPrefix(body, "needs_input — 日") {
		t.Errorf("title/body prefix wrong: %q / %q", title[:12], body[:20])
	}
	if len(title) != 256 || len(body) < 2048-2 {
		t.Errorf("cut too short: title %d B, body %d B", len(title), len(body))
	}
}

// CP-7: an unreachable, hung or refusing daemon returns an error, bounded by
// the client deadline, and never falls back.
func TestDesktop_unreachableReturnsError(t *testing.T) {
	t.Setenv("PG_OSX_NOTIFIER_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	if err := (Desktop{}).Notify(Event{Name: "a", State: "needs_input"}); err == nil {
		t.Error("want an error when the daemon is unreachable")
	}
}

func TestDesktop_hungDaemonIsBoundedByTimeout(t *testing.T) {
	srv := notifytest.Start(t)
	srv.SetHang(true)
	start := time.Now()
	err := (Desktop{Timeout: 50 * time.Millisecond}).Notify(Event{Name: "a", State: "needs_input"})
	if err == nil {
		t.Fatal("want an error from a hung daemon")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("call took %v, want bounded by the injected 50ms deadline", d)
	}
}

func TestDesktop_daemonErrorReply(t *testing.T) {
	srv := notifytest.Start(t)
	srv.SetReply(`{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unauthenticated","message":"denied"}}`)
	err := (Desktop{}).Notify(Event{Name: "a", State: "needs_input"})
	if err == nil || !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("err = %v, want it to name unauthenticated", err)
	}
}

func TestDesktop_malformedAndEmptyReplies(t *testing.T) {
	for _, reply := range []string{`not json`, `{"protocolVersion":1}`} {
		srv := notifytest.Start(t)
		srv.SetReply(reply)
		if err := (Desktop{}).Notify(Event{Name: "a", State: "x"}); err == nil {
			t.Errorf("reply %q: want an error", reply)
		}
	}
}

func TestDesktopSocketPath_discovery(t *testing.T) {
	t.Setenv("PG_OSX_NOTIFIER_SOCKET", "")
	t.Setenv("XDG_STATE_HOME", "/xdg")
	if p, _ := desktopSocketPath(); p != "/xdg/pg-osx-notifier/pg-osx-notifier.sock" {
		t.Errorf("xdg path = %q", p)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/h")
	if p, _ := desktopSocketPath(); p != "/h/.local/state/pg-osx-notifier/pg-osx-notifier.sock" {
		t.Errorf("home path = %q", p)
	}
	t.Setenv("PG_OSX_NOTIFIER_SOCKET", "/s.sock")
	if p, _ := desktopSocketPath(); p != "/s.sock" {
		t.Errorf("env path = %q", p)
	}
}
