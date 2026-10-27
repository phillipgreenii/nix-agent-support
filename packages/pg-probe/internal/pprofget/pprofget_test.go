package pprofget

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// request is one request a fixture server saw.
type request struct{ Method, Path, Query string }

// fixture is a real HTTP server on a real unix socket that records every
// request it receives, so "refused" can be asserted as "the server saw nothing".
type fixture struct {
	Socket string
	mu     sync.Mutex
	seen   []request
}

func (f *fixture) Seen() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.seen...)
}

// newFixture serves handler on <tmp>/<base>. The directory is under /tmp, not
// t.TempDir(): a macOS temp path can exceed the 104-byte unix socket limit.
func newFixture(t *testing.T, base string, handler func(w http.ResponseWriter, r *http.Request)) *fixture {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pgp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fixture{Socket: filepath.Join(dir, base)}
	l, err := net.Listen("unix", f.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, request{r.Method, r.URL.Path, r.URL.RawQuery})
		f.mu.Unlock()
		handler(w, r)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = fmt.Fprintf(w, "ok:%s", r.URL.Path)
}

// rawGet is the negative control's way in: a plain client that bypasses
// pprofget, to prove the fixture records what reaches it.
func rawGet(t *testing.T, socket, path string) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}}
	resp, err := hc.Get("http://pprof" + path)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func ptr(i int) *int { return &i }

func TestGet_EveryAllowedPath(t *testing.T) {
	f := newFixture(t, SocketBase, echoHandler)
	c := &Client{Socket: f.Socket}
	for _, p := range AllowedPaths() {
		var out bytes.Buffer
		if err := c.Get(context.Background(), p, Query{}, &out); err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if got, want := out.String(), "ok:"+p; got != want {
			t.Errorf("%s: body %q, want %q", p, got, want)
		}
	}
	seen := f.Seen()
	if len(seen) != len(AllowedPaths()) {
		t.Fatalf("server saw %d requests, want %d: %v", len(seen), len(AllowedPaths()), seen)
	}
	for _, r := range seen {
		if r.Method != http.MethodGet || r.Query != "" || !contains(AllowedPaths(), r.Path) {
			t.Errorf("unexpected request %+v", r)
		}
	}
}

func TestAllowedPaths_IsTheFixedSet(t *testing.T) {
	want := []string{
		"/debug/pprof/", "/debug/pprof/allocs", "/debug/pprof/block", "/debug/pprof/cmdline",
		"/debug/pprof/goroutine", "/debug/pprof/heap", "/debug/pprof/mutex", "/debug/pprof/profile",
		"/debug/pprof/symbol", "/debug/pprof/threadcreate", "/debug/pprof/trace",
	}
	if got := AllowedPaths(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("allowlist changed:\n got %v\nwant %v", got, want)
	}
}

func TestGet_QueryIsBuiltFromValidatedFieldsOnly(t *testing.T) {
	f := newFixture(t, SocketBase, echoHandler)
	c := &Client{Socket: f.Socket}
	var sink bytes.Buffer
	if err := c.Get(context.Background(), "/debug/pprof/profile", Query{Seconds: ptr(2)}, &sink); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), "/debug/pprof/goroutine", Query{Debug: ptr(1)}, &sink); err != nil {
		t.Fatal(err)
	}
	seen := f.Seen()
	if len(seen) != 2 || seen[0].Query != "seconds=2" || seen[1].Query != "debug=1" {
		t.Fatalf("queries not forwarded as validated: %v", seen)
	}
}

func TestGet_RefusesBadQueries(t *testing.T) {
	f := newFixture(t, SocketBase, echoHandler)
	c := &Client{Socket: f.Socket}
	cases := []struct {
		path string
		q    Query
	}{
		{"/debug/pprof/heap", Query{Seconds: ptr(5)}},      // seconds on a snapshot profile
		{"/debug/pprof/profile", Query{Debug: ptr(1)}},     // debug on a capture
		{"/debug/pprof/profile", Query{Seconds: ptr(0)}},   // below range
		{"/debug/pprof/profile", Query{Seconds: ptr(301)}}, // above range
		{"/debug/pprof/trace", Query{Seconds: ptr(-1)}},
		{"/debug/pprof/heap", Query{Debug: ptr(3)}},
		{"/debug/pprof/heap", Query{Debug: ptr(-1)}},
		{"/debug/pprof/cmdline", Query{Debug: ptr(1)}},
	}
	for _, tc := range cases {
		err := c.Get(context.Background(), tc.path, tc.q, io.Discard)
		var ref *RefusedError
		if !errors.As(err, &ref) {
			t.Errorf("%s %+v: want RefusedError, got %v", tc.path, tc.q, err)
		}
	}
	if seen := f.Seen(); len(seen) != 0 {
		t.Errorf("refused queries reached the server: %v", seen)
	}
}

func TestDo_RefusesEveryNonGET(t *testing.T) {
	f := newFixture(t, SocketBase, echoHandler)
	c := &Client{Socket: f.Socket}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "CONNECT", "TRACE", "get", "Get", ""} {
		err := c.Do(context.Background(), m, "/debug/pprof/heap", Query{}, io.Discard)
		var ref *RefusedError
		if !errors.As(err, &ref) {
			t.Errorf("method %q: want RefusedError, got %v", m, err)
		}
	}
	if seen := f.Seen(); len(seen) != 0 {
		t.Errorf("non-GET requests reached the server: %v", seen)
	}
}

