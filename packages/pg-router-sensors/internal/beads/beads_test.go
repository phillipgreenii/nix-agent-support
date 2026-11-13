package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/schemas"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

func cfgOf(raw map[string]any) sensorcli.Config { return sensorcli.Config{Raw: raw} }

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func configured(t *testing.T, url string) *Sensor {
	t.Helper()
	s := New()
	err := s.Configure(cfgOf(map[string]any{
		"exporter_url": url,
		"trackers":     map[string]any{"tracker_a": "alpha", "tracker_b": "beta"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func measure(t *testing.T, s *Sensor, resource string) ([]levels.Observation, string, error) {
	t.Helper()
	var errb bytes.Buffer
	obs, err := s.Measure(context.Background(), sensorcli.Env{Resource: resource, Stderr: &errb})
	return obs, errb.String(), err
}

func levelsOf(obs []levels.Observation) string {
	var parts []string
	for _, o := range obs {
		parts = append(parts, o.Resource+"="+o.Level)
	}
	return strings.Join(parts, ",")
}

func TestHealthyExporterSample(t *testing.T) {
	srv := serve(t, 200, fixture(t, "exporter-healthy.prom"))
	obs, _, err := measure(t, configured(t, srv.URL), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := levelsOf(obs); got != "beads_tracker_a=UP,beads_tracker_b=UP" {
		t.Errorf("got %s", got)
	}
}

func TestFailedExporterSample(t *testing.T) {
	srv := serve(t, 200, fixture(t, "exporter-failed.prom"))
	obs, _, err := measure(t, configured(t, srv.URL), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := levelsOf(obs); got != "beads_tracker_a=DOWN,beads_tracker_b=DOWN" {
		t.Errorf("got %s", got)
	}
}

func TestPerDBFailure(t *testing.T) {
	body := fixture(t, "exporter-healthy.prom")
	body = strings.Replace(body, `beads_exporter_up{db="beta"} 1`, `beads_exporter_up{db="beta"} 0`, 1)
	srv := serve(t, 200, body)
	obs, _, err := measure(t, configured(t, srv.URL), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := levelsOf(obs); got != "beads_tracker_a=UP,beads_tracker_b=DOWN" {
		t.Errorf("got %s", got)
	}
}

func TestSingleSeriesDBX(t *testing.T) {
	srv := serve(t, 200, "beads_exporter_up{db=\"x\"} 0\n")
	s := New()
	if err := s.Configure(cfgOf(map[string]any{"exporter_url": srv.URL, "trackers": map[string]any{"t": "x"}})); err != nil {
		t.Fatal(err)
	}
	obs, _, err := measure(t, s, "")
	if err != nil || levelsOf(obs) != "beads_t=DOWN" {
		t.Errorf("%v %v", levelsOf(obs), err)
	}
}

func TestMissingSeriesEmitsNoLine(t *testing.T) {
	srv := serve(t, 200, "beads_exporter_up{db=\"alpha\"} 1\n")
	obs, stderr, err := measure(t, configured(t, srv.URL), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := levelsOf(obs); got != "beads_tracker_a=UP" {
		t.Errorf("got %s", got)
	}
	if !strings.Contains(stderr, "beta") {
		t.Errorf("stderr %q", stderr)
	}
}

func TestResourceFilter(t *testing.T) {
	srv := serve(t, 200, fixture(t, "exporter-healthy.prom"))
	obs, _, _ := measure(t, configured(t, srv.URL), "beads_tracker_b")
	if got := levelsOf(obs); got != "beads_tracker_b=UP" {
		t.Errorf("got %s", got)
	}
}

func TestFetchFailures(t *testing.T) {
	for _, st := range []int{404, 500, 503} {
		srv := serve(t, st, "beads_exporter_up{db=\"alpha\"} 1\n")
		if _, _, err := measure(t, configured(t, srv.URL), ""); err == nil || !strings.Contains(err.Error(), "status") {
			t.Errorf("status %d: err %v", st, err)
		}
	}
	// Refused: a closed server.
	srv := serve(t, 200, "")
	url := srv.URL
	srv.Close()
	if _, _, err := measure(t, configured(t, url), ""); err == nil {
		t.Error("refused must fail")
	}
	// Timeout.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	s := configured(t, slow.URL)
	s.Timeout = 50 * time.Millisecond
	if _, _, err := measure(t, s, ""); err == nil {
		t.Error("timeout must fail")
	}
}

func TestParse(t *testing.T) {
	text := strings.Join([]string{
		"# TYPE beads_exporter_up gauge",
		`beads_exporter_up{db="a"} 1`,
		`beads_exporter_up{db="b",extra="q\"x"} 0`,
		`beads_exporter_up{extra="1"} 1`,
		`beads_exporter_up{db="c"} 0.5`,
		`beads_exporter_up{db="d"} nope`,
		`beads_exporter_up{db="e"}`,
		`beads_exporter_up{db="f"`,
		`beads_exporter_up{db=bad} 1`,
		`beads_exporter_up{db="g"} 1`,
		`beads_exporter_up{db="g"} 0`,
		`beads_exporter_up{db="g"} 1`,
		`beads_exporter_up_other{db="z"} 1`,
		`beads_exporter_up{db="h} 1`,
	}, "\n")
	got := Parse(text)
	want := map[string]bool{"a": true, "b": false, "g": false}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v", k, got[k])
		}
	}
}

func TestConfigureRejects(t *testing.T) {
	good := func() map[string]any {
		return map[string]any{"exporter_url": "http://127.0.0.1:1/metrics", "trackers": map[string]any{"t": "x"}}
	}
	for name, mut := range map[string]func(map[string]any){
		"unknown key":  func(m map[string]any) { m["bogus"] = "x" },
		"no url":       func(m map[string]any) { delete(m, "exporter_url") },
		"url type":     func(m map[string]any) { m["exporter_url"] = int64(1) },
		"bad scheme":   func(m map[string]any) { m["exporter_url"] = "ftp://h/x" },
		"no host":      func(m map[string]any) { m["exporter_url"] = "http://" },
		"bad timeout":  func(m map[string]any) { m["timeout"] = "x" },
		"zero timeout": func(m map[string]any) { m["timeout"] = "0s" },
		"no trackers":  func(m map[string]any) { delete(m, "trackers") },
		"empty table":  func(m map[string]any) { m["trackers"] = map[string]any{} },
		"table type":   func(m map[string]any) { m["trackers"] = "x" },
		"value type":   func(m map[string]any) { m["trackers"] = map[string]any{"t": int64(1)} },
		"empty db":     func(m map[string]any) { m["trackers"] = map[string]any{"t": ""} },
		"bad name":     func(m map[string]any) { m["trackers"] = map[string]any{"a-b": "x"} },
	} {
		m := good()
		mut(m)
		if err := New().Configure(cfgOf(m)); err == nil {
			t.Errorf("%s: must fail", name)
		}
	}
	s := New()
	m := good()
	m["timeout"] = "2s"
	if err := s.Configure(cfgOf(m)); err != nil || s.Timeout != 2*time.Second {
		t.Fatalf("%v %v", err, s.Timeout)
	}
	if b := s.Bands(); len(b) != 1 || b[0].Resource != "beads_t" || len(b[0].Bands) != 0 {
		t.Errorf("bands %+v", b)
	}
}

func TestUnconfigured(t *testing.T) {
	s := New()
	if len(s.Resources()) != 0 || s.Bands() != nil {
		t.Error("unconfigured sensor answers nothing")
	}
	if _, err := s.Measure(context.Background(), sensorcli.Env{}); err != levels.ErrNoReading {
		t.Errorf("err %v", err)
	}
}

// End to end through the CLI: JSONL lines validate against the schema.
func TestRunAnswersValidateAgainstSchema(t *testing.T) {
	for file, want := range map[string]string{"exporter-healthy.prom": "UP", "exporter-failed.prom": "DOWN"} {
		srv := serve(t, 200, fixture(t, file))
		dir := t.TempDir()
		cfg := dir + "/c.toml"
		toml := "exporter_url = \"" + srv.URL + "\"\n[trackers]\ntracker_a = \"alpha\"\ntracker_b = \"beta\"\n"
		if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errb bytes.Buffer
		code := sensorcli.Run([]string{"beads-sensor", "--config", cfg, "--raw"}, sensorcli.Registry{"beads-sensor": New()},
			sensorcli.IO{Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }})
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", file, code, errb.String())
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("lines %v", lines)
		}
		for _, l := range lines {
			if err := schemas.ValidateLevelAnswer([]byte(l)); err != nil {
				t.Errorf("schema: %v: %s", err, l)
			}
			var a struct{ Level string }
			_ = json.Unmarshal([]byte(l), &a)
			if a.Level != want {
				t.Errorf("%s: level %s", file, a.Level)
			}
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	srv := serve(t, 500, "")
	dir := t.TempDir()
	cfg := dir + "/c.toml"
	_ = os.WriteFile(cfg, []byte("exporter_url = \""+srv.URL+"\"\n[trackers]\nt = \"x\"\n"), 0o600)
	reg := sensorcli.Registry{"beads-sensor": New()}
	run := func(args ...string) int {
		var out, errb bytes.Buffer
		return sensorcli.Run(append([]string{"beads-sensor"}, args...), reg, sensorcli.IO{Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }})
	}
	if c := run("--config", cfg, "--raw"); c != 3 {
		t.Errorf("non-200: exit %d", c)
	}
	if c := run("--config", cfg, "--raw", "--resource", "beads_other"); c != 2 {
		t.Errorf("unconfigured resource: exit %d", c)
	}
}
