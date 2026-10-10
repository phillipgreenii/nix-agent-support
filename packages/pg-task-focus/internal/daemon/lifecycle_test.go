package daemon_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/daemon"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

// A reload keeps the previous configuration on a bad document, reports it
// through /healthz and config_valid by path only, and recovers on a good one.
func TestReloadKeepsThePreviousConfigurationOnAnInvalidOne(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	digest := func() string {
		var h struct{ Config struct{ Digest string } }
		e.get("/healthz").json(t, &h)
		return h.Config.Digest
	}
	before := digest()

	// A document that is not valid, with a configured value in a message.
	e.writeConfig(func(c map[string]any) {
		c["tasks"].(map[string]any)["plan-day"].(map[string]any)["due"] = map[string]any{"at": "9am-SECRET-VALUE", "tz": "America/New_York"}
	})
	if err := e.d.Reload(); err == nil {
		t.Fatal("an invalid configuration was accepted")
	}
	var h map[string]any
	e.get("/healthz").json(t, &h)
	cfg := h["config"].(map[string]any)
	if cfg["valid"] != false || cfg["digest"] != before {
		t.Errorf("a bad reload keeps the previous digest and reports invalid: %v", cfg)
	}
	msg, _ := cfg["reload_error"].(string)
	if !strings.Contains(msg, "/tasks/plan-day/due") || strings.Contains(msg, "SECRET-VALUE") {
		t.Errorf("reload_error names the path and never echoes a configured value: %q", msg)
	}
	if h["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", h["status"])
	}
	fams := e.scrape()
	if v, _ := sampleValue(fams["pg_task_focus_config_valid"], nil); v != 0 {
		t.Errorf("config_valid = %v, want 0", v)
	}
	if v, _ := sampleValue(fams["pg_task_focus_config_reload_total"], map[string]string{"result": "invalid"}); v != 1 {
		t.Errorf("config_reload_total{invalid} = %v", v)
	}
	// The daemon still serves on the previous configuration.
	if r := e.get("/api/v1/config"); r.Status != 200 {
		t.Fatalf("/config: %d", r.Status)
	}

	// A reload that removes the active profile is a bad reload.
	e.writeConfig(func(c map[string]any) {
		delete(c["profiles"].(map[string]any), "normal")
		c["defaults"].(map[string]any)["profile"] = "on-call"
	})
	if err := e.d.Reload(); err == nil || !strings.Contains(err.Error(), `"normal"`) {
		t.Fatalf("removing the active profile: %v", err)
	}
	if v, _ := sampleValue(e.scrape()["pg_task_focus_config_reload_total"], map[string]string{"result": "refused"}); v != 1 {
		t.Errorf("config_reload_total{refused} = %v", v)
	}

	// A good reload takes effect, advances the version, and a changed port is reported as restart-only.
	v1 := e.state()["version"].(map[string]any)["config_generation"]
	e.writeConfig(func(c map[string]any) {
		c["listen_port"] = e.port + 1
		c["cycles"].(map[string]any)["deep-work"].(map[string]any)["minutes"] = 90
	})
	if err := e.d.Reload(); err != nil {
		t.Fatalf("a good reload: %v", err)
	}
	if v2 := e.state()["version"].(map[string]any)["config_generation"]; v2 == v1 {
		t.Errorf("the config generation did not advance on a reload")
	}
	e.get("/healthz").json(t, &h)
	cfg = h["config"].(map[string]any)
	if cfg["valid"] != true || cfg["digest"] == before {
		t.Errorf("after a good reload: %v", cfg)
	}
	if rr, _ := cfg["restart_required"].([]any); len(rr) != 1 || rr[0] != "listen_port" {
		t.Errorf("restart_required = %v, want [listen_port]", cfg["restart_required"])
	}
	var c struct {
		Cycles []struct {
			ID      string
			Minutes int
		}
	}
	e.get("/api/v1/config").json(t, &c)
	for _, cy := range c.Cycles {
		if cy.ID == "deep-work" && cy.Minutes != 90 {
			t.Errorf("the reloaded minutes are not in force: %d", cy.Minutes)
		}
	}
	if v, _ := sampleValue(e.scrape()["pg_task_focus_config_reload_total"], map[string]string{"result": "restart_required"}); v != 1 {
		t.Errorf("config_reload_total{restart_required} = %v", v)
	}
}

func startErr(t *testing.T, e *env) error {
	t.Helper()
	_, err := daemon.Start(context.Background(), daemon.Params{
		ConfigPath: e.cfg, DataDir: e.dir, Version: "test", Log: e.log, Clock: e.clock,
		Player: e.player, Notifier: e.player,
	})
	return err
}

