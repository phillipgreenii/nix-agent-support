//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// The role-adapter resolver (config.Config.RoleCommand, reached through
// handlerCommandFor) serves every surface that runs or shows a role's adapter:
// config --show, config check, run-role, doctor and startup validate. These
// tests drive each surface against REAL stub handler processes, one per role,
// and read the outcome from the stubs' own recordings, never from a fake.

const (
	adapterNewRole    = "scratch-command"
	adapterSecondRole = "second-command"
)

// adapterWorkspace is a hermetic config with two roles, each with its own stub
// handler.
type adapterWorkspace struct {
	dir    string
	rec    string // the stubs record argv and stdin here
	stub   string // the first role's handler
	second string // the second role's handler
}

// writeAdapterStub writes an executable /bin/sh stub that records argv and stdin
// of every call under rec as "<tag>-argv" and "<tag>-<subcommand>.json", and
// answers: dispatch with dispatchReply, validate with validateReply, and
// postStartup/preShutdown with an ok outcome.
func writeAdapterStub(t *testing.T, path, rec, tag, dispatchReply, validateReply string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"R='" + rec + "'\nT='" + tag + "'\nsub=\"$1\"\n" +
		"echo \"$*\" >> \"$R/$T-argv\"\n" +
		"cat > \"$R/$T-$sub.json\"\n" +
		"case \"$sub\" in\n" +
		"dispatch) echo '" + dispatchReply + "' ;;\n" +
		"validate) echo '" + validateReply + "' ;;\n" +
		"*) echo '{\"schemaVersion\":\"1\",\"id\":\"x\",\"outcome\":\"ok\"}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

const (
	adapterDeferredReply = `{"schemaVersion":"1","id":"x","deferred":true}`
	adapterValidOK       = `{"schemaVersion":"1","problems":[],"warnings":[]}`
	adapterValidBoom     = `{"schemaVersion":"1","problems":["boom"],"warnings":[]}`
)

// newAdapterWorkspace writes the config and points the environment at it.
func newAdapterWorkspace(t *testing.T, dispatchReply, validateReply string) adapterWorkspace {
	t.Helper()
	dir := shortDir(t)
	w := adapterWorkspace{
		dir:    dir,
		rec:    filepath.Join(dir, "rec"),
		stub:   filepath.Join(dir, "stub-handler"),
		second: filepath.Join(dir, "second-handler"),
	}
	for _, d := range []string{w.rec, filepath.Join(dir, "log")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeAdapterStub(t, w.stub, w.rec, "new", dispatchReply, validateReply)
	writeAdapterStub(t, w.second, w.rec, "second", dispatchReply, adapterValidOK)
	cfg := `[[query]]
name = "scratch-src"
emits = ["scratch.event"]
type = "command"
[query.command]
argv = ["/bin/echo", "[]"]
format = "json"
[query.trigger]
kind = "manual"

[[role]]
name = "` + adapterNewRole + `"
enabled = true
binds = ["scratch.event"]
handler = "` + w.stub + `"
survives_shutdown = true
[role.config.command]
argv = ["/usr/bin/true"]
[role.config.sessionEnv]
SCRATCH_KEY = "scratch-value"

[[role]]
name = "` + adapterSecondRole + `"
enabled = true
binds = ["scratch.event"]
handler = "` + w.second + `"
survives_shutdown = true
[role.config.command]
argv = ["/usr/bin/false"]
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("PG_ROUTER_GLOBAL_CONFIG", filepath.Join(dir, "no-global.toml"))
	t.Setenv("PG_ROUTER_LOG_DIR", filepath.Join(dir, "log"))
	t.Setenv("PG_ROUTER_SOCKET", "")
	t.Setenv("PG_ROUTER_TOKEN", "")
	return w
}

func (w adapterWorkspace) load(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func (w adapterWorkspace) recorded(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.rec, name))
	if err != nil {
		t.Fatalf("the stub never recorded %s: %v", name, err)
	}
	return string(b)
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wr
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	_ = wr.Close()
	os.Stdout = old
	return <-done
}

// config --show (text): each role shows the command it runs (its own handler),
// its survives_shutdown value, and the role object as sent, sessionEnv
// included.
func TestConfigShow_textShowsTheResolvedCommandPerRole(t *testing.T) {
	w := newAdapterWorkspace(t, adapterDeferredReply, adapterValidOK)
	var b bytes.Buffer
	renderConfigShow(&b, w.load(t))
	out := b.String()
	for _, want := range []string{
		"command: " + w.stub + " dispatch\n",
		"command: " + w.second + " dispatch\n",
		`"sessionEnv":{"SCRATCH_KEY":"scratch-value"}`,
		`role: {"name":"` + adapterNewRole + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config --show lacks %q:\n%s", want, out)
		}
	}
	// Per-role blocks: both roles survive (their TOML key).
	if got := strings.Count(out, "survives_shutdown: true"); got != 2 {
		t.Errorf("survives_shutdown: true appears %d times, want 2 (one per role):\n%s", got, out)
	}
	if got := strings.Count(out, "      role: "); got != 2 {
		t.Errorf("role object printed %d times, want once per role:\n%s", got, out)
	}
}

