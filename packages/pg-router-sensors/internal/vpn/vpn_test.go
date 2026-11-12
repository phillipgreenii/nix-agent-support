package vpn

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

func cfgOf(raw map[string]any) sensorcli.Config { return sensorcli.Config{Raw: raw} }

func TestConfigureRejects(t *testing.T) {
	good := map[string]any{"resource": "network_corp", "hosts": []any{"a.example"}}
	for name, mut := range map[string]func(map[string]any){
		"unknown key":   func(m map[string]any) { m["bogus"] = "x" },
		"no resource":   func(m map[string]any) { delete(m, "resource") },
		"bad resource":  func(m map[string]any) { m["resource"] = "corp" },
		"bare prefix":   func(m map[string]any) { m["resource"] = "network_" },
		"resource type": func(m map[string]any) { m["resource"] = int64(1) },
		"no hosts":      func(m map[string]any) { delete(m, "hosts") },
		"empty hosts":   func(m map[string]any) { m["hosts"] = []any{} },
		"url host":      func(m map[string]any) { m["hosts"] = []any{"https://a.example"} },
		"hosts type":    func(m map[string]any) { m["hosts"] = "a.example" },
		"bad timeout":   func(m map[string]any) { m["timeout"] = "x" },
		"bad trust":     func(m map[string]any) { m["trust_resource"] = "trust" },
		"bare trust":    func(m map[string]any) { m["trust_resource"] = "network_" },
		"same trust":    func(m map[string]any) { m["trust_resource"] = "network_corp" },
		"trust type":    func(m map[string]any) { m["trust_resource"] = int64(1) },
	} {
		m := map[string]any{}
		for k, v := range good {
			m[k] = v
		}
		mut(m)
		if err := New().Configure(cfgOf(m)); err == nil {
			t.Errorf("%s: must fail", name)
		}
	}
	s := New()
	if err := s.Configure(cfgOf(good)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Resources(), ","); got != "network_corp,network_zr_trust" {
		t.Errorf("resources %s", got)
	}
	if b := s.Bands(); len(b) != 2 || b[0].Resource != "network_corp" || b[1].Resource != "network_zr_trust" || len(b[0].Bands)+len(b[1].Bands) != 0 {
		t.Errorf("bands %+v", b)
	}
	good["trust_resource"] = "network_corp_trust"
	s = New()
	if err := s.Configure(cfgOf(good)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Resources(), ","); got != "network_corp,network_corp_trust" {
		t.Errorf("resources %s", got)
	}
	if !s.Reach.SkipVerify || s.Prober.SkipVerify {
		t.Errorf("reach must skip verification, trust must not: %+v %+v", s.Reach, s.Prober)
	}
}

func TestUnconfigured(t *testing.T) {
	s := New()
	if s.Resources() != nil || s.Bands() != nil {
		t.Error("unconfigured sensor answers nothing")
	}
	if _, err := s.Measure(context.Background(), sensorcli.Env{}); err != levels.ErrNoReading {
		t.Errorf("err %v", err)
	}
}

func configured(t *testing.T, host string) *Sensor {
	t.Helper()
	s := New()
	if err := s.Configure(cfgOf(map[string]any{"resource": "network_zr", "hosts": []any{host}, "timeout": "2s"})); err != nil {
		t.Fatal(err)
	}
	return s
}

func levelsOf(t *testing.T, s *Sensor, env sensorcli.Env) map[string]string {
	t.Helper()
	obs, err := s.Measure(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, o := range obs {
		m[o.Resource] = o.Level
	}
	return m
}

func newEnv() sensorcli.Env {
	return sensorcli.Env{Store: levels.NewMemoryStore(), Stderr: &bytes.Buffer{}, Now: time.Now()}
}

func headServer() *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
}

// A private CA this machine does not trust: reachable, not trusted.
func TestSelfSignedReachableNotTrusted(t *testing.T) {
	srv := headServer()
	defer srv.Close()
	s := configured(t, strings.TrimPrefix(srv.URL, "https://"))
	env := newEnv()
	for i := 0; i < 5; i++ {
		got := levelsOf(t, s, env)
		if got["network_zr"] != "AVAILABLE" {
			t.Fatalf("pass %d: reachability %v", i, got)
		}
		wantTrust := "AVAILABLE"
		if i >= 2 {
			wantTrust = "UNAVAILABLE"
		}
		if got["network_zr_trust"] != wantTrust {
			t.Fatalf("pass %d: trust %v, want %s", i, got, wantTrust)
		}
	}
}

