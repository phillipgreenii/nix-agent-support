package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRouterConfigFixture(t *testing.T) {
	rc, err := loadRouterConfig("testdata/doctor_router_config.toml")
	if err != nil {
		t.Fatalf("loadRouterConfig: %v", err)
	}
	if len(rc.Queries) != 5 || len(rc.Roles) != 2 {
		t.Fatalf("queries=%d roles=%d, want 5 and 2", len(rc.Queries), len(rc.Roles))
	}
	q := rc.Queries[0]
	if q.Name != "desk-pr-changes" || q.Every != 60*time.Second {
		t.Errorf("first query = %+v", q)
	}
	if got := strings.Join(q.Argv, " "); got != "example-adapter pg-desk pr --consumer pg-router" {
		t.Errorf("argv = %q", got)
	}
	r := rc.Roles[0]
	if r.Name != "pr-decider" || !r.Enabled || len(r.Binds) != 4 || r.Binds[1] != "pr.closed" {
		t.Errorf("first role = %+v", r)
	}
}

func TestRouterConfigMatching(t *testing.T) {
	rc, err := loadRouterConfig("testdata/doctor_router_config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := rc.PollInterval("pr"); !ok || d != 30*time.Second {
		t.Errorf("pr poll interval = %v, %v; want the smallest, 30s", d, ok)
	}
	if d, ok := rc.PollInterval("issue"); !ok || d != 5*time.Minute {
		t.Errorf("issue poll interval = %v, %v", d, ok)
	}
	if _, ok := rc.PollInterval("note"); ok {
		t.Error("a query naming no consumer must not match")
	}
	if d, ok := rc.ConsumerPeriod("pr", "pg-router"); !ok || d != time.Minute {
		t.Errorf("pg-router/pr period = %v, %v; want 1m", d, ok)
	}
	if _, ok := rc.ConsumerPeriod("pr", "someone-else"); ok {
		t.Error("an unknown consumer must not match")
	}
	if _, ok := rc.ConsumerPeriod("issue", "pg-router-fast"); ok {
		t.Error("a consumer naming a different type must not match")
	}
}

func TestRouterConfigRolesBindByExactPrefix(t *testing.T) {
	rc, err := parseRouterConfig(`
[[role]]
name = "wild"
binds = ["pr*", "prx.opened", "issue.opened"]
[[role]]
name = "off"
enabled = false
binds = ["pr.opened"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := rc.RolesBoundTo("pr"); len(got) != 1 || got[0].Name != "off" || got[0].Enabled {
		t.Errorf("pr roles = %+v; a wildcard or look-alike prefix must not bind", got)
	}
	if got := rc.RolesBoundTo("issue"); len(got) != 1 || got[0].Name != "wild" {
		t.Errorf("issue roles = %+v", got)
	}
	if got := rc.RolesBoundTo("thread"); len(got) != 0 {
		t.Errorf("thread roles = %+v, want none", got)
	}
}

func TestParseRouterConfigErrors(t *testing.T) {
	for name, text := range map[string]string{
		"unterminated array": "[[role]]\nname = \"x\"\nbinds = [\n \"a\",\n",
		"bad period":         "[[query]]\ntrigger = { kind = \"period\", every = \"soon\" }\n",
		"no equals":          "[[role]]\nname\n",
		"bad header":         "[[role\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRouterConfig(text); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestParseRouterConfigIgnoresUnknownSectionsAndInlineQueryTables(t *testing.T) {
	rc, err := parseRouterConfig(`
title = "x"
[server]
port = 8
[[query]]
name = "q"
trigger = { kind = "period", every = "2m" }
command = { argv = ["a", "pr", "--consumer=c"], format = "json" }
`)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := rc.ConsumerPeriod("pr", "c"); !ok || d != 2*time.Minute {
		t.Errorf("period = %v, %v; want 2m via --consumer=c and an inline command table", d, ok)
	}
}

func TestRouterPollIntervalsSupplierForServe(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if routerPollIntervals("", logger) != nil {
		t.Error("no router config path must yield no supplier (no verdict)")
	}

	fn := routerPollIntervals("testdata/doctor_router_config.toml", logger)
	if d, ok := fn("pr"); !ok || d != 30*time.Second {
		t.Errorf("pr = %v, %v; want 30s (the smallest matching period, as doctor reads it)", d, ok)
	}
	if _, ok := fn("nonexistent-type"); ok {
		t.Error("a type no router query names must be unknown")
	}

	// Re-read on every call: an edited config is picked up without a restart,
	// and an unreadable one degrades to unknown rather than a stale answer.
	path := filepath.Join(t.TempDir(), "router.toml")
	write := func(every string) {
		t.Helper()
		body := "[[query]]\nname = \"q\"\ntrigger = { kind = \"period\", every = \"" + every + "\" }\n[query.command]\nargv = [\"example-adapter\", \"pg-desk\", \"pr\", \"--consumer\", \"c\"]\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("60s")
	live := routerPollIntervals(path, logger)
	if d, ok := live("pr"); !ok || d != time.Minute {
		t.Fatalf("first read = %v, %v", d, ok)
	}
	write("2m")
	if d, ok := live("pr"); !ok || d != 2*time.Minute {
		t.Errorf("after edit = %v, %v; want 2m", d, ok)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := live("pr"); ok {
		t.Error("an unreadable config must be unknown")
	}
}
