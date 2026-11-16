package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionAndUsage(t *testing.T) {
	var out, errb bytes.Buffer
	a := newApp(&out, &errb)
	if code := a.run([]string{"version"}); code != 0 || strings.TrimSpace(out.String()) != Version {
		t.Fatalf("version: %d %q", code, out.String())
	}
	if code := a.run(nil); code != 2 {
		t.Fatalf("no args exit %d", code)
	}
	if code := a.run([]string{"status"}); code != 2 {
		t.Fatalf("removed command exit %d", code)
	}
}

func TestServeRendersBuildInfo(t *testing.T) {
	var out, errb bytes.Buffer
	srv := httptest.NewServer(newApp(&out, &errb).metricsHandler())
	defer srv.Close()
	code, ctype, body := get(t, srv.URL+"/metrics")
	if code != 200 || !strings.HasPrefix(ctype, "text/plain") {
		t.Fatalf("scrape: %d %q", code, ctype)
	}
	if !strings.Contains(body, "pg_gwm_build_info{version=") || strings.Count(body, "pg_gwm_") != 3 {
		t.Errorf("unexpected exposition:\n%s", body)
	}
}

func TestServeRejectsNonLoopback(t *testing.T) {
	var out, errb bytes.Buffer
	if code := newApp(&out, &errb).serve([]string{"--addr", "0.0.0.0:0"}, nil); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
