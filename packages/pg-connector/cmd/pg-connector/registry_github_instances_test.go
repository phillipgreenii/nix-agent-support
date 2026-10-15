package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The deployment-shaped registry (docket pg2-z5fax.5, Phase 3c): the
// {name, command} mapping form the home-manager module renders for the
// daemon-backed pg-connector-github (`command: [pg-connector-github, --config,
// <rendered config path>, pr|ci]`) in connector.pr, connector.ci,
// search.sources, activity.sources and attention.sources. The fixture is the
// SAME bytes as tests/fixtures/pg-connector-home/github-instances.yaml, which
// `checks.test-pg-connector-home-rendering` compares to the module's rendered
// registry (store path normalized to githubInstancesConfigPath) and to this
// copy, so the Nix-rendered output, the golden and this parser are tied
// together. The fake daemon client and every name are generic.

const (
	githubInstancesFixture    = "testdata/registry-github-instances.yaml"
	githubInstancesConfigPath = "/example/state/pg-connector-github/config.json"
	githubPRInstance          = "pg-connector-pr-github"
	githubCIInstance          = "pg-connector-ci-github-actions"
)

func githubInstanceArgv(kind string) []string {
	return []string{"pg-connector-github", "--config", githubInstancesConfigPath, kind}
}

func TestRegistryGithubInstancesLoads(t *testing.T) {
	reg, err := loadRegistryFile(githubInstancesFixture)
	if err != nil {
		t.Fatalf("loadRegistryFile: %v", err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if got, err := reg.List("pr"); err != nil || !reflect.DeepEqual(got, []string{githubPRInstance}) {
		t.Errorf("List(pr) = %v, %v", got, err)
	}
	if got, err := reg.List("ci"); err != nil || !reflect.DeepEqual(got, []string{githubCIInstance}) {
		t.Errorf("List(ci) = %v, %v", got, err)
	}
	for name, want := range map[string][]string{
		"search":    {githubPRInstance},
		"activity":  {githubPRInstance},
		"attention": {"pg-connector-issue-beads", githubPRInstance},
	} {
		fn := map[string]func() ([]string, error){
			"search": reg.SearchSources, "activity": reg.ActivitySources, "attention": reg.AttentionSources,
		}[name]
		if got, err := fn(); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s sources = %v, %v; want %v", name, got, err, want)
		}
	}

	if got := reg.Command(githubPRInstance); !reflect.DeepEqual(got, githubInstanceArgv("pr")) {
		t.Errorf("Command(%s) = %v", githubPRInstance, got)
	}
	if got := reg.Command(githubCIInstance); !reflect.DeepEqual(got, githubInstanceArgv("ci")) {
		t.Errorf("Command(%s) = %v", githubCIInstance, got)
	}
	// The plain-string neighbour keeps its bare-name argv.
	if got := reg.Command("pg-connector-issue-beads"); !reflect.DeepEqual(got, []string{"pg-connector-issue-beads"}) {
		t.Errorf("plain command = %v", got)
	}

	// The --config word is the same in EVERY occurrence of both names (the
	// registry parser keeps one argv per name, so this reads the fixture's own
	// text: five registrations carrying one path).
	raw, err := os.ReadFile(githubInstancesFixture)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "- "+githubInstancesConfigPath); n != 5 {
		t.Errorf("fixture carries the config path %d times, want 5 (one per registration)", n)
	}
	words := map[string]bool{}
	for _, name := range []string{githubPRInstance, githubCIInstance} {
		words[reg.Command(name)[2]] = true
	}
	if len(words) != 1 {
		t.Errorf("--config word differs between the two instances: %v", words)
	}

	all, err := reg.AllBackends()
	if err != nil {
		t.Fatalf("AllBackends: %v", err)
	}
	counts := map[string]int{}
	for _, name := range all {
		counts[name]++
	}
	for _, name := range []string{githubPRInstance, githubCIInstance} {
		if counts[name] != 1 {
			t.Errorf("AllBackends lists %s %d times, want once: %v", name, counts[name], all)
		}
	}
	// AllBackends lists the connector.* backends only; the plain-string
	// pg-connector-issue-beads is registered under attention.sources alone.
	if len(all) != 2 {
		t.Errorf("AllBackends = %v, want exactly the two instance names", all)
	}
}

