package netprobe

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

func status(code int) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			http.Error(w, "want HEAD", 405)
			return
		}
		if code/100 == 3 {
			w.Header().Set("Location", "https://elsewhere.invalid/")
		}
		w.WriteHeader(code)
	}))
}

func TestProbeStatuses(t *testing.T) {
	for code, want := range map[int]bool{200: true, 302: true, 404: true, 499: true, 500: false, 503: false} {
		srv := status(code)
		p := &Prober{Client: srv.Client(), Timeout: 2 * time.Second}
		r := p.Probe(context.Background(), []string{srv.URL})[0]
		srv.Close()
		if r.Reachable != want || r.Status != code {
			t.Errorf("status %d: %+v, want reachable=%v", code, r, want)
		}
	}
}

func TestProbeTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	p := &Prober{Client: srv.Client(), Timeout: 50 * time.Millisecond}
	r := p.Probe(context.Background(), []string{srv.URL})[0]
	if r.Reachable || r.Err == nil {
		t.Fatalf("timeout must be unreachable: %+v", r)
	}
}

func TestProbeRefused(t *testing.T) {
	srv := status(200)
	url, client := srv.URL, srv.Client()
	srv.Close()
	r := (&Prober{Client: client, Timeout: time.Second}).Probe(context.Background(), []string{url})[0]
	if r.Reachable || r.Err == nil {
		t.Fatalf("refused must be unreachable: %+v", r)
	}
}

// A captive portal answers with a certificate the client does not trust.
func TestProbeBadCertificate(t *testing.T) {
	srv := status(200)
	defer srv.Close()
	r := (&Prober{Timeout: time.Second}).Probe(context.Background(), []string{srv.URL})[0]
	if r.Reachable || r.Err == nil {
		t.Fatalf("untrusted certificate must be unreachable: %+v", r)
	}
	if !strings.Contains(r.Err.Error(), "certificate") {
		t.Errorf("want a certificate error, got %v", r.Err)
	}
}

// SkipVerify accepts the untrusted certificate but still needs an HTTP
// response; the zero value keeps verifying.
func TestProbeSkipVerify(t *testing.T) {
	srv := status(200)
	defer srv.Close()
	r := (&Prober{Timeout: time.Second, SkipVerify: true}).Probe(context.Background(), []string{srv.URL})[0]
	if !r.Reachable || r.Err != nil || r.Status != 200 {
		t.Fatalf("skip-verify must reach an untrusted certificate: %+v", r)
	}
	r = (&Prober{Timeout: time.Second}).Probe(context.Background(), []string{srv.URL})[0]
	if r.Reachable {
		t.Fatalf("the zero value must verify: %+v", r)
	}
	// A custom client's transport is cloned, not mutated.
	tr := srv.Client().Transport.(*http.Transport)
	p := &Prober{Client: srv.Client(), Timeout: time.Second, SkipVerify: true}
	if r := p.Probe(context.Background(), []string{srv.URL})[0]; !r.Reachable {
		t.Fatalf("%+v", r)
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("the caller's transport must not be modified")
	}
	// A 5xx over an unverified handshake is still unreachable.
	s5 := status(503)
	defer s5.Close()
	if r := (&Prober{Timeout: time.Second, SkipVerify: true}).Probe(context.Background(), []string{s5.URL})[0]; r.Reachable {
		t.Fatalf("503 must be unreachable: %+v", r)
	}
}

func TestProbeBadURL(t *testing.T) {
	r := (&Prober{}).Probe(context.Background(), []string{"http://[::1"})[0]
	if r.Reachable || r.Err == nil {
		t.Fatalf("%+v", r)
	}
}

func TestProbeDefaultClientAndTimeout(t *testing.T) {
	srv := status(200)
	defer srv.Close()
	// Default client does not trust the test certificate; default timeout applies.
	r := (&Prober{}).Probe(context.Background(), []string{srv.URL})[0]
	if r.Reachable {
		t.Fatal("default client must verify")
	}
}

func env(raw bool) (sensorcli.Env, *bytes.Buffer) {
	var buf bytes.Buffer
	return sensorcli.Env{Raw: raw, Store: levels.NewMemoryStore(), Stderr: &buf}, &buf
}

