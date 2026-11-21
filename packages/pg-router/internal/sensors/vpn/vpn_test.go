package vpn

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/sensors/levels"
	"github.com/phillipgreenii/pg-router/internal/sensors/sensorcli"
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
	if got := strings.Join(s.Resources(), ","); got != "network_corp" {
		t.Errorf("resources %s", got)
	}
	if b := s.Bands(); len(b) != 1 || b[0].Resource != "network_corp" || len(b[0].Bands) != 0 {
		t.Errorf("bands %+v", b)
	}
	if !s.Reach.SkipVerify {
		t.Errorf("reach must skip verification: %+v", s.Reach)
	}
	// The retired trust_resource key is accepted and ignored (config compat).
	good["trust_resource"] = "network_zr_trust"
	s = New()
	if err := s.Configure(cfgOf(good)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Resources(), ","); got != "network_corp" {
		t.Errorf("resources %s", got)
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

// A private CA this machine does not trust: still reachable.
func TestSelfSignedReachable(t *testing.T) {
	srv := headServer()
	defer srv.Close()
	s := configured(t, strings.TrimPrefix(srv.URL, "https://"))
	env := newEnv()
	for i := 0; i < 5; i++ {
		got := levelsOf(t, s, env)
		if got["network_zr"] != "AVAILABLE" || len(got) != 1 {
			t.Fatalf("pass %d: reachability %v", i, got)
		}
	}
}

// A closed port: down after 3 consecutive passes.
func TestClosedPortDown(t *testing.T) {
	srv := headServer()
	host := strings.TrimPrefix(srv.URL, "https://")
	srv.Close()
	s := configured(t, host)
	env := newEnv()
	var got map[string]string
	for i := 0; i < 3; i++ {
		got = levelsOf(t, s, env)
		if i < 2 && got["network_zr"] != "AVAILABLE" {
			t.Fatalf("pass %d: %v", i, got)
		}
	}
	if got["network_zr"] != "UNAVAILABLE" || len(got) != 1 {
		t.Fatalf("%v", got)
	}
	if env.Store.Extra("network_zr")["net_failures"] != "3" {
		t.Errorf("counter %v", env.Store.Extra("network_zr"))
	}
}

// The retired trust resource is no longer answered.
func TestTrustResourceRetired(t *testing.T) {
	srv := headServer()
	defer srv.Close()
	s := configured(t, strings.TrimPrefix(srv.URL, "https://"))
	env := newEnv()
	env.Resource = "network_zr_trust"
	if _, err := s.Measure(context.Background(), env); err != levels.ErrNoReading {
		t.Errorf("err %v", err)
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
	s.Reach.Client = srv.Client()
	var stderr bytes.Buffer
	env := sensorcli.Env{Store: levels.NewMemoryStore(), Stderr: &stderr, Now: time.Now()}
	obs, err := s.Measure(context.Background(), env)
	if err != nil || len(obs) != 1 || obs[0].Resource != "network_corp" || obs[0].Level != "AVAILABLE" {
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

func TestMeasureTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	s := New()
	if err := s.Configure(cfgOf(map[string]any{"resource": "network_zr", "hosts": []any{strings.TrimPrefix(srv.URL, "https://")}, "timeout": "400ms"})); err != nil {
		t.Fatal(err)
	}
	got := levelsOf(t, s, newEnv())
	if len(got) != 1 {
		t.Errorf("%v", got)
	}
}
