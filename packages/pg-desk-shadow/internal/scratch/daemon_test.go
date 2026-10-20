package scratch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
)

// The fixture is a copy of the daemon's own complete example configuration
// (config/testdata/example.json in pg-connector-github); the harness module
// must not import the daemon module, so the key set is pinned by copy.
func exampleKeys(t *testing.T) (top, pr map[string]bool) {
	t.Helper()
	b, err := os.ReadFile("testdata/daemon-config-example.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var prDoc map[string]json.RawMessage
	if err := json.Unmarshal(doc["pr"], &prDoc); err != nil {
		t.Fatal(err)
	}
	top, pr = map[string]bool{}, map[string]bool{}
	for k := range doc {
		top[k] = true
	}
	for k := range prDoc {
		pr[k] = true
	}
	return top, pr
}

func daemonLayout(t *testing.T) Layout {
	t.Helper()
	return Layout{Root: safety.Resolve(t.TempDir())}
}

func TestRenderDaemonConfigUsesOnlyKeysTheDaemonAccepts(t *testing.T) {
	l := daemonLayout(t)
	q, err := ParseDaemonQueries([]byte(`{"mine":"is:pr is:open author:@me","team":["is:pr review-requested:@me","is:pr assignee:@me"],"extra":"is:pr"}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.RenderDaemonConfig([]string{"mine", "team"}, q, 1500)
	if err != nil {
		t.Fatal(err)
	}
	topKeys, prKeys := exampleKeys(t)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	for k := range doc {
		if !topKeys[k] {
			t.Errorf("rendered key %q is not a key of the daemon's example configuration (the loader rejects unknown keys)", k)
		}
	}
	for _, k := range []string{"state_dir", "socket_path", "gh_path", "pr", "graphql_points_per_hour"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("rendered config lacks %q", k)
		}
	}
	var pr map[string]json.RawMessage
	if err := json.Unmarshal(doc["pr"], &pr); err != nil {
		t.Fatal(err)
	}
	for k := range pr {
		if !prKeys[k] {
			t.Errorf("rendered pr key %q is not a key of the daemon's example configuration", k)
		}
	}
	var queries map[string]any
	if err := json.Unmarshal(pr["queries"], &queries); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries["mine"] != "is:pr is:open author:@me" {
		t.Errorf("pr.queries = %v: want only the two watched queries", queries)
	}
	if list, ok := queries["team"].([]any); !ok || len(list) != 2 {
		t.Errorf("team query = %v, want the two-element list", queries["team"])
	}
	var s struct {
		StateDir, SocketPath, GH string
		Points                   int
	}
	_ = json.Unmarshal(doc["state_dir"], &s.StateDir)
	_ = json.Unmarshal(doc["socket_path"], &s.SocketPath)
	_ = json.Unmarshal(doc["gh_path"], &s.GH)
	_ = json.Unmarshal(doc["graphql_points_per_hour"], &s.Points)
	if s.StateDir != l.DaemonStateDir() || s.SocketPath != l.DaemonSocket() || s.GH != filepath.Join(l.BinDir(), "gh") || s.Points != 1500 {
		t.Errorf("rendered values = %+v", s)
	}
	for name, p := range map[string]string{"state_dir": s.StateDir, "socket_path": s.SocketPath, "gh_path": s.GH, "config": l.DaemonConfig(), "log": l.DaemonLog()} {
		if !safety.Under(p, l.Root) {
			t.Errorf("%s %s is not under the scratch directory", name, p)
		}
	}
}

func TestRenderDaemonConfigNamesAMissingQueryNeverItsExpression(t *testing.T) {
	l := daemonLayout(t)
	q := DaemonQueries{"mine": "is:pr secret-expression"}
	_, err := l.RenderDaemonConfig([]string{"mine", "team"}, q, 1500)
	if err == nil || !strings.Contains(err.Error(), `"team"`) {
		t.Fatalf("err = %v, want one naming team", err)
	}
	if strings.Contains(err.Error(), "secret-expression") {
		t.Errorf("error leaks an expression: %v", err)
	}
}

func TestParseDaemonQueriesRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ body, key string }{
		"empty string":     {`{"mine":""}`, "mine"},
		"blank string":     {`{"mine":"  "}`, "mine"},
		"empty list":       {`{"team":[]}`, "team"},
		"blank in list":    {`{"team":["is:pr",""]}`, "team"},
		"number":           {`{"team":3}`, "team"},
		"list of numbers":  {`{"team":[1]}`, "team"},
		"null":             {`{"team":null}`, "team"},
		"not an object":    {`["a"]`, ""},
		"invalid json":     {`{"mine":`, ""},
		"two values":       {`{"mine":"x"} {"team":"y"}`, ""},
		"empty input":      {``, ""},
		"trailing garbage": {`{"mine":"x"} x`, ""},
	} {
		_, err := ParseDaemonQueries([]byte(tc.body))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if tc.key != "" && !strings.Contains(err.Error(), `"`+tc.key+`"`) {
			t.Errorf("%s: error %q does not name the key", name, err)
		}
	}
	if q, err := ParseDaemonQueries([]byte(`{"a":"x","b":["y","z"]}`)); err != nil || len(q) != 2 {
		t.Errorf("valid file refused: %v %v", q, err)
	}
}

func TestValidateDaemonPoints(t *testing.T) {
	for _, n := range []int{1, 1500, 4000, MaxDaemonPointsPerHour} {
		if err := ValidateDaemonPoints(n); err != nil {
			t.Errorf("%d refused: %v", n, err)
		}
	}
	for _, n := range []int{0, -1, MaxDaemonPointsPerHour + 1} {
		if ValidateDaemonPoints(n) == nil {
			t.Errorf("%d accepted", n)
		}
	}
}

func TestValidateSocketPathLimit(t *testing.T) {
	// The socket is <root>/run/pcg.sock.
	fit := "/" + strings.Repeat("a", MaxSocketPathBytes-len("/run/pcg.sock")-1)
	if got := len(Layout{Root: fit}.DaemonSocket()); got != MaxSocketPathBytes {
		t.Fatalf("fixture socket is %d bytes, want %d", got, MaxSocketPathBytes)
	}
	if err := (Layout{Root: fit}).ValidateSocketPath(); err != nil {
		t.Errorf("a %d-byte socket path was refused: %v", MaxSocketPathBytes, err)
	}
	if err := (Layout{Root: fit + "a"}).ValidateSocketPath(); err == nil || !strings.Contains(err.Error(), "socket") {
		t.Errorf("a %d-byte socket path was accepted: %v", MaxSocketPathBytes+1, err)
	}
}

func TestDaemonLayoutPaths(t *testing.T) {
	l := daemonLayout(t)
	if !strings.HasSuffix(l.ConnectorLog(), "pg-connector-github/events.jsonl") || filepath.Dir(l.ConnectorLog()) != l.DaemonStateDir() {
		t.Errorf("ConnectorLog = %s, want <daemon state dir>/events.jsonl", l.ConnectorLog())
	}
	if l.DaemonStateDir() != filepath.Join(l.StateHome(), "pg-connector-github") {
		t.Errorf("DaemonStateDir = %s", l.DaemonStateDir())
	}
	if filepath.Dir(l.DaemonConfig()) != l.ConfigDir() || filepath.Dir(l.DaemonSocket()) != l.RuntimeDir() || filepath.Dir(l.DaemonLog()) != l.LogsDir() {
		t.Errorf("config %s socket %s log %s are not under their directories", l.DaemonConfig(), l.DaemonSocket(), l.DaemonLog())
	}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(l.DaemonStateDir()); err != nil || !fi.IsDir() {
		t.Errorf("MkdirAll did not create the daemon state dir: %v", err)
	}
}

func TestConnectorLogMovesButLiveSourceStays(t *testing.T) {
	// Layout.ConnectorLog is the scratch side only; the live source is whatever
	// the manifest records, untouched by the layout.
	l := daemonLayout(t)
	live := LiveSources{ConnectorEvents: "/h/.local/state/pg-connector-pr-github/events.jsonl"}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	if err := l.Save(Manifest{Home: "/h", Live: live, CreatedAt: "2026-01-05T09:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Live.ConnectorEvents != live.ConnectorEvents || strings.Contains(l.ConnectorLog(), "pr-github") {
		t.Errorf("live = %q, scratch = %q", got.Live.ConnectorEvents, l.ConnectorLog())
	}
}

func TestDaemonCapRoundTripsThroughManifestAndEnvironment(t *testing.T) {
	for _, cap := range []int{DefaultDaemonPointsPerHour, 2500} {
		l := daemonLayout(t)
		if err := l.MkdirAll(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(l.PRConfig(), []byte("x: 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := l.Save(Manifest{Home: filepath.Join(l.Root, "home"), CreatedAt: "2026-01-05T09:00:00Z", DaemonGraphQLPointsPerHour: cap}); err != nil {
			t.Fatal(err)
		}
		m, err := l.Load()
		if err != nil || m.DaemonGraphQLPointsPerHour != cap {
			t.Fatalf("manifest cap = %d (%v), want %d", m.DaemonGraphQLPointsPerHour, err, cap)
		}
		pol := l.Policy(m)
		env := safety.ChildEnv(pol, "/usr/bin:/bin")
		want := "PG_CONNECTOR_GITHUB_GRAPHQL_POINTS_PER_HOUR=" + strconv.Itoa(cap)
		if !contains(env, want) {
			t.Errorf("environment lacks %s: %v", want, env)
		}
		if err := safety.Verify(env, pol); err != nil {
			t.Errorf("cap %d: Verify refused the policy's own environment: %v", cap, err)
		}
	}
}

func TestManifestWithoutACapKeepsTheDaemonDisabled(t *testing.T) {
	l := daemonLayout(t)
	pol := l.Policy(Manifest{Home: "/h"})
	if pol.Daemon {
		t.Error("a manifest that records no daemon cap must leave the daemon checks off")
	}
	for _, kv := range safety.ChildEnv(pol, "/usr/bin") {
		if strings.HasPrefix(kv, "PG_CONNECTOR_GITHUB_") {
			t.Errorf("%s emitted with the daemon disabled", kv)
		}
	}
	b, _ := json.Marshal(Manifest{})
	if strings.Contains(string(b), "daemon_graphql_points_per_hour") {
		t.Errorf("an unset cap must be absent from the manifest: %s", b)
	}
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}
