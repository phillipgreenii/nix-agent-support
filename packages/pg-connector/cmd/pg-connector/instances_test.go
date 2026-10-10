package main

// Registry instances ({name, command}) end to end, through real subprocess
// fakes (bead pg2-91y12, INV-REG-4): one script registered under two names
// with different arguments, driven through the umbrella's own verbs.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const instScript = `#!/bin/sh
req=$(cat)
dir=""
if [ "$1" = "--beads-dir" ]; then dir="$2"; fi
printf 'ARGC=%s ARGV[%s] REQ[%s]\n' "$#" "$*" "$req" >> "$INST_LOG"
tag=$(basename "$dir")
case "$dir" in
  */down)
    echo '{"protocolVersion":1,"error":{"code":"unavailable","message":"tracker unreachable"}}'
    exit 0;;
esac
op=$(echo "$req" | sed -E 's/^\{"op":"([a-z_]+)".*/\1/')
case "$op" in
  list_activity)
    echo '{"protocolVersion":1,"schemaVersion":1,"result":{"items":[{"id":"'"$tag"'-1","kind":"pr.merged","entity_type":"pr","entity_id":"o/r#1","occurred_at":"2026-10-01T12:00:00Z","summary":"s","fields":{},"as_of":"2026-10-02T00:00:00Z","stale":false}],"truncated":false}}';;
  capabilities)
    echo '{"protocolVersion":1,"schemaVersions":{"issue":ISSUE_SCHEMA_VERSION},"ops":["capabilities","list","show"],"vocabulary":{"workspace_dir":"'"$dir"'"}}';;
  list)
    echo '{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"'"$tag"'-1","title":"t","state":"open"}],"present_ids":["'"$tag"'-1"],"cursor":null,"truncated":false}}';;
  show)
    if [ "$tag" = "pg2" ]; then
      echo '{"protocolVersion":1,"error":{"code":"not_found","message":"no issue found matching"}}'
    else
      echo '{"protocolVersion":1,"schemaVersion":1,"result":{"id":"zr-1","title":"t","state":"open"}}'
    fi;;
  *)
    echo '{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}';;
esac
`

// writeInstanceBackend installs the argv-aware fake as name on PATH and
// points $INST_LOG at a fresh log, returning its path. Each backend call
// appends one line: ARGC, the argv words, and the stdin request.
func writeInstanceBackend(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.ReplaceAll(instScript, "ISSUE_SCHEMA_VERSION", strconv.Itoa(schema.IssueSchemaVersion))), 0o755); err != nil {
		t.Fatalf("write instance backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("INST_LOG", logPath)
	return logPath
}

func writeInstancesConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
}

func instanceCalls(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("backend was never invoked: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// twoInstanceList is the two instances as an indented YAML list; under
// connector.<type> it follows "  issue:\n", under attention:/search: it
// follows "  sources:\n" (twoInstanceSources).
const twoInstanceList = `    - name: inst-pg2
      command: [inst-bin, --beads-dir, /example/pg2]
    - name: inst-zr
      command: [inst-bin, --beads-dir, /example/zr]
