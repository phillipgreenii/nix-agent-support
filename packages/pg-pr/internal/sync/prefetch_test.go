package sync

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeCert struct {
	valid bool
	err   error
}

func (c fakeCert) CertValid(context.Context, string) (bool, error) { return c.valid, c.err }

// fakeResolver returns a fixed socket path (or error) for the gate's socket
// resolution seam, so the gate tests never touch a real ssh-agent.
type fakeResolver struct {
	sock string
	err  error
}

func (r fakeResolver) Resolve(context.Context) (string, error) { return r.sock, r.err }

// fakeFetcher records the batchMode and resolved authSock of each call and
// returns a fixed error. The daemon drives the gate serially, so no locking is
// needed.
type fakeFetcher struct {
	calls []bool   // batchMode per call
	socks []string // authSock per call
	err   error
}

func (f *fakeFetcher) FetchPRHead(_ context.Context, _ string, _ int, batchMode bool, authSock string) error {
	f.calls = append(f.calls, batchMode)
	f.socks = append(f.socks, authSock)
	return f.err
}

func TestPreFetchGate_CertValid_UsesBatchMode(t *testing.T) {
	f := &fakeFetcher{}
	g := &PreFetchGate{
		Resolver: fakeResolver{sock: "/agent.sock"},
		Cert:     fakeCert{valid: true},
		Fetcher:  f,
	}
	if got := g.Ensure(context.Background(), "/repo", 42); got != FetchOK {
		t.Fatalf("outcome = %v, want FetchOK", got)
	}
	if len(f.calls) != 1 || f.calls[0] != true {
		t.Fatalf("expected exactly one batch-mode fetch, got %v", f.calls)
	}
	if f.socks[0] != "/agent.sock" {
		t.Fatalf("resolved socket must thread into the fetch, got %q", f.socks[0])
	}
}

func TestPreFetchGate_CertExpired_InteractiveThenCooldownDefers(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fakeFetcher{err: errors.New("git fetch: exit status 128: Permission denied (publickey)")}
	g := &PreFetchGate{
		Resolver: fakeResolver{sock: "/agent.sock"},
		Cert:     fakeCert{valid: false},
		Fetcher:  f,
		Cooldown: time.Hour,
		Now:      func() time.Time { return now },
	}
	// First attempt: interactive (batchMode=false), fetch fails → FetchFailed.
	if got := g.Ensure(context.Background(), "/repo", 42); got != FetchFailed {
		t.Fatalf("first outcome = %v, want FetchFailed", got)
	}
	if len(f.calls) != 1 || f.calls[0] != false {
		t.Fatalf("expected one interactive (non-batch) fetch, got %v", f.calls)
	}
	// Second attempt within cooldown: deferred, NO second browser-capable fetch.
	now = now.Add(time.Minute)
	if got := g.Ensure(context.Background(), "/repo", 7); got != FetchDeferred {
		t.Fatalf("second outcome = %v, want FetchDeferred", got)
	}
	if len(f.calls) != 1 {
		t.Fatalf("no second fetch expected during cooldown, got %v", f.calls)
	}
}

func TestPreFetchGate_ResolverError_TakesInteractivePath(t *testing.T) {
	f := &fakeFetcher{err: errors.New("Permission denied (publickey).")}
	g := &PreFetchGate{
		// Resolver fails (no cert-bearing socket / agent down). The cert check
		// MUST be bypassed and the gate MUST take the interactive path with the
		// ambient (empty) SSH_AUTH_SOCK.
		Resolver: fakeResolver{err: errors.New("no cert-bearing ssh-agent socket found")},
		Cert:     fakeCert{valid: true}, // deliberately "valid": must be skipped
		Fetcher:  f,
		Cooldown: time.Hour,
		Now:      func() time.Time { return time.Unix(1000, 0) },
	}
	if got := g.Ensure(context.Background(), "/repo", 42); got != FetchFailed {
		t.Fatalf("outcome = %v, want FetchFailed", got)
	}
	if len(f.calls) != 1 || f.calls[0] != false {
		t.Fatalf("resolver error must take the interactive (non-batch) path, got %v", f.calls)
	}
	if f.socks[0] != "" {
		t.Fatalf("resolver error must fetch with the ambient (empty) SSH_AUTH_SOCK, got %q", f.socks[0])
	}
}