// Startup refusals are hard failures: logged at Error with their cause (and
// line), flushed, and returned as a StartupError.
func TestStartupRefusals(t *testing.T) {
	t.Run("a second daemon on the same data directory", func(t *testing.T) {
		e := newEnv(t, options{})
		second := *e
		second.port = freePort(t)
		second.writeConfig(nil)
		second.log = &lockedBuf{}
		err := startErr(t, &second)
		var se *daemon.StartupError
		if !errors.As(err, &se) || se.Stage != "lock" || !errors.Is(err, store.ErrLocked) {
			t.Fatalf("second daemon: %v", err)
		}
		if !strings.Contains(second.log.String(), `"level":"ERROR"`) || !strings.Contains(second.log.String(), "startup failed") {
			t.Errorf("no Error line:\n%s", second.log.String())
		}
	})

	t.Run("a corrupt log reports its line", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "data")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		good := `{"v":1,"id":"01M4B9REV0ZVE0ZAAH4D89K31Q","at":"2026-10-07T12:00:00.000Z","effective_at":"2026-10-07T12:00:00.000Z","type":"task.completed","data":{"task_id":"day:2026-10-07:plan-day"}}`
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(good+"\nNOT JSON\n"+good+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		e := newEnv2(t, options{dir: dir})
		err := startErr(t, e)
		if err == nil {
			t.Fatal("a corrupt log started")
		}
		if !strings.Contains(e.log.String(), `"line":2`) {
			t.Errorf("the Error line carries no line number:\n%s", e.log.String())
		}
	})

	t.Run("a bad configuration at first start", func(t *testing.T) {
		e := newEnv2(t, options{})
		if err := os.WriteFile(e.cfg, []byte(`{"listen_port": 1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		var se *daemon.StartupError
		if err := startErr(t, e); !errors.As(err, &se) || se.Stage != "config" {
			t.Fatalf("bad config: %v", err)
		}
	})
}

// A Start that fails while opening the store gives its port back before it
// returns, so a caller that starts again on the same port at once can bind it.
// Start serves HTTP from a goroutine before it opens the store, and
// http.Server.Close closes only the listeners that goroutine has already
// registered: a failure that came back before the goroutine ran left the port
// bound until it did. One P keeps that goroutine off the CPU until Start
// blocks, the interleaving this test is about (the P count is restored with
// the test). A second daemon on a held data directory is the quickest open() to
// fail, and every failure of open() leaves Start by the same branch.
func TestStartReleasesItsPortWhenOpeningTheStoreFails(t *testing.T) {
	held := newEnv(t, options{}) // holds the data-directory lock
	second := newEnv2(t, options{dir: held.dir})
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(second.port))

	prev := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
	for attempt := 1; attempt <= 10; attempt++ {
		var se *daemon.StartupError
		if err := startErr(t, second); !errors.As(err, &se) || se.Stage != "lock" {
			t.Fatalf("attempt %d: a second daemon on a held data directory: %v", attempt, err)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("attempt %d: the port is still bound after the failed Start returned: %v", attempt, err)
		}
		_ = ln.Close()
	}
}

// Item 1 of the hand-over, decided by the daemon: a log whose active profile
// the configuration no longer defines is refused at start, naming the profile,
// the profiles the configuration does define and the fix. The reload check
// already refuses to drop the active profile, so no state the reload forbids
// is entered through a restart.
func TestStartRefusesALogWhoseActiveProfileIsNotConfigured(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap() // the log's active profile is "normal"
	e.d.Stop()
	e.writeConfig(func(c map[string]any) {
		delete(c["profiles"].(map[string]any), "normal")
		c["defaults"].(map[string]any)["profile"] = "on-call"
	})
	e.log = &lockedBuf{}
	err := startErr(t, e)
	var se *daemon.StartupError
	if !errors.As(err, &se) || se.Stage != "active_profile" {
		t.Fatalf("start with the active profile removed: %v", err)
	}
	for _, want := range []string{`"normal"`, "on-call", "restore profile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if !strings.Contains(e.log.String(), `"level":"ERROR"`) {
		t.Errorf("no Error line:\n%s", e.log.String())
	}
	// Restoring the profile lets it start again, with the data untouched.
	e.writeConfig(nil)
	e.start(nil)
	if e.state()["profile"] != "normal" {
		t.Errorf("profile after the fix: %v", e.state()["profile"])
	}
}

// An empty log has no active profile and starts on any configuration.
func TestCheckActiveProfileAcceptsAnEmptyLog(t *testing.T) {
	e := newEnv(t, options{})
	if err := daemon.CheckActiveProfile(e.d.Engine().Snapshot().Config, ""); err != nil {
		t.Fatal(err)
	}
	if err := daemon.CheckActiveProfile(e.d.Engine().Snapshot().Config, "nonesuch"); err == nil {
		t.Fatal("an undefined active profile passed")
	}
}
