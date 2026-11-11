package recorderfacts

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// wireRequest is one request the recorder received, read off the proxy's bytes.
type wireRequest struct{ Op, Body string }

var limitRe = regexp.MustCompile(`"limit":(\d+)`)

func (r wireRequest) limit() string {
	if m := limitRe.FindStringSubmatch(r.Body); m != nil {
		return m[1]
	}
	return ""
}

// parseWire splits the bytes a client sent into its requests (one connection
// per request, "POST /v1/<op>" then the JSON body).
func parseWire(wire string) []wireRequest {
	var out []wireRequest
	for _, chunk := range strings.Split(wire, "POST /v1/")[1:] {
		op, rest, _ := strings.Cut(chunk, " HTTP")
		_, body, _ := strings.Cut(rest, "\r\n\r\n")
		out = append(out, wireRequest{Op: op, Body: body})
	}
	return out
}

// scanEnv is a recorder with two live sessions and one ended one, read through
// a recording proxy.
func scanEnv(t *testing.T) (e *clienttest.Env, f *Facts, since func() []wireRequest) {
	t.Helper()
	e = clienttest.Start(t)
	for pid, sid := range map[int]string{501: "example-live-a", 502: "example-live-b", 503: "example-ended"} {
		e.Procs.Add(pid, time.UnixMilli(startMs), nil)
		writeRegistry(t, e, pid, sid, "busy", "")
	}
	e.Procs.Exit(503, time.UnixMilli(startMs+9000))
	socket, sent := recordingProxy(t, e.Socket)
	f = New(client.Options{Socket: socket})
	seen := 0
	since = func() []wireRequest {
		all := parseWire(sent())
		n := all[seen:]
		seen = len(all)
		return n
	}
	// The recorder learns of the exit asynchronously.
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.ResyncEnded()
		got, err := f.Registrations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if s := byID(got, "example-ended"); s != nil && !s.PidAlive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recorder never reported the exit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.ResyncEnded()
	since()
	return e, f, since
}

func ops(rs []wireRequest, op string) []wireRequest {
	var out []wireRequest
	for _, r := range rs {
		if r.Op == op {
			out = append(out, r)
		}
	}
	return out
}

// TestScanReadsOnlyLiveSessionsAfterStartup pins the request shape of a Scan:
// the first reads the ended Processes (once) and every later one reads the
// alive list plus one limit-1 registry lookup per LIVE session.
//
// Before: every Scan sent 2 process.list (alive, ended) and 1 registry query
// carrying every live and ended id with limit 5000. After: the first Scan sends
// 2 process.list and 3 limit-1 queries (the ended session's registry fields are
// read once); every later Scan sends 1 process.list and 2 limit-1 queries.
func TestScanReadsOnlyLiveSessionsAfterStartup(t *testing.T) {
	_, f, since := scanEnv(t)
	ctx := context.Background()

	first, err := f.Registrations(ctx)
	if err != nil || len(first) != 3 {
		t.Fatalf("first scan: %v %v", first, err)
	}
	startup := since()
	if n := len(ops(startup, "process.list")); n != 2 {
		t.Errorf("startup process.list = %d, want 2 (alive + ended)", n)
	}

	for i := 0; i < 3; i++ {
		got, err := f.Registrations(ctx)
		if err != nil || len(got) != 3 {
			t.Fatalf("scan %d: %v %v", i, got, err)
		}
		reqs := since()
		pl := ops(reqs, "process.list")
		if len(pl) != 1 || !strings.Contains(pl[0].Body, `"alive":true`) {
			t.Errorf("scan %d: process.list = %+v, want one alive read and no ended read", i, pl)
		}
		qs := ops(reqs, "query")
		if len(qs) != 2 || len(reqs) != 3 {
			t.Errorf("scan %d: %d requests, %d queries; want 3 and 2: %+v", i, len(reqs), len(qs), reqs)
		}
		for _, q := range qs {
			if q.limit() != "1" || strings.Contains(q.Body, `"in"`) || strings.Contains(q.Body, "example-ended") {
				t.Errorf("scan %d: registry query is not a live point lookup: %s", i, q.Body)
			}
		}
		live := byID(got, "example-live-a")
		if live == nil || live.Kind != "interactive" || live.Entrypoint != "cli" || !live.PidAlive {
			t.Errorf("scan %d: live row lost its registry fields: %+v", i, live)
		}
		if end := byID(got, "example-ended"); end == nil || end.PidAlive || end.Kind != "interactive" {
			t.Errorf("scan %d: ended row = %+v", i, end)
		}
	}
}

