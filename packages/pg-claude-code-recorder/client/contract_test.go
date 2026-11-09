//go:build integration

package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
)

// These helpers are shared by the integration-tagged contract, latency and
// lifecycle suites, so they carry the same build tag.
//
// The wire-contract suite: a real daemon (store, copier, parser, feed, listener)
// in process, one case per operation and one per error (the conversation and
// process operations are in lifecycle_integration_test.go), over its Unix socket. The cases for operations that need
// no daemon state (hello, errors) go through raw HTTP so the framing is what is
// tested, not this package's reading of it.

const (
	cL1 = `{"type":"user","uuid":"a1","sessionId":"s1","timestamp":"2026-10-29T20:00:00Z","cwd":"/example/work/sub","message":{"role":"user"}}` + "\n"
	cL2 = `{"type":"assistant","uuid":"a2","sessionId":"s1","timestamp":"2026-10-29T20:00:01Z","cwd":"/example/work","message":{"id":"m1","model":"example-model-a","role":"assistant","usage":{"output_tokens":10}}}` + "\n"
	cL3 = `{"type":"assistant","uuid":"a3","sessionId":"s1","timestamp":"2026-10-29T20:00:02Z","message":{"id":"m2","model":"example-model-b","role":"assistant","usage":{"output_tokens":5}}}` + "\n"
	cL4 = `{"type":"summary","uuid":"a4","sessionId":"s1","timestamp":"2026-10-29T20:00:03Z","summary":"registry file deleted early"}` + "\n"
	rel = "projects/example-proj/s1.jsonl"
)

// errorsIs and errorsAs wrap the errors package for the integration cases.
func errorsIs(err, target error) bool           { return errors.Is(err, target) }
func errorsAs(err error, t **client.Error) bool { return errors.As(err, t) }

func ctx() context.Context { return context.Background() }

func start(t *testing.T, mutate func(*config.Config)) (*clienttest.Env, *client.Client) {
	t.Helper()
	e := clienttest.Start(t, clienttest.Options{Mutate: mutate})
	e.Write(rel, cL1+cL2+cL3+cL4)
	e.Settle()
	return e, e.Dial(ctx())
}

// httpDo speaks the framing directly.
func httpDo(t *testing.T, sock, method, path, body string) (int, map[string]any) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(c, "unix", sock)
	}, DisableKeepAlives: true}, Timeout: 15 * time.Second}
	req, _ := http.NewRequest(method, "http://recorder"+path, bytes.NewReader([]byte(body)))
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s %s: not JSON: %q", method, path, b)
	}
	return resp.StatusCode, out
}

func gauge(e *clienttest.Env, name string) float64 {
	v, _ := e.Daemon.Obs().Reg.Gauge(name, nil)
	return v
}

func mustFeedHead(t *testing.T, c *client.Client) string {
	t.Helper()
	a, err := c.FeedPage(ctx(), nil, "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a.NextCursor
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