// A certificate the client trusts: both up.
func TestVerifiedServerBothUp(t *testing.T) {
	srv := headServer()
	defer srv.Close()
	s := configured(t, strings.TrimPrefix(srv.URL, "https://"))
	s.Prober.Client, s.Reach.Client = srv.Client(), srv.Client()
	got := levelsOf(t, s, newEnv())
	if got["network_zr"] != "AVAILABLE" || got["network_zr_trust"] != "AVAILABLE" || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

// A closed port: both down after 3 consecutive passes, with separate counters.
func TestClosedPortBothDown(t *testing.T) {
	srv := headServer()
	host := strings.TrimPrefix(srv.URL, "https://")
	srv.Close()
	s := configured(t, host)
	env := newEnv()
	var got map[string]string
	for i := 0; i < 3; i++ {
		got = levelsOf(t, s, env)
		if i < 2 && (got["network_zr"] != "AVAILABLE" || got["network_zr_trust"] != "AVAILABLE") {
			t.Fatalf("pass %d: %v", i, got)
		}
	}
	if got["network_zr"] != "UNAVAILABLE" || got["network_zr_trust"] != "UNAVAILABLE" {
		t.Fatalf("%v", got)
	}
	for _, r := range []string{"network_zr", "network_zr_trust"} {
		if env.Store.Extra(r)["net_failures"] != "3" {
			t.Errorf("%s counter %v", r, env.Store.Extra(r))
		}
	}
}

// --resource limits the probe to one of the two resources.
func TestResourceLimit(t *testing.T) {
	srv := headServer()
	defer srv.Close()
	s := configured(t, strings.TrimPrefix(srv.URL, "https://"))
	for _, r := range []string{"network_zr", "network_zr_trust"} {
		env := newEnv()
		env.Resource = r
		got := levelsOf(t, s, env)
		if len(got) != 1 || got[r] == "" {
			t.Errorf("%s: %v", r, got)
		}
	}
}

func TestMeasure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.Method != http.MethodHead {
			t.Errorf("probe %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	s := New()
	if err := s.Configure(cfgOf(map[string]any{"resource": "network_corp", "hosts": []any{host}, "timeout": "2s"})); err != nil {
		t.Fatal(err)
	}
	s.Prober.Client, s.Reach.Client = srv.Client(), srv.Client()
	var stderr bytes.Buffer
	env := sensorcli.Env{Store: levels.NewMemoryStore(), Stderr: &stderr, Now: time.Now()}
	obs, err := s.Measure(context.Background(), env)
	if err != nil || len(obs) != 2 || obs[0].Resource != "network_corp" || obs[0].Level != "AVAILABLE" || obs[1].Resource != "network_zr_trust" || obs[1].Level != "AVAILABLE" {
		t.Fatalf("%+v %v", obs, err)
	}
	env.Resource = "other"
	if _, err := s.Measure(context.Background(), env); err != levels.ErrNoReading {
		t.Errorf("other resource: %v", err)
	}
	// Unreachable host (server closed): three consecutive passes.
	srv.Close()
	env.Resource = ""
	var last string
	for i := 0; i < 3; i++ {
		obs, _ = s.Measure(context.Background(), env)
		last = obs[0].Level
	}
	if last != "UNAVAILABLE" {
		t.Errorf("after 3 failing passes: %s", last)
	}
}

// Both probes run concurrently: against a server that never answers, a
// measure takes about one timeout, not two.
func TestMeasureProbesConcurrently(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	s := New()
	if err := s.Configure(cfgOf(map[string]any{"resource": "network_zr", "hosts": []any{strings.TrimPrefix(srv.URL, "https://")}, "timeout": "400ms"})); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := levelsOf(t, s, newEnv())
	if el := time.Since(start); el >= 750*time.Millisecond {
		t.Errorf("measure took %v, want about one 400ms timeout", el)
	}
	if len(got) != 2 {
		t.Errorf("%v", got)
	}
}
