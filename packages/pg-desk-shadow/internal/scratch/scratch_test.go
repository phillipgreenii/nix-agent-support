package scratch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const liveDesk = `
self_login: tester
team_members: [teammate]
ticket_patterns: ['[A-Z]+-[0-9]+']
jira: {base_url: "https://tracker.example"}
serve: {addr: "127.0.0.1:1"}
repos:
  - remote: acme/api
    beads_dir: /work/beads-ws
sync:
  mode: apply
  retry: {max_retries: 3}
watch:
  issue: {queries: [work]}
hydration: {max_per_poll: 40}
`

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDeriveDeskConfig(t *testing.T) {
	out, info, err := DeriveDeskConfig([]byte(liveDesk), DeskParams{Queries: []string{"mine", "team"}, SweepMaxAge: "8760h"})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out)
	if m["sync"].(map[string]any)["mode"] != "off" {
		t.Errorf("sync.mode must be off: %v", m["sync"])
	}
	for _, k := range []string{"ticket_patterns", "jira", "serve"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must be removed", k)
		}
	}
	repo := m["repos"].([]any)[0].(map[string]any)
	if repo["remote"] != "acme/api" || repo["beads_dir"] != "/work/beads-ws" {
		t.Errorf("remote and the read-only beads_dir must be kept: %v", repo)
	}
	if m["self_login"] != "tester" {
		t.Error("self_login must be kept")
	}
	watch := m["watch"].(map[string]any)
	if fmt.Sprint(watch["pr"].(map[string]any)["queries"]) != "[mine team]" {
		t.Errorf("watch.pr.queries = %v", watch["pr"])
	}
	if _, ok := watch["issue"]; ok {
		t.Error("only the pr type is watched")
	}
	if m["sweep"].(map[string]any)["max_age"] != "8760h" {
		t.Errorf("sweep = %v", m["sweep"])
	}
	if info.SelfLogin != "tester" || info.Remote != "acme/api" || info.BeadsDir != "/work/beads-ws" || info.MaxPerPoll != 40 {
		t.Errorf("info = %+v", info)
	}
}

func TestDeriveDeskConfigHermeticAndRefusals(t *testing.T) {
	out, info, err := DeriveDeskConfig([]byte(liveDesk), DeskParams{Queries: []string{"mine"}, HermeticBD: true, HermeticDir: "/scratch/beads-ws"})
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, out)["repos"].([]any)[0].(map[string]any)["beads_dir"]; got != "/scratch/beads-ws" || info.BeadsDir != "/scratch/beads-ws" {
		t.Errorf("hermetic bd mode points beads_dir at the scratch workspace, never the live one: %v", got)
	}
	if _, _, err := DeriveDeskConfig([]byte(liveDesk), DeskParams{Queries: []string{"mine"}, HermeticBD: true}); err == nil {
		t.Error("the hermetic mode needs a scratch workspace")
	}
	for name, doc := range map[string]string{
		"no self_login": "repos: [{remote: a/b, beads_dir: /x}]",
		"no repos":      "self_login: x",
		"no remote":     "self_login: x\nrepos: [{beads_dir: /x}]",
		"no beads_dir":  "self_login: x\nrepos: [{remote: a/b}]",
		"empty":         "",
	} {
		if _, _, err := DeriveDeskConfig([]byte(doc), DeskParams{Queries: []string{"mine"}}); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestDerivePRConfigRemovesJira(t *testing.T) {
	live := `
sources: [pg-connector-pr-github, pg-connector-issue-jira]
connector:
  issue: [pg-connector-issue-jira, pg-connector-issue-beads]
  pr: [pg-connector-pr-github]
backends:
  pg-connector-pr-github: {rate_reserve_points: 1000}
  pg-connector-issue-jira: {site: x}
search:
  queries: {mine: "is:pr author:@me"}
`
	out, err := DerivePRConfig([]byte(live))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(out)), "jira") {
		t.Errorf("Jira must be gone:\n%s", out)
	}
	m := decode(t, out)
	if fmt.Sprint(m["connector"].(map[string]any)["issue"]) != "[pg-connector-issue-beads]" {
		t.Errorf("connector = %v", m["connector"])
	}
	if m["backends"].(map[string]any)["pg-connector-pr-github"] == nil || m["search"] == nil {
		t.Error("everything else is kept")
	}
}

func TestResolvePrefersTheUnwrappedBinary(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "pg-desk")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec .pg-desk-wrapped\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("pg-desk", func(string) (string, error) { return wrapper, nil }); err == nil {
		t.Fatal("a wrapper with no unwrapped sibling must be refused")
	}
	real := filepath.Join(dir, ".pg-desk-wrapped")
	if err := os.WriteFile(real, []byte{0xcf, 0xfa, 0xed, 0xfe}, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "pg-desk")
	if err := os.Symlink(wrapper, link); err != nil {
		t.Fatal(err)
	}
	tool, err := Resolve("pg-desk", func(string) (string, error) { return link, nil })
	if err != nil || tool.Path != realPath(t, real) || tool.Wrapper == "" {
		t.Fatalf("%+v %v", tool, err)
	}
	// A plain binary is used as is.
	bin := filepath.Join(dir, "plain")
	_ = os.WriteFile(bin, []byte{0xcf, 0xfa, 0xed, 0xfe}, 0o755)
	if tool, err := Resolve("plain", func(string) (string, error) { return bin, nil }); err != nil || tool.Wrapper != "" {
		t.Fatalf("%+v %v", tool, err)
	}
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestShimScriptQuotesAndRoutesThroughTheClassifier(t *testing.T) {
	s := ShimScript("/bin/self", "gh", "/real dir/gh", "/log/shim.log", false)
	for _, want := range []string{"#!/bin/sh", "exec '/bin/self' shim --tool gh --real '/real dir/gh' --log '/log/shim.log' -- \"$@\""} {
		if !strings.Contains(s, want) {
			t.Errorf("shim script lacks %q: %s", want, s)
		}
	}
	if !strings.Contains(ShimScript("/s", "bd", "/b", "/l", true), "--hermetic-bd") {
		t.Error("hermetic flag")
	}
}

func TestManifestRoundTripAndPolicy(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Phase: "A", CreatedAt: "2026-01-05T09:00:00Z", Home: "/h", BeadsDir: "/h/ws", Consumer: "shadow-compare"}
	if err := l.Save(m); err != nil {
		t.Fatal(err)
	}
	got, err := l.Load()
	if err != nil || got.Phase != "A" || got.Schema != ManifestSchema || got.T0().IsZero() {
		t.Fatalf("%+v %v", got, err)
	}
	p := l.Policy(got)
	if p.BeadsDir != "/h/ws" || len(p.LiveRoots) != 1 || !strings.HasSuffix(p.LiveRoots[0], ".local/state") {
		t.Errorf("policy = %+v", p)
	}
	if _, err := (Layout{Root: t.TempDir()}).Load(); err == nil {
		t.Error("loading an unprepared directory must fail")
	}
}
