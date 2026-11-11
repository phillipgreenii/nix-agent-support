package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
)

func TestParseOverrideFor(t *testing.T) {
	for _, ok := range []string{"1m", "90m", "3h", "24h", "1h30m"} {
		if _, err := parseOverrideFor(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0", "0s", "-5m", "abc", "1d", "2d12h", "25h", "24h1s"} {
		if _, err := parseOverrideFor(bad); err == nil {
			t.Errorf("%q accepted, want a usage error", bad)
		}
	}
	if _, err := parseOverrideFor("25h"); err == nil || err.Error() != "override capped at 24h; use --until-cleared" {
		t.Errorf("cap message = %v", err)
	}
}

func TestFormatRemaining(t *testing.T) {
	for in, want := range map[time.Duration]string{
		3 * time.Hour: "3h",
		time.Hour + 58*time.Minute + 50*time.Second: "1h58m",
		12*time.Minute + 59*time.Second:             "12m",
		30 * time.Second:                            "0m",
		-time.Minute:                                "0m",
		24 * time.Hour:                              "24h",
	} {
		if got := formatRemaining(in); got != want {
			t.Errorf("formatRemaining(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestOverrideTextAndRenderLevels(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	exp := now.Add(3 * time.Hour).Format(time.RFC3339)
	v := levelView{Resource: "cpu", Level: "HIGH", Source: "operator", Owner: "operator", AgeS: 5}
	v.Override = &struct {
		UntilCleared bool   `json:"until_cleared"`
		ExpiresAt    string `json:"expires_at"`
	}{false, exp}
	if got := overrideText(v, now); got != "OVERRIDE by operator, expires in 3h" {
		t.Errorf("override text = %q", got)
	}
	v.Override.UntilCleared = true
	if got := overrideText(v, now); got != "OVERRIDE by operator, until cleared" {
		t.Errorf("until cleared text = %q", got)
	}
	var b bytes.Buffer
	renderLevels(&b, []levelView{{Resource: "disk"}, v}, now)
	out := b.String()
	if !strings.Contains(out, "disk: no reading") || !strings.Contains(out, "cpu: HIGH") || !strings.Contains(out, "OVERRIDE by operator, until cleared") {
		t.Errorf("render = %q", out)
	}
}

type cliRefresher struct{ got []string }

func (c *cliRefresher) RefreshLevels(_ context.Context, r []string) []core.LevelRefreshResult {
	c.got = r
	return []core.LevelRefreshResult{{Resource: "cpu", Outcome: "applied", Level: "LOW"}}
}

// levelCore starts a real core with a level projection over a mutable clock.
func levelCore(t *testing.T, fr core.LevelRefresher) (core.Ref, *time.Time) {
	t.Helper()
	logDir := shortDir(t)
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	clk := &now
	proj := core.NewLevelProjection(core.LevelProjectionConfig{
		Registry: levels.Registry{
			"cpu":    {Levels: []string{"LOW", "HIGH"}, Unknown: "LOW"},
			"router": {Levels: []string{"RUNNING", "PAUSED"}, Unknown: "RUNNING"},
		},
		Resources: map[string]core.LevelResource{"cpu": {Sensor: "host-sensor", GraceFactor: 2}},
		Skew:      time.Minute,
		Now:       func() time.Time { return *clk },
	})
	svc, err := core.Listen(core.Options{LogDir: logDir, Queue: q, Bindings: core.NewBindings("t"), Levels: proj, LevelRefresher: fr})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Accept(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return svc.Ref(), clk
}

func cli(t *testing.T, ref core.Ref, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	full := append(append([]string{}, args...), "--socket", ref.Socket, "--token", ref.Token)
	var code int
	switch args[0] {
	case "pause":
		code = runPauseIO(full[1:], &out, &errb)
	case "resume":
		code = runResumeIO(full[1:], &out, &errb)
	default:
		code = runLevelIO(full, &out, &errb)
	}
	return code, out.String(), errb.String()
}

func TestLevelCLI_setListExpireClear(t *testing.T) {
	ref, clk := levelCore(t, nil)
	code, out, errs := cli(t, ref, "set", "cpu", "HIGH", "--for", "3h")
	if code != 0 || !strings.Contains(out, "OVERRIDE, expires in") {
		t.Fatalf("set: %d %q %q", code, out, errs)
	}
	code, out, _ = cli(t, ref, "list")
	if code != 0 || !strings.Contains(out, "cpu: HIGH (operator") || !strings.Contains(out, "OVERRIDE by operator, expires in") {
		t.Fatalf("list: %d %q", code, out)
	}
	*clk = clk.Add(4 * time.Hour)
	_, out, _ = cli(t, ref, "list")
	if strings.Contains(out, "OVERRIDE") || !strings.Contains(out, "cpu: no reading") {
		t.Fatalf("after expiry the override must be gone: %q", out)
	}
	cli(t, ref, "set", "cpu", "HIGH", "--until-cleared")
	_, out, _ = cli(t, ref, "list")
	if !strings.Contains(out, "OVERRIDE by operator, until cleared") {
		t.Fatalf("until cleared: %q", out)
	}
	code, out, _ = cli(t, ref, "clear", "cpu")
	if code != 0 || !strings.Contains(out, "cleared: cpu") {
		t.Fatalf("clear: %d %q", code, out)
	}
	if code, out, _ = cli(t, ref, "clear", "cpu"); code != 0 || !strings.Contains(out, "no operator override") {
		t.Fatalf("second clear: %d %q", code, out)
	}
}

func TestLevelCLI_usageAndRouterRejections(t *testing.T) {
	ref, _ := levelCore(t, nil)
	for name, args := range map[string][]string{
		"neither":   {"set", "cpu", "HIGH"},
		"both":      {"set", "cpu", "HIGH", "--for", "1h", "--until-cleared"},
		"over cap":  {"set", "cpu", "HIGH", "--for", "25h"},
		"day form":  {"set", "cpu", "HIGH", "--for", "1d"},
		"zero":      {"set", "cpu", "HIGH", "--for", "0s"},
		"arity":     {"set", "cpu", "--for", "1h"},
		"no verb":   {},
		"bad verb":  {"explode"},
		"pause 25h": {"pause", "--for", "25h"},
		"pause 1d":  {"pause", "--for", "1d"},
	} {
		if len(args) == 0 {
			if code := runLevel(nil); code != 2 {
				t.Errorf("%s: exit %d, want 2", name, code)
			}
			continue
		}
		if code, _, errs := cli(t, ref, args...); code != 2 {
			t.Errorf("%s: exit %d (%q), want 2", name, code, errs)
		}
	}
	if code, _, errs := cli(t, ref, "set", "nope", "HIGH", "--for", "1h"); code != 1 || !strings.Contains(errs, "unknown resource") {
		t.Errorf("unknown resource: %d %q", code, errs)
	}
	if code, _, errs := cli(t, ref, "set", "cpu", "BOGUS", "--for", "1h"); code != 1 || !strings.Contains(errs, "unknown level") {
		t.Errorf("unknown level: %d %q", code, errs)
	}
	if code, _, _ := cli(t, ref, "pause", "--for", "24h"); code != 0 {
		t.Errorf("pause 24h exit %d, want 0", code)
	}
}

func TestPauseResumeOverLevels(t *testing.T) {
	ref, _ := levelCore(t, nil)
	if code, out, errs := cli(t, ref, "pause"); code != 0 || !strings.Contains(out, "paused until resumed") {
		t.Fatalf("pause: %d %q %q", code, out, errs)
	}
	_, out, _ := cli(t, ref, "list")
	if !strings.Contains(out, "router: PAUSED") {
		t.Fatalf("router should be PAUSED: %q", out)
	}
	if code, out, _ := cli(t, ref, "resume"); code != 0 || !strings.Contains(out, "resumed") {
		t.Fatalf("resume: %d %q", code, out)
	}
	if _, out, _ = cli(t, ref, "resume"); !strings.Contains(out, "not paused") {
		t.Fatalf("second resume: %q", out)
	}
	cli(t, ref, "pause", "--owner", "ops")
	cli(t, ref, "set", "cpu", "HIGH", "--until-cleared")
	code, out, _ := cli(t, ref, "resume", "--all")
	if code != 0 || !strings.Contains(out, "cleared: cpu, router") {
		t.Fatalf("resume --all: %d %q", code, out)
	}
}

func TestLevelCLI_refresh(t *testing.T) {
	fr := &cliRefresher{}
	ref, _ := levelCore(t, fr)
	code, out, _ := cli(t, ref, "refresh", "cpu")
	if code != 0 || !strings.Contains(out, "cpu: applied LOW") || strings.Join(fr.got, ",") != "cpu" {
		t.Fatalf("refresh: %d %q %v", code, out, fr.got)
	}
	ref2, _ := levelCore(t, nil)
	if code, _, _ := cli(t, ref2, "refresh"); code != 1 {
		t.Fatalf("refresh without a refresher exit %d, want 1", code)
	}
}

func TestRouteLevel(t *testing.T) {
	if r := route([]string{"pg-router", "level", "list"}); r.kind != routeLevel || len(r.rest) != 1 {
		t.Fatalf("route = %+v", r)
	}
}