// config --show --json carries the same per-role fields, and its role object is
// byte for byte what the stub receives on stdin for the same role.
func TestConfigShowJSON_roleObjectIsExactlyWhatIsSent(t *testing.T) {
	w := newAdapterWorkspace(t, adapterDeferredReply, adapterValidOK)
	cfg := w.load(t)
	var b bytes.Buffer
	renderConfigShowJSON(&b, cfg)
	var rep struct {
		Roles []struct {
			Name             string          `json:"name"`
			Command          string          `json:"command"`
			CommandError     string          `json:"commandError"`
			SurvivesShutdown bool            `json:"survivesShutdown"`
			Role             json.RawMessage `json:"role"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(b.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, b.String())
	}
	if len(rep.Roles) != 2 {
		t.Fatalf("roles = %d, want 2:\n%s", len(rep.Roles), b.String())
	}
	n, o := rep.Roles[0], rep.Roles[1]
	if n.Name != adapterNewRole || n.Command != w.stub+" dispatch" || !n.SurvivesShutdown || len(n.Role) == 0 {
		t.Errorf("first role = %+v, want its own handler, survives true and a role object", n)
	}
	if o.Name != adapterSecondRole || o.Command != w.second+" dispatch" || !o.SurvivesShutdown || len(o.Role) == 0 {
		t.Errorf("second role = %+v, want its own handler, survives true and a role object", o)
	}

	// Deliver one event through run-role and compare what the stub received.
	evt := `{"id":"evt-1","type":"scratch.event","payload":{"id":"item-1","type":"scratch.event"}}`
	if code := runRunRole(adapterNewRole, evt, false); code != exitOK {
		t.Fatalf("run-role exit = %d", code)
	}
	var req struct {
		Role json.RawMessage `json:"role"`
	}
	if err := json.Unmarshal([]byte(w.recorded(t, "new-dispatch.json")), &req); err != nil {
		t.Fatal(err)
	}
	var shown, sent any
	if err := json.Unmarshal(n.Role, &shown); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(req.Role, &sent); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(shown, sent) {
		t.Errorf("config --show prints role %s, but the handler received %s", n.Role, req.Role)
	}
}

// A role built in code without handler shows why it has no command instead of
// printing an empty one (a loaded config never has one: decode rejects it).
func TestConfigShow_roleWithoutHandlerSaysSo(t *testing.T) {
	cfg := config.Config{Roles: roles.RoleSet{{Name: "bare", Enabled: true, Binds: []string{"e"}}}}
	var text, js bytes.Buffer
	renderConfigShow(&text, cfg)
	renderConfigShowJSON(&js, cfg)
	if !strings.Contains(text.String(), "command: (none: role \"bare\" has no handler") {
		t.Errorf("text lacks the none line:\n%s", text.String())
	}
	if !strings.Contains(js.String(), `"commandError": "role \"bare\" has no handler`) &&
		!strings.Contains(js.String(), `"commandError":"role \"bare\" has no handler`) {
		t.Errorf("json lacks commandError:\n%s", js.String())
	}
}

// run-role delivers the event to the role's OWN handler, with the role object, and prints that handler's outcome (here a deferral, then an outcome).
func TestRunRole_newPathReachesItsOwnHandlerAndPrintsItsReply(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		wantJSON    map[string]any
	}{
		{"deferred", adapterDeferredReply, map[string]any{"role": adapterNewRole, "accepted": true, "deferred": true}},
		{"outcome", `{"schemaVersion":"1","id":"x","outcome":"stub-outcome"}`, map[string]any{"role": adapterNewRole, "accepted": true, "outcome": "stub-outcome"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newAdapterWorkspace(t, tc.reply, adapterValidOK)
			evt := `{"id":"evt-1","type":"scratch.event","payload":{"id":"item-1","type":"scratch.event"}}`
			var code int
			out := captureStdout(t, func() { code = runRunRole(adapterNewRole, evt, true) })
			if code != exitOK {
				t.Fatalf("exit = %d, stdout=%s", code, out)
			}
			// The orchestrator's own "# dispatch" summary line precedes the --json
			// report on stdout (as it does for every role); the report is the last line.
			lines := strings.Split(strings.TrimSpace(out), "\n")
			var got map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
				t.Fatalf("decode %q: %v", out, err)
			}
			for k, v := range tc.wantJSON {
				if got[k] != v {
					t.Errorf("--json %s = %v, want %v (full: %s)", k, got[k], v, out)
				}
			}
			// The handler's own recording: argv is [dispatch] only, stdin carries
			// the event and the role object; the other role's handler never ran.
			if argv := w.recorded(t, "new-argv"); !strings.HasPrefix(argv, "dispatch\n") || strings.Contains(argv, " ") {
				t.Errorf("handler argv = %q, want bare subcommands (dispatch first) with no argument after any", argv)
			}
			var req struct {
				Event struct{ ID string } `json:"event"`
				Role  struct {
					Name   string         `json:"name"`
					Config map[string]any `json:"config"`
				} `json:"role"`
			}
			if err := json.Unmarshal([]byte(w.recorded(t, "new-dispatch.json")), &req); err != nil {
				t.Fatal(err)
			}
			if req.Event.ID != "evt-1" || req.Role.Name != adapterNewRole || req.Role.Config["sessionEnv"] == nil || req.Role.Config["command"] == nil {
				t.Errorf("dispatch request = %+v, want the event and the role object with its config", req)
			}
			if _, err := os.Stat(filepath.Join(w.rec, "second-argv")); err == nil {
				t.Error("another role's handler ran for this role")
			}
		})
	}
}