`

const twoInstanceSources = "  sources:\n" + twoInstanceList

func twoInstanceActivityConfig(zrDir string) string {
	return "activity:\n  sources:\n" +
		"    - name: inst-pg2\n      command: [inst-bin, --beads-dir, /example/pg2]\n" +
		"    - name: inst-zr\n      command: [inst-bin, --beads-dir, " + zrDir + "]\n" +
		"backends:\n  inst-pg2: {activity_actors: [Example Person]}\n  inst-zr: {activity_actors: [Other Person]}\n"
}

func TestInstances_ActivityFansOutOverBothInstances(t *testing.T) {
	logPath := writeInstanceBackend(t, "inst-bin")
	writeInstancesConfig(t, twoInstanceActivityConfig("/example/zr"))

	stdout, _, code := executePr(t, []string{"activity", "list", "--since", "7d"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	o := decodeActivityOut(t, stdout)
	if len(o.Sources) != 2 || o.Sources[0]["source"] != "inst-pg2" || o.Sources[1]["source"] != "inst-zr" {
		t.Fatalf("sources = %v, want rows named inst-pg2 and inst-zr", o.Sources)
	}
	var got []string
	for _, it := range o.Items {
		got = append(got, it.Source+"/"+it.Item["id"].(string))
	}
	if strings.Join(got, ",") != "inst-pg2/pg2-1,inst-zr/zr-1" {
		t.Fatalf("items = %v", got)
	}

	calls := instanceCalls(t, logPath)
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want exactly one per instance", calls)
	}
	// The instances are called concurrently (INV-FANOUT-1), so the log's
	// arrival order is not registration order; sorting puts the pg2 argv
	// before the zr one.
	sort.Strings(calls)
	// Each instance got ITS argv (command args first, nothing appended)
	// and ITS OWN backends.<name> block in the request.
	for i, want := range []struct{ argv, actor string }{
		{"ARGC=2 ARGV[--beads-dir /example/pg2]", "Example Person"},
		{"ARGC=2 ARGV[--beads-dir /example/zr]", "Other Person"},
	} {
		if !strings.HasPrefix(calls[i], want.argv) || !strings.Contains(calls[i], want.actor) {
			t.Errorf("call %d = %q, want argv %q and actor %q", i, calls[i], want.argv, want.actor)
		}
		if strings.Count(calls[i], "Person") != 1 {
			t.Errorf("call %d carried another instance's config block: %q", i, calls[i])
		}
	}
}

func TestInstances_OneUnreachableInstanceDegradesOnlyThatRow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pg2, zr    string
		degraded   string
		survivor   string
		wantSource string
	}{
		{"zr down", "/example/pg2", "/example/down", "inst-zr", "inst-pg2", "inst-pg2/pg2-1"},
		{"pg2 down", "/example/down", "/example/zr", "inst-pg2", "inst-zr", "inst-zr/zr-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeInstanceBackend(t, "inst-bin")
			writeInstancesConfig(t, "activity:\n  sources:\n"+
				"    - {name: inst-pg2, command: [inst-bin, --beads-dir, "+tc.pg2+"]}\n"+
				"    - {name: inst-zr, command: [inst-bin, --beads-dir, "+tc.zr+"]}\n")

			stdout, _, code := executePr(t, []string{"activity", "list", "--since", "7d"})
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (one source degraded); stdout=%s", code, stdout)
			}
			o := decodeActivityOut(t, stdout)
			status := map[string]string{}
			reason := map[string]string{}
			for _, row := range o.Sources {
				status[row["source"].(string)], _ = row["status"].(string)
				reason[row["source"].(string)], _ = row["reason"].(string)
			}
			if status[tc.degraded] != "degraded" || !strings.Contains(reason[tc.degraded], "unavailable") {
				t.Errorf("%s = %q (%q), want degraded with an unavailable reason", tc.degraded, status[tc.degraded], reason[tc.degraded])
			}
			if status[tc.survivor] != "succeeded" {
				t.Errorf("%s status = %q, want succeeded", tc.survivor, status[tc.survivor])
			}
			if len(o.Items) != 1 || o.Items[0].Source+"/"+o.Items[0].Item["id"].(string) != tc.wantSource {
				t.Errorf("items = %+v, want only %s", o.Items, tc.wantSource)
			}
		})
	}
}

func TestInstances_ConfigValidateAndAuthRunEveryProbeWithTheFlag(t *testing.T) {
	logPath := writeInstanceBackend(t, "inst-bin")
	writeInstancesConfig(t, "connector:\n  issue:\n"+twoInstanceList)

	stdout, _, code := executePr(t, []string{"config", "validate"})
	if code != 0 {
		t.Fatalf("config validate exit = %d; stdout=%s", code, stdout)
	}
	for _, name := range []string{"inst-pg2", "inst-zr"} {
		if !strings.Contains(stdout, `"source":"`+name+`"`) {
			t.Errorf("config validate output has no row for %s: %s", name, stdout)
		}
	}
	calls := instanceCalls(t, logPath)
	seen := map[string]bool{}
	for _, c := range calls {
		if !strings.HasPrefix(c, "ARGC=2 ARGV[--beads-dir /example/") {
			t.Errorf("a probe ran without its flag: %q", c)
		}
		for _, op := range []string{"auth_status", "capabilities"} {
			if strings.Contains(c, `"op":"`+op+`"`) {
				seen[op+"@"+strings.TrimSuffix(strings.Fields(c)[2], "]")] = true
			}
		}
	}
	if len(seen) != 4 {
		t.Errorf("want auth_status and capabilities per instance, saw %v in %v", seen, calls)
	}
}

func TestInstances_IssueListFansOutAndPinRunsOnlyThatInstance(t *testing.T) {
	logPath := writeInstanceBackend(t, "inst-bin")
	writeInstancesConfig(t, "connector:\n  issue:\n"+twoInstanceList)

	stdout, _, code := executePr(t, []string{"issue", "list", "--query", "mine"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	var outcome issueListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode: %v (%s)", err, stdout)
	}
	if len(outcome.Sources) != 2 || outcome.Sources[0].Source != "inst-pg2" || outcome.Sources[1].Source != "inst-zr" {
		t.Fatalf("sources = %+v", outcome.Sources)
	}
	if len(outcome.Entities) != 2 {
		t.Fatalf("entities = %+v, want one per instance", outcome.Entities)
	}

	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if _, _, code := executePr(t, []string{"issue", "list", "--query", "mine", "--backend", "inst-zr"}); code != 0 {
		t.Fatalf("pinned exit = %d", code)
	}
	for _, c := range instanceCalls(t, logPath) {
		if !strings.HasPrefix(c, "ARGC=2 ARGV[--beads-dir /example/zr]") {
			t.Errorf("pin to inst-zr ran %q", c)
		}
	}
}

func TestInstances_TargetedTryEach(t *testing.T) {
	writeInstanceBackend(t, "inst-bin")
	writeInstancesConfig(t, "connector:\n  issue:\n"+twoInstanceList)
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}

	// A foreign id answers not_found from inst-pg2; inst-zr is then tried.
	resp, err := DispatchTargeted(context.Background(), reg, "issue", "show", map[string]string{"id": "zr-1"}, "")
	if err != nil {
		t.Fatalf("try-each: %v", err)
	}
	if !strings.Contains(string(resp.Result), `"zr-1"`) {
		t.Errorf("result = %s", resp.Result)
	}

	// Any other error from the first instance short-circuits (documented
	// hazard of id-keyed ops over an unreachable tracker).
	writeInstancesConfig(t, "connector:\n  issue:\n"+
		"    - {name: inst-down, command: [inst-bin, --beads-dir, /example/down]}\n"+
		"    - {name: inst-zr, command: [inst-bin, --beads-dir, /example/zr]}\n")
	reg, err = LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DispatchTargeted(context.Background(), reg, "issue", "show", map[string]string{"id": "zr-1"}, ""); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable short-circuit", err)
	}
}

func TestInstances_AttentionAndSearchSourcesFanOut(t *testing.T) {
	writeInstanceBackend(t, "inst-bin")
	writeInstancesConfig(t, "attention:\n"+twoInstanceSources+"search:\n"+twoInstanceSources)

	for _, args := range [][]string{{"attention", "list"}, {"search", "x"}} {
		stdout, _, _ := executePr(t, args)
		var o struct {
			Sources []map[string]any `json:"sources"`
		}
		if err := json.Unmarshal([]byte(stdout), &o); err != nil {
			t.Fatalf("%v: decode %q: %v", args, stdout, err)
		}
		if len(o.Sources) != 2 || o.Sources[0]["source"] != "inst-pg2" || o.Sources[1]["source"] != "inst-zr" ||
			o.Sources[0]["status"] != "disabled" || o.Sources[1]["status"] != "disabled" {
			t.Errorf("%v: sources = %v, want one disabled (not applicable) row per instance", args, o.Sources)
		}
	}
}

func TestInstances_PlainStringRegistrationIsUnchanged(t *testing.T) {
	logPath := writeInstanceBackend(t, "inst-plain")
	writeInstancesConfig(t, "activity:\n  sources:\n    - inst-plain\n")

	if _, _, code := executePr(t, []string{"activity", "list", "--since", "7d"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	calls := instanceCalls(t, logPath)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "ARGC=0 ARGV[] REQ[{") {
		t.Fatalf("calls = %v, want one call with no arguments", calls)
	}
	req := strings.TrimSuffix(strings.TrimPrefix(calls[0], "ARGC=0 ARGV[] REQ["), "]")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(req), &fields); err != nil {
		t.Fatalf("request %q: %v", req, err)
	}
	for k := range fields {
		if k != "op" && k != "args" && k != "config" {
			t.Errorf("unexpected request field %q", k)
		}
	}
}

// capabilitiesCalls returns the logged backend calls whose request op is
// "capabilities" (bead pg2-h5cmo: the capability probes behind
// cacheEnabled, search --fields and config validate's activity_kinds
// summary are separate call sites from the op-dispatch path).
func capabilitiesCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.Contains(c, `"op":"capabilities"`) {
			out = append(out, c)
		}
	}
	return out
}

// assertEveryCapabilityProbeCarriesTheFlag asserts exactly want capability
// probes ran and every one received its instance's argv (ARGC=2,
// --beads-dir <dir>), not the bare binary.
func assertEveryCapabilityProbeCarriesTheFlag(t *testing.T, logPath string, want int, dirPrefix string) {
	t.Helper()
	probes := capabilitiesCalls(instanceCalls(t, logPath))
	if len(probes) != want {
		t.Fatalf("capability probes = %d (%v), want %d", len(probes), probes, want)
	}
	for _, c := range probes {
		if !strings.HasPrefix(c, "ARGC=2 ARGV[--beads-dir "+dirPrefix) {
			t.Errorf("a capability probe ran without its instance argv: %q", c)
		}
	}
}

func TestInstances_CacheEnabledProbeCarriesTheInstanceArgv(t *testing.T) {
	logPath := writeInstanceBackend(t, "inst-bin")
	writeInstancesConfig(t, "connector:\n  issue:\n"+twoInstanceList)
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}

	enabled, err := cacheEnabled(context.Background(), reg, "issue", "inst-zr")
	if err != nil || !enabled {
		t.Fatalf("cacheEnabled = %v, %v; want true, nil", enabled, err)
	}
	assertEveryCapabilityProbeCarriesTheFlag(t, logPath, 1, "/example/zr]")
}

func TestInstances_SearchFieldsProbeCarriesTheInstanceArgv(t *testing.T) {
	logPath := writeInstanceBackend(t, "inst-bin")
	writeInstancesConfig(t, "search:\n"+twoInstanceSources)

	if _, _, code := executePr(t, []string{"search", "x", "--fields", "not-a-field"}); code != 0 && code != 2 {
		t.Fatalf("search exit = %d", code)
	}
	// One --fields validation probe per queried instance, each with its argv.
	assertEveryCapabilityProbeCarriesTheFlag(t, logPath, 2, "/example/")
}

func TestInstances_ActivityKindsProbeCarriesTheInstanceArgv(t *testing.T) {
	logPath := writeInstanceBackend(t, "inst-bin")
	writeInstancesConfig(t, twoInstanceActivityConfig("/example/zr"))
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}

	// The fake declares no activity_kinds, so the union is empty; the point
	// is that each source's probe ran with its own argv.
	if kinds := activityKindsUnion(context.Background(), reg); kinds != nil {
		t.Fatalf("kinds = %v, want nil", kinds)
	}
	assertEveryCapabilityProbeCarriesTheFlag(t, logPath, 2, "/example/")
}