// writeRecordingGithubClient puts a fake pg-connector-github first on PATH. It
// appends one "ARGV<TAB>word..." line and one "STDIN<TAB>request" line per
// call to the returned log, answers capabilities with a document declaring
// owns_changes and cache_opt_out, and every other op with an empty success.
func writeRecordingGithubClient(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	caps := `{"protocolVersion":1,"ops":["capabilities","auth_status"],"vocabulary":{"owns_changes":true,"cache_opt_out":true}}`
	script := `#!/bin/sh
req=$(cat)
{
  printf 'ARGV'
  for a in "$@"; do printf '\t%s' "$a"; done
  printf '\n'
  printf 'STDIN\t%s\n' "$req"
} >> '` + logPath + `'
case "$req" in
  *'"op":"capabilities"'*) printf '%s\n' '` + caps + `' ;;
  *) printf '%s\n' '{"protocolVersion":1,"schemaVersion":1,"result":{"ok":true}}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "pg-connector-github"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pg-connector-github: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// readCalls decodes the recorder's log into (argv words, stdin) per call.
func readCalls(t *testing.T, logPath string) (argvs [][]string, stdins []string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		switch fields[0] {
		case "ARGV":
			argvs = append(argvs, fields[1:])
		case "STDIN":
			stdins = append(stdins, strings.Join(fields[1:], "\t"))
		}
	}
	return argvs, stdins
}

func TestRegistryGithubInstancesExecArgv(t *testing.T) {
	reg, err := loadRegistryFile(githubInstancesFixture)
	if err != nil {
		t.Fatalf("loadRegistryFile: %v", err)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	logPath := writeRecordingGithubClient(t)
	ctx := context.Background()

	for _, tc := range []struct{ name, kind string }{
		{githubPRInstance, "pr"},
		{githubCIInstance, "ci"},
	} {
		caps, err := reg.InvokeCapabilities(ctx, tc.name)
		if err != nil {
			t.Fatalf("InvokeCapabilities(%s): %v", tc.name, err)
		}
		for _, key := range []string{"owns_changes", "cache_opt_out"} {
			if v, _ := caps.Vocabulary[key].(bool); !v {
				t.Errorf("%s capabilities vocabulary[%s] = %v, want true", tc.name, key, caps.Vocabulary[key])
			}
		}
		if _, err := reg.Invoke(ctx, tc.name, "auth_status", map[string]string{"probe": tc.kind}, nil); err != nil {
			t.Fatalf("Invoke(%s, auth_status): %v", tc.name, err)
		}
	}

	argvs, stdins := readCalls(t, logPath)
	wantArgs := [][]string{
		{"--config", githubInstancesConfigPath, "pr"}, // capabilities, pr
		{"--config", githubInstancesConfigPath, "pr"}, // auth_status, pr
		{"--config", githubInstancesConfigPath, "ci"}, // capabilities, ci
		{"--config", githubInstancesConfigPath, "ci"}, // auth_status, ci
	}
	if !reflect.DeepEqual(argvs, wantArgs) {
		t.Fatalf("argv received by pg-connector-github = %v, want %v", argvs, wantArgs)
	}
	if len(stdins) != 4 {
		t.Fatalf("recorded %d requests, want 4: %v", len(stdins), stdins)
	}
	for i, want := range []struct{ op, probe string }{
		{"capabilities", ""}, {"auth_status", "pr"}, {"capabilities", ""}, {"auth_status", "ci"},
	} {
		var req struct {
			Op   string            `json:"op"`
			Args map[string]string `json:"args"`
		}
		if err := json.Unmarshal([]byte(stdins[i]), &req); err != nil {
			t.Fatalf("request %d is not JSON: %q: %v", i, stdins[i], err)
		}
		if req.Op != want.op || req.Args["probe"] != want.probe {
			t.Errorf("request %d on stdin = %q, want op %q probe %q", i, stdins[i], want.op, want.probe)
		}
	}
}

// One instance name registered with two different argv lists is rejected, and
// the error names the instance (the spec's rule that EVERY registration of an
// instance name carries the same argv).
func TestRegistryGithubInstancesDivergentArgvRejected(t *testing.T) {
	raw, err := os.ReadFile(githubInstancesFixture)
	if err != nil {
		t.Fatal(err)
	}
	// Change the config path of the search.sources registration only.
	text := string(raw)
	idx := strings.Index(text, "search:")
	if idx < 0 {
		t.Fatal("fixture has no search: list")
	}
	diverged := text[:idx] + strings.Replace(text[idx:], githubInstancesConfigPath, "/example/state/other/config.json", 1)
	if diverged == text {
		t.Fatal("divergence not applied")
	}
	path := filepath.Join(t.TempDir(), "registry.yaml")
	if err := os.WriteFile(path, []byte(diverged), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadRegistryFile(path)
	if err == nil {
		err = reg.Validate()
	}
	if err == nil {
		t.Fatal("a registry with two argv lists for one instance name was accepted")
	}
	if !strings.Contains(err.Error(), githubPRInstance) {
		t.Errorf("error does not name the instance %q: %v", githubPRInstance, err)
	}
}