// Text-mode run-role says so when the handler deferred, since a deferral carries
// no outcome for the dispatch line.
func TestRunRole_textModeReportsADeferral(t *testing.T) {
	newAdapterWorkspace(t, adapterDeferredReply, adapterValidOK)
	evt := `{"id":"evt-1","type":"scratch.event","payload":{"id":"item-1","type":"scratch.event"}}`
	var code int
	out := captureStdout(t, func() { code = runRunRole(adapterNewRole, evt, false) })
	if code != exitOK || !strings.Contains(out, "(deferred)") {
		t.Errorf("exit=%d stdout=%q, want exit 0 and a deferred line", code, out)
	}
}

// run-role on the second role reaches the second handler and not the first.
func TestRunRole_eachRoleReachesItsOwnHandler(t *testing.T) {
	w := newAdapterWorkspace(t, adapterDeferredReply, adapterValidOK)
	evt := `{"id":"evt-1","type":"scratch.event","payload":{"id":"item-1","type":"scratch.event"}}`
	if code := runRunRole(adapterSecondRole, evt, false); code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if argv := w.recorded(t, "second-argv"); !strings.HasPrefix(argv, "dispatch\n") || strings.Contains(argv, " ") {
		t.Errorf("second handler argv = %q, want bare subcommands (dispatch first) with no argument after any", argv)
	}
	if !strings.Contains(w.recorded(t, "second-dispatch.json"), `"role":{"name":"`+adapterSecondRole+`"`) {
		t.Errorf("second handler dispatch request lacks its role object: %s", w.recorded(t, "second-dispatch.json"))
	}
	if _, err := os.Stat(filepath.Join(w.rec, "new-argv")); err == nil {
		t.Error("the first role's handler ran for the second role")
	}
}

