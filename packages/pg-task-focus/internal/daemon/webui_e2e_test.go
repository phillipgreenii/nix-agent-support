package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

// The web UI's own modules (its store, controller, API client and views, drawn
// into a fake DOM) run under node against this real daemon, through a proxy that
// records every exchange. Afterwards every request the page made is checked
// against api/openapi.yaml, as the daemon's other end-to-end tests do for the
// command line. A field the page names wrongly, a body the contract does not
// allow, a request without its id: each fails here and not in the operator's
// morning. The script is web/test/e2e.mjs; what it asserts about the page is
// there, and what it asserts about the wire is here.

type exchange struct {
	method, path string
	reqHeader    http.Header
	req          []byte
	status       int
	contentType  string
	resp         []byte
}

type capture struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (c *capture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.buf.Len() < 1<<20 {
		c.buf.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

func (c *capture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func nodeOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("PG_TASK_FOCUS_REQUIRE_NODE") == "1" {
			t.Fatalf("node is not on PATH and PG_TASK_FOCUS_REQUIRE_NODE=1: the web UI cannot go untested against the real daemon here")
		}
		t.Skip("node is not on PATH; the web UI end-to-end test needs it (PG_TASK_FOCUS_REQUIRE_NODE=1 makes this a failure)")
	}
	return p
}

func TestWebUIAgainstARealDaemon(t *testing.T) {
	node := nodeOrSkip(t)
	fs := storefault.New(nil)
	e := newEnv(t, options{fs: fs})

	// A proxy in front of the daemon that records every exchange. The daemon's
	// Host allowlist wants its own address, so the proxy presents that.
	target, err := url.Parse(e.url(""))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []exchange
	rp := &httputil.ReverseProxy{
		Rewrite:       func(r *httputil.ProxyRequest) { r.SetURL(target); r.Out.Host = target.Host },
		FlushInterval: -1,
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		hdr := r.Header.Clone()
		cw := &capture{ResponseWriter: w}
		rp.ServeHTTP(cw, r)
		mu.Lock()
		seen = append(seen, exchange{method: r.Method, path: r.URL.Path, reqHeader: hdr, req: body, status: cw.status, contentType: cw.Header().Get("Content-Type"), resp: cw.buf.Bytes()})
		mu.Unlock()
	}))
	defer proxy.Close()

	// The control server: the fake clock, and a fault that makes the store read-only.
	mux := http.NewServeMux()
	mux.HandleFunc("/clock", func(w http.ResponseWriter, r *http.Request) {
		at, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("at"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		e.clock.Set(at)
		w.WriteHeader(204)
	})
	mux.HandleFunc("/fault", func(w http.ResponseWriter, _ *http.Request) {
		fs.Inject(storefault.Rule{Op: storefault.OpSync, Name: logFile})
		w.WriteHeader(204)
	})
	control := httptest.NewServer(mux)
	defer control.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test", "test/e2e.mjs")
	cmd.Dir = "../../web"
	cmd.Env = append(os.Environ(), "PGTF_BASE="+proxy.URL, "PGTF_CONTROL="+control.URL)
	cmd.WaitDelay = 5 * time.Second // a child that holds the pipe open must not hold the test
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	if runErr != nil || !regexp.MustCompile(`(?m)^ℹ fail 0$`).MatchString(out.String()) {
		t.Fatalf("the page against the real daemon failed: %v\n%s\ndaemon log:\n%s", runErr, out.String(), tail(e.log.String(), 4000))
	}
	t.Log(lastLines(out.String(), 9))

	// The wire: what the page sent and what the daemon answered, against the contract.
	mu.Lock()
	defer mu.Unlock()
	pageMutations, pageReads := 0, 0
	paths := map[string]bool{}
	for _, x := range seen {
		if x.path == "/" || strings.HasPrefix(x.path, "/assets/") || x.path == "/api/v1/stream" {
			continue
		}
		tmpl, ok := e.spec.TemplateOf(x.path)
		if !ok {
			t.Errorf("%s %s is not a path of the OpenAPI document", x.method, x.path)
			continue
		}
		client := x.reqHeader.Get("X-Client")
		if client != "" && client != "web" && client != "cli" {
			t.Errorf("%s %s: X-Client %q is not one of the closed set", x.method, x.path, client)
		}
		if client == "web" {
			if x.reqHeader.Get("Traceparent") != "" {
				t.Errorf("%s %s: the browser has no tracing of its own, yet it sent a traceparent", x.method, x.path)
			}
			if x.reqHeader.Get("Origin") != "" && x.method == "GET" {
				t.Errorf("%s %s: a same-origin read sent an Origin", x.method, x.path)
			}
			paths[tmpl] = true
		}
		if x.method == "POST" {
			if ct := x.reqHeader.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("POST %s: content type %q", x.path, ct)
			}
			e.spec.ValidateRequest(t, x.method, tmpl, x.req)
			if client == "web" {
				var b map[string]any
				_ = json.Unmarshal(x.req, &b)
				if b["dry_run"] == nil {
					pageMutations++
					id, _ := b["id"].(string)
					if !regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`).MatchString(id) {
						t.Errorf("POST %s from the page carries id %q, not a ULID", x.path, id)
					}
				}
			}
		} else if client == "web" {
			pageReads++
		}
		if strings.HasPrefix(x.contentType, "application/") {
			e.spec.ValidateResponse(t, x.method, tmpl, x.status, x.contentType, x.resp)
		}
	}
	if pageMutations < 25 || pageReads < 20 {
		t.Errorf("the scripted day made %d mutations and %d reads through the page's client; it should be a full day", pageMutations, pageReads)
	}
	for _, want := range []string{"/api/v1/periods/change", "/api/v1/tasks/{id}/complete", "/api/v1/tasks/{id}/skip", "/api/v1/cycles/start", "/api/v1/cycles/pause", "/api/v1/cycles/resume", "/api/v1/cycles/stop", "/api/v1/cycles/boost", "/api/v1/cycles/switch", "/api/v1/cycles/annotate", "/api/v1/cycles/break", "/api/v1/events/{id}/correct", "/api/v1/events/{id}/retract", "/api/v1/batches/{id}/retract", "/api/v1/state", "/api/v1/config", "/api/v1/events"} {
		if !paths[want] {
			t.Errorf("the page never used %s; the script should cover every endpoint a client of the web UI needs", want)
		}
	}

	// What the operator typed through the page is in no log, no metric and no health document.
	for what, text := range map[string]string{"the log": e.log.String(), "/metrics": string(e.raw("GET", "/metrics", nil, nil).Body), "/healthz": string(e.raw("GET", "/healthz", nil, nil).Body)} {
		if strings.Contains(text, "zq-sentinel") {
			t.Errorf("free text typed in the web UI reached %s", what)
		}
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