func TestCLIAgentSocketResolver_CacheHitThenMiss(t *testing.T) {
	// certOn controls which candidate sockets currently carry a cert; probes
	// records every probe so we can assert a cache hit re-probes ONLY the cached
	// socket. Both the lister and the prober are injected seams, so the test
	// needs no real ssh-agent.
	certOn := map[string]bool{"/sockA": true}
	var probes []string
	r := &CLIAgentSocketResolver{
		list: func() ([]string, error) { return []string{"/sockA", "/sockB"}, nil },
		hasCert: func(_ context.Context, sock string) bool {
			probes = append(probes, sock)
			return certOn[sock]
		},
	}

	// Cold resolve: probe candidates, find + cache /sockA.
	if got, err := r.Resolve(context.Background()); err != nil || got != "/sockA" {
		t.Fatalf("cold resolve = %q, %v; want /sockA, nil", got, err)
	}

	// Cache hit: /sockA still carries the cert → return it after re-probing ONLY
	// the cached socket (never the whole candidate list).
	probes = nil
	if got, err := r.Resolve(context.Background()); err != nil || got != "/sockA" {
		t.Fatalf("cache-hit resolve = %q, %v; want /sockA, nil", got, err)
	}
	if len(probes) != 1 || probes[0] != "/sockA" {
		t.Fatalf("cache hit must re-probe only the cached socket, got %v", probes)
	}

	// Cache miss (agent restarted, cert moved to /sockB): re-glob candidates,
	// find + cache /sockB.
	certOn = map[string]bool{"/sockB": true}
	if got, err := r.Resolve(context.Background()); err != nil || got != "/sockB" {
		t.Fatalf("post-rotation resolve = %q, %v; want /sockB, nil", got, err)
	}

	// No candidate carries a cert → error.
	certOn = map[string]bool{}
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error when no candidate socket carries a cert")
	}
}

// wedgingResolver builds a resolver whose probe for any socket in `wedged`
// blocks until its (per-candidate) context is cancelled, and otherwise returns
// certOn[sock]. A probe handed an already-expired context fails — modelling
// `ssh-add` under exec.CommandContext once the deadline has passed. A tiny
// per-candidate probeTimeout keeps the test fast and deterministic.
func wedgingResolver(cands []string, certOn, wedged map[string]bool) (*CLIAgentSocketResolver, *[]string) {
	var probes []string
	r := &CLIAgentSocketResolver{
		probeTimeout: 20 * time.Millisecond,
		list:         func() ([]string, error) { return cands, nil },
		hasCert: func(ctx context.Context, sock string) bool {
			probes = append(probes, sock)
			if ctx.Err() != nil {
				return false
			}
			if wedged[sock] {
				<-ctx.Done()
				return false
			}
			return certOn[sock]
		},
	}
	return r, &probes
}

// TestCLIAgentSocketResolver_SkipsWedgedCandidate: a wedged candidate (ssh-add
// hangs) must not consume the whole resolve budget — the per-candidate probe
// timeout bounds it so a later good candidate is still reached (pg2-h7c9).
func TestCLIAgentSocketResolver_SkipsWedgedCandidate(t *testing.T) {
	r, _ := wedgingResolver(
		[]string{"/wedged", "/good"},
		map[string]bool{"/good": true},
		map[string]bool{"/wedged": true},
	)
	// Overall budget models the caller's context.WithTimeout(defaultProbeTimeout).
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := r.Resolve(ctx)
	if err != nil || got != "/good" {
		t.Fatalf("resolve = %q, %v; want /good, nil (wedged candidate must be skipped)", got, err)
	}
}

