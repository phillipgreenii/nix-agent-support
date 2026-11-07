package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// scrapeApp is an app whose environment probes are faked like metricsOut's.
func (e *env) scrapeApp(out, errb *bytes.Buffer) *app {
	a := newApp(out, errb, e.clk)
	a.freeBytes = func(string) (uint64, error) { return e.free, nil }
	a.daemons = func(string) (int, error) { return 0, nil }
	return a
}

func get(t *testing.T, url string) (int, string, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

func TestServeScrapeEqualsMetricsCommandForEmptyPool(t *testing.T) {
	e := newEnv(t)
	var out, errb bytes.Buffer
	srv := httptest.NewServer(e.scrapeApp(&out, &errb).metricsHandler(e.root))
	defer srv.Close()

	code, ctype, body := get(t, srv.URL+"/metrics")
	if code != 200 || !strings.HasPrefix(ctype, "text/plain") {
		t.Fatalf("scrape: %d %q", code, ctype)
	}
	if want := e.metricsOut(0); body != want {
		t.Errorf("scrape differs from the metrics command:\n--- scrape\n%s\n--- metrics\n%s", body, want)
	}
	// An empty pool still emits every state series.
	for _, st := range []string{"warm-free", "leased", "resetting", "quarantined", "retired"} {
		hasLine(t, body, `pg_checkout_pool_slots{state="`+st+`"} 0`)
	}
}

func TestServeRendersAfreshOnEachScrape(t *testing.T) {
	e := newEnv(t)
	var out, errb bytes.Buffer
	srv := httptest.NewServer(e.scrapeApp(&out, &errb).metricsHandler(e.root))
	defer srv.Close()

	_, _, first := get(t, srv.URL+"/metrics")
	hasLine(t, first, `pg_checkout_pool_slots{state="leased"} 0`)
	e.mustLease("s1", "")
	_, _, second := get(t, srv.URL+"/metrics")
	hasLine(t, second, `pg_checkout_pool_slots{state="leased"} 1`)
}

func TestServeOnlyMetricsRoute(t *testing.T) {
	e := newEnv(t)
	var out, errb bytes.Buffer
	srv := httptest.NewServer(e.scrapeApp(&out, &errb).metricsHandler(e.root))
	defer srv.Close()
	if code, _, _ := get(t, srv.URL+"/other"); code != http.StatusNotFound {
		t.Errorf("/other: %d, want 404", code)
	}
}

func TestServeRejectsNonLoopbackAddress(t *testing.T) {
	e := newEnv(t)
	for _, addr := range []string{"0.0.0.0:9148", ":9148", "example.com:9148", "nonsense"} {
		var out, errb bytes.Buffer
		if code := e.scrapeApp(&out, &errb).serve([]string{"--root", e.root, "--addr", addr}, nil); code != exitUsage {
			t.Errorf("%q: exit %d, want %d", addr, code, exitUsage)
		}
	}
}

func TestServeListensOnEphemeralLoopbackPort(t *testing.T) {
	e := newEnv(t)
	var out, errb bytes.Buffer
	a := e.scrapeApp(&out, &errb)
	addrCh := make(chan string, 1)
	done := make(chan int, 1)
	go func() {
		done <- a.serve([]string{"--root", e.root, "--addr", "127.0.0.1:0"}, func(addr string) { addrCh <- addr })
	}()
	select {
	case addr := <-addrCh:
		code, _, body := get(t, "http://"+addr+"/metrics")
		if code != 200 || !strings.Contains(body, "pg_checkout_pool_slots{") {
			t.Fatalf("scrape: %d %q", code, body)
		}
	case c := <-done:
		t.Fatalf("serve exited early with %d: %s", c, errb.String())
	case <-time.After(10 * time.Second):
		t.Fatal("serve never became ready")
	}
	// Leave the goroutine to the test process exit: it stops on SIGTERM only.
}