func TestObserveThreeConsecutivePasses(t *testing.T) {
	srv := status(500)
	defer srv.Close()
	p := &Prober{Client: srv.Client(), Timeout: time.Second}
	e, stderr := env(false)
	for i := 1; i <= 4; i++ {
		o := Observe(context.Background(), p, e, "network", []string{srv.URL}, 3)
		want := "AVAILABLE"
		if i >= 3 {
			want = "UNAVAILABLE"
		}
		if o.Level != want {
			t.Fatalf("pass %d: %s, want %s", i, o.Level, want)
		}
		if o.Reading == nil || o.Reading.Value != 0 {
			t.Errorf("reading %+v", o.Reading)
		}
	}
	if !strings.Contains(stderr.String(), "status 500") {
		t.Errorf("stderr %q", stderr)
	}
}

func TestObserveSuccessResetsCounter(t *testing.T) {
	bad, good := status(500), status(200)
	defer bad.Close()
	defer good.Close()
	pb := &Prober{Client: bad.Client(), Timeout: time.Second}
	pg := &Prober{Client: good.Client(), Timeout: time.Second}
	e, _ := env(false)
	Observe(context.Background(), pb, e, "network", []string{bad.URL}, 3)
	Observe(context.Background(), pb, e, "network", []string{bad.URL}, 3)
	if e.Store.Extra("network")[FailKey] != "2" {
		t.Fatalf("counter %v", e.Store.Extra("network"))
	}
	o := Observe(context.Background(), pg, e, "network", []string{good.URL}, 3)
	if o.Level != "AVAILABLE" || o.Reading.Value != 1 {
		t.Fatalf("%+v", o)
	}
	if _, has := e.Store.Extra("network")[FailKey]; has {
		t.Fatal("counter must be cleared")
	}
	// One reachable target among failures is enough.
	o = Observe(context.Background(), pg, e, "network", []string{bad.URL, good.URL}, 1)
	if o.Level != "AVAILABLE" {
		t.Fatalf("%+v", o)
	}
}

func TestObserveRawIsImmediate(t *testing.T) {
	srv := status(500)
	defer srv.Close()
	e, _ := env(true)
	o := Observe(context.Background(), &Prober{Client: srv.Client(), Timeout: time.Second}, e, "network", []string{srv.URL}, 3)
	if o.Level != "UNAVAILABLE" {
		t.Fatalf("%+v", o)
	}
	if len(e.Store.Extra("network")) != 0 {
		t.Error("raw must not keep a counter")
	}
}

func TestHostURLsAndValidHost(t *testing.T) {
	got := HostURLs([]string{"a.example", "b.example"})
	if got[0] != "https://a.example/" || got[1] != "https://b.example/" {
		t.Fatalf("%v", got)
	}
	for h, want := range map[string]bool{"a.example": true, "": false, "https://x": false, "a b": false, "a/b": false} {
		if ValidHost(h) != want {
			t.Errorf("ValidHost(%q) != %v", h, want)
		}
	}
}

func cfgOf(raw map[string]any) sensorcli.Config { return sensorcli.Config{Raw: raw} }

func TestConfigureCommon(t *testing.T) {
	p, n := &Prober{}, 3
	if err := ConfigureCommon(cfgOf(map[string]any{"t": "2s", "f": int64(5)}), "t", "f", p, &n); err != nil || p.Timeout != 2*time.Second || n != 5 {
		t.Fatalf("%v %v %d", err, p.Timeout, n)
	}
	if err := ConfigureCommon(cfgOf(nil), "t", "f", p, &n); err != nil || p.Timeout != DefaultTimeout || n != 5 {
		t.Fatalf("defaults: %v %v %d", err, p.Timeout, n)
	}
	for _, raw := range []map[string]any{
		{"t": "0s"}, {"t": "bogus"}, {"t": int64(1)}, {"f": int64(0)}, {"f": "3"},
	} {
		if err := ConfigureCommon(cfgOf(raw), "t", "f", &Prober{}, new(int)); err == nil {
			t.Errorf("%v must fail", raw)
		}
	}
	_ = errors.New
}