func TestGet_RefusesEveryOtherPath(t *testing.T) {
	f := newFixture(t, SocketBase, echoHandler)
	c := &Client{Socket: f.Socket}
	other := []string{
		"", "/", "/debug/pprof", "/debug/pprof/heap/", "/debug/pprof/heap?debug=1", "/debug/pprof/heap#x",
		"/debug/pprof/../vars", "/debug/pprof/./heap", "/debug/pprof/unknown", "/debug/pprof//heap",
		"/debug/vars", "/metrics", "/DEBUG/PPROF/heap", "debug/pprof/heap", "/debug/pprof/heap%2f",
		"/v1.43/containers/json", "/containers/json", "/debug/pprof/profile/../../../etc/passwd",
		"http://evil/debug/pprof/heap", "/debug/pprof/heap ",
	}
	for _, p := range other {
		err := c.Get(context.Background(), p, Query{}, io.Discard)
		var ref *RefusedError
		if !errors.As(err, &ref) {
			t.Errorf("path %q: want RefusedError, got %v", p, err)
		}
	}
	if seen := f.Seen(); len(seen) != 0 {
		t.Fatalf("a refused path reached the server: %v", seen)
	}

	// Negative control: the fixture really does record what reaches it, so the
	// empty list above is meaningful rather than a broken recorder.
	rawGet(t, f.Socket, "/debug/vars")
	if seen := f.Seen(); len(seen) != 1 || seen[0].Path != "/debug/vars" {
		t.Fatalf("negative control: recorder did not see a direct request: %v", seen)
	}
}

func TestResolveTarget(t *testing.T) {
	ok := map[string]string{
		"": Index, "index": Index, "heap": "/debug/pprof/heap", "goroutine": "/debug/pprof/goroutine",
		"profile": "/debug/pprof/profile", "/debug/pprof/trace": "/debug/pprof/trace", "/debug/pprof/": Index,
	}
	for in, want := range ok {
		got, err := ResolveTarget(in)
		if err != nil || got != want {
			t.Errorf("ResolveTarget(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"vars", "heap/", "../heap", "heap?debug=1", "/etc/passwd", "/debug/pprof/x", "he ap", "heap%2f", "/"} {
		if got, err := ResolveTarget(in); err == nil {
			t.Errorf("ResolveTarget(%q) = %q, want a refusal", in, got)
		}
	}
}

func TestGet_RefusesASocketThatIsNotNamedPprofSock(t *testing.T) {
	// A real, listening socket with another name (a container engine's API
	// socket is the case that matters): refused, and nothing is dialled.
	f := newFixture(t, "docker.sock", echoHandler)
	err := (&Client{Socket: f.Socket}).Get(context.Background(), "/debug/pprof/heap", Query{}, io.Discard)
	var ref *RefusedError
	if !errors.As(err, &ref) {
		t.Fatalf("want RefusedError, got %v", err)
	}
	if seen := f.Seen(); len(seen) != 0 {
		t.Fatalf("a foreign socket was dialled: %v", seen)
	}
}

func TestGet_SocketPreconditions(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pgp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	regular := filepath.Join(dir, SocketBase)
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var ref *RefusedError
	if err := (&Client{Socket: regular}).Get(context.Background(), Index, Query{}, io.Discard); !errors.As(err, &ref) {
		t.Errorf("regular file named pprof.sock: want RefusedError, got %v", err)
	}
	if err := (&Client{Socket: SocketBase}).Get(context.Background(), Index, Query{}, io.Discard); !errors.As(err, &ref) {
		t.Errorf("relative socket: want RefusedError, got %v", err)
	}
	missing := filepath.Join(dir, "none", SocketBase)
	err = (&Client{Socket: missing}).Get(context.Background(), Index, Query{}, io.Discard)
	if err == nil || errors.As(err, &ref) {
		t.Errorf("missing socket: want a plain (non-refusal) error, got %v", err)
	}
}

func TestGet_DoesNotFollowRedirects(t *testing.T) {
	f := newFixture(t, SocketBase, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/debug/vars", http.StatusFound)
	})
	err := (&Client{Socket: f.Socket}).Get(context.Background(), "/debug/pprof/heap", Query{}, io.Discard)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusFound {
		t.Fatalf("want StatusError 302, got %v", err)
	}
	for _, r := range f.Seen() {
		if r.Path == "/debug/vars" {
			t.Fatalf("redirect target was requested: %v", f.Seen())
		}
	}
}

func TestGet_Non2xxIsAStatusError(t *testing.T) {
	f := newFixture(t, SocketBase, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "profiling disabled", http.StatusNotFound)
	})
	var out bytes.Buffer
	err := (&Client{Socket: f.Socket}).Get(context.Background(), Index, Query{}, &out)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 || !strings.Contains(se.Body, "profiling disabled") {
		t.Fatalf("want StatusError 404 with body, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("error body leaked to the output writer: %q", out.String())
	}
}

func TestGet_HonoursContextDeadline(t *testing.T) {
	f := newFixture(t, SocketBase, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := (&Client{Socket: f.Socket}).Get(ctx, "/debug/pprof/profile", Query{}, io.Discard); err == nil {
		t.Fatal("want a deadline error")
	}
}