// doctor calls each role's validate through the resolver: each role's own
// handler answers for it.
func TestDoctor_validateGoesThroughTheResolverPerRole(t *testing.T) {
	for _, tc := range []struct {
		name, validate string
		want           []string
	}{
		{"valid", adapterValidOK, []string{"ok   validate " + adapterNewRole + ": valid", "ok   validate " + adapterSecondRole + ": valid"}},
		{"problem", adapterValidBoom, []string{"FAIL validate " + adapterNewRole + ": boom", "ok   validate " + adapterSecondRole + ": valid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newAdapterWorkspace(t, adapterDeferredReply, tc.validate)
			var out strings.Builder
			doctor(&out, "", "")
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("doctor output lacks %q:\n%s", want, out.String())
				}
			}
			// Each handler saw a validate call carrying its own role object.
			for _, tag := range [][2]string{{"new", adapterNewRole}, {"second", adapterSecondRole}} {
				if !strings.Contains(w.recorded(t, tag[0]+"-validate.json"), `"role":{"name":"`+tag[1]+`"`) {
					t.Errorf("%s handler validate request lacks its role object: %s", tag[0], w.recorded(t, tag[0]+"-validate.json"))
				}
			}
		})
	}
}

// Startup validate goes through the same resolver: a role whose own handler
// reports a problem stays registered and is marked unavailable with the stub's
// problem text, while the other role stays available.
func TestStartupValidate_roleUnavailableThroughItsOwnHandler(t *testing.T) {
	w := newAdapterWorkspace(t, adapterDeferredReply, adapterValidBoom)
	cfg := w.load(t)
	cfg.LogDir = filepath.Join(w.dir, "core-log")
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: wireclient.New(handlerCommandFor(cfg))}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	t.Cleanup(func() { _ = storeClose() })
	t.Cleanup(func() { _ = svc.Close() })
	validateRolesAtStartup(context.Background(), o, cfg)

	reg := svc.Registry()
	got, registered := reg.Get(adapterNewRole)
	if !registered {
		t.Fatal("a role whose own handler failed validate must stay registered")
	}
	if got.Self != core.SelfUnavailable || got.UnavailableReason != "boom" {
		t.Errorf("%s: Self=%q reason=%q, want unavailable with the stub's problem", adapterNewRole, got.Self, got.UnavailableReason)
	}
	if !reg.Available(adapterSecondRole) {
		t.Errorf("%s must stay available: its own handler validated fine", adapterSecondRole)
	}
	if !strings.Contains(w.recorded(t, "new-validate.json"), adapterNewRole) {
		t.Error("the new-path role's own handler was never asked to validate")
	}
}

// config check reports a missing handler path together with a missing query
// command in ONE run (design section 10, V-3).
func TestConfigCheck_reportsMissingHandlerAndMissingQueryCommandTogether(t *testing.T) {
	w := newAdapterWorkspace(t, adapterDeferredReply, adapterValidOK)
	cfgPath := filepath.Join(w.dir, "config2.toml")
	body := `[[query]]
name = "scratch-src"
emits = ["scratch.event"]
type = "command"
[query.command]
argv = ["/nonexistent/scratch-producer"]
format = "json"

[[role]]
name = "ghost"
enabled = true
binds = ["scratch.event"]
handler = "` + filepath.Join(w.dir, "missing") + `"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_CONFIG", cfgPath)

	var stdout, stderr strings.Builder
	if code := configCheck(&stdout, &stderr); code != exitGeneric {
		t.Fatalf("exit = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"/nonexistent/scratch-producer", `role "ghost" handler "` + filepath.Join(w.dir, "missing") + `"`} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("config check output lacks %q:\n%s", want, stderr.String())
		}
	}
	if lines := strings.Split(strings.TrimSpace(stderr.String()), "\n"); len(lines) != 2 {
		t.Errorf("want exactly 2 findings, got %d:\n%s", len(lines), stderr.String())
	}
}

// One resolver for every surface: handlerCommandFor, the config's RoleCommand
// and what config --show prints agree for a role with a handler and for one
// without.
func TestHandlerCommandFor_isTheConfigResolver(t *testing.T) {
	cfg := config.Config{}
	for _, r := range []roles.Role{{Name: "bare"}, {Name: "new", Handler: "/opt/h"}} {
		a, errA := handlerCommandFor(cfg)(r, "validate")
		b, errB := cfg.RoleCommand(r, "validate")
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Errorf("%s: handlerCommandFor = %v (%v), RoleCommand = %v (%v)", r.Name, a, errA, b, errB)
		}
	}
}