// TestSessionThatDiesMidRunKeepsItsRow: a session seen alive that then ends is
// carried as ended with the registry fields it had, with no ended-process read.
func TestSessionThatDiesMidRunKeepsItsRow(t *testing.T) {
	e, f, since := scanEnv(t)
	ctx := context.Background()
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatal(err)
	}
	since()
	e.Procs.Exit(502, time.UnixMilli(startMs+20000))
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := f.Registrations(ctx)
		if err != nil || len(got) != 3 {
			t.Fatalf("scan: %v %v", got, err)
		}
		if s := byID(got, "example-live-b"); s != nil && !s.PidAlive {
			if s.Kind != "interactive" || s.Entrypoint != "cli" || s.PID != 502 || s.Cwd != "/example/work" || s.RegistryStatus != "busy" {
				t.Errorf("died session lost its row: %+v", s)
			}
			if !s.StatusUpdatedAt.Equal(time.UnixMilli(startMs + 5000)) {
				t.Errorf("StatusUpdatedAt = %v", s.StatusUpdatedAt)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan never showed the session as ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, r := range since() {
		if r.Op == "process.list" && strings.Contains(r.Body, `"alive":false`) {
			t.Errorf("a Scan after startup read ended Processes: %s", r.Body)
		}
	}
	// The live set is now one session, so a later scan looks up one session.
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatal(err)
	}
	if qs := ops(since(), "query"); len(qs) != 1 {
		t.Errorf("queries after the death = %d, want 1 (one live session)", len(qs))
	}
}

// TestKnownSessionIDsReadsEveryProcessFreshAndIgnoresTheScanState: GC keeps
// every id the recorder knows, alive or ended, and never uses the carried state.
func TestKnownSessionIDsReadsEveryProcessFreshAndIgnoresTheScanState(t *testing.T) {
	e, f, since := scanEnv(t)
	ctx := context.Background()
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatal(err)
	}
	since()
	want := []string{"example-ended", "example-live-a", "example-live-b"}
	for i := 0; i < 2; i++ {
		ids, err := f.KnownSessionIDs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, want) {
			t.Fatalf("KnownSessionIDs = %v, want %v", ids, want)
		}
		pl := ops(since(), "process.list")
		if len(pl) != 2 || !strings.Contains(pl[0].Body+pl[1].Body, `"alive":false`) {
			t.Errorf("call %d: process.list = %+v, want a fresh alive and ended read", i, pl)
		}
		// A process that ended since the last scan is still in the set: the scan
		// state is not consulted, and a new one appears at once.
		e.Procs.Exit(501, time.UnixMilli(startMs+30000))
	}
	e.Procs.Add(504, time.UnixMilli(startMs), nil)
	writeRegistry(t, e, 504, "example-new", "idle", "")
	ids, err := f.KnownSessionIDs(ctx)
	if err != nil || !slices.Contains(ids, "example-new") {
		t.Errorf("a session born after the last scan is missing: %v %v", ids, err)
	}
	if _, err := New(client.Options{Socket: filepath.Join(t.TempDir(), "absent.sock")}).KnownSessionIDs(ctx); !errorsIsOffline(err) {
		t.Errorf("recorder down: err = %v, want offline (no ids, so GC keeps every row)", err)
	}
}

func errorsIsOffline(err error) bool { return err == session.ErrDaemonOffline }

// failingProxy answers a registry query that names failSID with a query
// timeout and forwards everything else.
func failingProxy(t *testing.T, target, failSID string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fpx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	rp := &httputil.ReverseProxy{
		Director:  func(r *http.Request) { r.URL.Scheme, r.URL.Host = "http", "recorder" },
		Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", target) }},
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/query" {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), failSID) {
				w.WriteHeader(http.StatusGatewayTimeout)
				_, _ = w.Write([]byte(`{"error":"query_timeout","message":"injected"}`))
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			r.ContentLength = int64(len(b))
		}
		rp.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socket
}

// TestFailedRegistryLookupForOneSessionDoesNotFailTheScan: one session's
// lookup timing out leaves the Scan whole; the others keep their fields and the
// failed one keeps what it had last time.
func TestFailedRegistryLookupForOneSessionDoesNotFailTheScan(t *testing.T) {
	e := clienttest.Start(t)
	for pid, sid := range map[int]string{501: "example-live-a", 502: "example-live-b"} {
		e.Procs.Add(pid, time.UnixMilli(startMs), nil)
		writeRegistry(t, e, pid, sid, "busy", "")
	}
	healthy := New(client.Options{Socket: e.Socket})
	if _, err := healthy.Registrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := New(client.Options{Socket: failingProxy(t, e.Socket, "example-live-b")})
	got, err := f.Registrations(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("scan with one failed lookup: %v %v", got, err)
	}
	if a := byID(got, "example-live-a"); a.Kind != "interactive" {
		t.Errorf("the other session lost its fields: %+v", a)
	}
	if b := byID(got, "example-live-b"); b == nil || !b.PidAlive || b.PID != 502 || b.Kind != "" {
		t.Errorf("failed session = %+v, want the process facts and no registry fields", b)
	}
}