// TestCLIAgentSocketResolver_WedgedLastGoodDoesNotStarve: a cached lastGood that
// is now wedged must be bounded by the per-candidate timeout, invalidated, and
// the scan must still return the good candidate (pg2-h7c9).
func TestCLIAgentSocketResolver_WedgedLastGoodDoesNotStarve(t *testing.T) {
	r, _ := wedgingResolver(
		[]string{"/good"},
		map[string]bool{"/good": true},
		map[string]bool{"/wedged": true},
	)
	r.lastGood = "/wedged" // stale + hung cached entry
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := r.Resolve(ctx)
	if err != nil || got != "/good" {
		t.Fatalf("resolve = %q, %v; want /good, nil (wedged lastGood must be invalidated + skipped)", got, err)
	}
	if r.lastGood != "/good" {
		t.Fatalf("lastGood = %q; want /good (a lastGood that no longer probes clean must be invalidated)", r.lastGood)
	}
}

func TestClassifyFetch(t *testing.T) {
	if classifyFetch(nil) != FetchOK {
		t.Fatal("nil error must classify as FetchOK")
	}
	// Go exec form (top-level binary missing).
	if classifyFetch(errors.New(`git fetch: exec: "step": executable file not found in $PATH`)) != FetchStepMissing {
		t.Fatal("missing step binary (exec form) must classify as FetchStepMissing")
	}
	// The daemon's ~/.ssh/config ProxyCommand runs under /bin/sh, so a missing
	// `step` there surfaces as `sh: step: command not found`.
	if classifyFetch(errors.New("git fetch: exit status 128: sh: step: command not found")) != FetchStepMissing {
		t.Fatal("sh: step: command not found must classify as FetchStepMissing")
	}
	// zsh phrasing (interactive shells) — the reversed token order.
	if classifyFetch(errors.New("zsh:1: command not found: step")) != FetchStepMissing {
		t.Fatal("command not found: step must classify as FetchStepMissing")
	}
	// A DIFFERENT missing binary (git/ssh) must NOT be mislabeled a step problem:
	// the "executable file not found" token is generic to Go's exec, so it must
	// only classify as FetchStepMissing when `step` is actually the missing
	// binary (pg2-h7c9).
	if classifyFetch(errors.New(`git fetch: exec: "git": executable file not found in $PATH`)) != FetchFailed {
		t.Fatal("a missing non-step binary must classify as FetchFailed, not FetchStepMissing")
	}
	if classifyFetch(errors.New("Permission denied (publickey).")) != FetchFailed {
		t.Fatal("auth failure must classify as FetchFailed")
	}
}

func TestParseCertValidBounds(t *testing.T) {
	out := "        Valid: from 2026-07-06T08:00:00 to 2999-01-01T00:00:00\n"
	nb, na, ok := parseCertValidBounds(out)
	if !ok || na.Year() != 2999 {
		t.Fatalf("parse failed: notAfter=%v ok=%v", na, ok)
	}
	if nb.Year() != 2026 {
		t.Fatalf("not-before parse failed: notBefore=%v", nb)
	}
	if _, _, ok := parseCertValidBounds("no valid line here\n"); ok {
		t.Fatal("expected ok=false when there is no Valid line")
	}
	// "forever" → an always-valid window: not-before in the far past, not-after
	// in the far future.
	if nb, na, ok := parseCertValidBounds("        Valid: forever\n"); !ok || !nb.Before(time.Now()) || na.Before(time.Now()) {
		t.Fatalf("forever must be an always-valid window: notBefore=%v notAfter=%v ok=%v", nb, na, ok)
	}
}

func TestFirstCertLine(t *testing.T) {
	out := "ssh-ed25519 AAAA... plainkey\n" +
		"ecdsa-sha2-nistp256-cert-v01@openssh.com AAAAee... me@host\n"
	got := firstCertLine(out)
	if got == "" || got[:5] != "ecdsa" {
		t.Fatalf("firstCertLine = %q, want the cert line", got)
	}
	if firstCertLine("ssh-ed25519 AAAA... plainkey\n") != "" {
		t.Fatal("firstCertLine must return \"\" when no cert line is present")
	}
}
