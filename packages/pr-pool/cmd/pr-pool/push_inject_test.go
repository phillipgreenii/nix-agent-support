package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/emit"
)

// testPushEvent carries an explicit far-future `expiresAt` so the injected event
// is still RETAINED when the assertions look at the core's queue depth. The
// DEFAULT (no instants at all) is born expired — offered once, then dropped
// (INV-EVT-4) — which is exactly what testPushEventDefault below exercises.
const testPushEvent = `{"schemaVersion":"1","id":"op-1","type":"review-requested","expiresAt":"2099-01-01T00:00:00Z","payload":{"pr":42}}`

// testPushEventDefault is the DEFAULT event shape: no `at`, no `expiresAt`.
const testPushEventDefault = `{"schemaVersion":"1","id":"op-2","type":"review-requested","payload":{"pr":42}}`

// injectedLocator names a core by socket/token, as `--socket`/`--token` would.
func injectedLocator(socket, token string) emit.Locator {
	return emit.Locator{InjectedSocket: socket, InjectedToken: token}
}

// The whole point: push-inject against a REAL running core must put the event in
// THAT core's durable queue. The assertion is on the CORE's queue depth, which is
// what a silently-local enqueue could never satisfy.
func TestPushInject_DeliversToTheRunningCore(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	code := pushInject(&stdout, &stderr, false, injectedLocator(svc.Ref().Socket, svc.Ref().Token), emit.SocketEnqueuer{}, testPushEvent)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("core queue depth = %d, want 1 — the injected event never reached the core", depth)
	}
	if !strings.Contains(stdout.String(), "accepted event \"op-1\"") {
		t.Fatalf("stdout = %q, want the accepted outcome", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing on a successful injection", stderr.String())
	}
}

// Discovery path: no --socket, so the core is found via the record under the log
// dir, and the report says so.
func TestPushInject_DiscoversTheCore(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	loc := emit.Locator{Discover: emit.Discoverer(dir)}
	if code := pushInject(&stdout, &stderr, false, loc, emit.SocketEnqueuer{}, testPushEvent); code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("core queue depth = %d, want 1", depth)
	}
	if !strings.Contains(stdout.String(), "(discovered)") {
		t.Fatalf("stdout = %q, want it to say the core was discovered", stdout.String())
	}
}

// --json emits ONE JSON object describing the outcome, and NEVER the auth token.
func TestPushInject_JSONOutput(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	code := pushInject(&stdout, &stderr, true, injectedLocator(svc.Ref().Socket, svc.Ref().Token), emit.SocketEnqueuer{}, testPushEvent)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	var got pushInjectJSON
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("stdout %q is not one JSON object: %v", stdout.String(), err)
	}
	if got.SchemaVersion != "1" || !got.Accepted {
		t.Fatalf("report = %+v, want schemaVersion 1 and accepted", got)
	}
	if got.Event.ID != "op-1" || got.Event.Type != "review-requested" {
		t.Fatalf("event = %+v, want the injected event echoed", got.Event)
	}
	if got.Event.ExpiresAt != "2099-01-01T00:00:00Z" {
		t.Fatalf("event expiresAt = %q, want the injected instant echoed verbatim", got.Event.ExpiresAt)
	}
	// `at` was not supplied, so it is OMITTED rather than filled in: the default
	// belongs to the core's clock at ingest (INV-EVT-1), not to this CLI.
	if got.Event.At != "" {
		t.Fatalf("event at = %q, want it omitted — the CLI must not invent the core's default", got.Event.At)
	}
	if got.Core.Socket != svc.Ref().Socket || got.Core.Discovered {
		t.Fatalf("core = %+v, want the injected socket marked not-discovered", got.Core)
	}
	if strings.Contains(stdout.String(), svc.Ref().Token) {
		t.Fatal("the auth token leaked into --json output")
	}
}

// pushInjectJSON is the report shape as a CONSUMER of `--json` sees it.
type pushInjectJSON struct {
	SchemaVersion string `json:"schemaVersion"`
	Accepted      bool   `json:"accepted"`
	Event         struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		At        string `json:"at"`
		ExpiresAt string `json:"expiresAt"`
	} `json:"event"`
	Core struct {
		Socket     string `json:"socket"`
		Discovered bool   `json:"discovered"`
	} `json:"core"`
}

// The DEFAULT event (neither instant) is a first-class injection, and both fields
// are OMITTED from the report — nothing is invented on the operator's behalf. The
// human output says so out loud, because "I set no expiry" reads as "it never
// expires" while the contract means the opposite (INV-EVT-4, DEC-EVENT-1).
func TestPushInject_DefaultEventIsBornExpiredAndSaysSo(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	code := pushInject(&stdout, &stderr, false, injectedLocator(svc.Ref().Socket, svc.Ref().Token), emit.SocketEnqueuer{}, testPushEventDefault)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "born expired") {
		t.Fatalf("stdout = %q, want it to name the born-expired default", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := pushInject(&stdout, &stderr, true, injectedLocator(svc.Ref().Socket, svc.Ref().Token), emit.SocketEnqueuer{}, testPushEventDefault); code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	var got pushInjectJSON
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("stdout %q is not one JSON object: %v", stdout.String(), err)
	}
	if got.Event.At != "" || got.Event.ExpiresAt != "" {
		t.Fatalf("event = %+v, want both instants omitted for a default event", got.Event)
	}
}

// The text form must not leak the token either.
func TestPushInject_TextOutputNeverLeaksTheToken(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	pushInject(&stdout, &stderr, false, injectedLocator(svc.Ref().Socket, svc.Ref().Token), emit.SocketEnqueuer{}, testPushEvent)
	if strings.Contains(stdout.String()+stderr.String(), svc.Ref().Token) {
		t.Fatal("the auth token leaked into the text output")
	}
}

// No running core: exit 1 with the "no running core" diagnostic AND the remedy —
// push-inject never starts one (ADR 0036).
func TestPushInject_NoRunningCoreIsExit1(t *testing.T) {
	var stdout, stderr strings.Builder
	loc := injectedLocator(filepath.Join(shortDir(t), "gone.sock"), "tok")
	code := pushInject(&stdout, &stderr, false, loc, emit.SocketEnqueuer{}, testPushEvent)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no running core") {
		t.Fatalf("stderr = %q, want a no-running-core diagnostic", stderr.String())
	}
	if !strings.Contains(stderr.String(), "start the core's socket service") {
		t.Fatalf("stderr = %q, want the remedy, since push-inject will not start one itself", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing on stdout in text mode on failure", stdout.String())
	}
}

// Discovery finding nothing under the log dir is the same outcome.
func TestPushInject_NoDiscoverableCoreIsExit1(t *testing.T) {
	var stdout, stderr strings.Builder
	loc := emit.Locator{Discover: emit.Discoverer(shortDir(t))}
	if code := pushInject(&stdout, &stderr, false, loc, emit.SocketEnqueuer{}, testPushEvent); code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no running core") {
		t.Fatalf("stderr = %q, want a no-running-core diagnostic", stderr.String())
	}
}

// With --json, a FAILURE still leaves parseable JSON on stdout, so a machine
// consumer is never handed a half-written stream.
func TestPushInject_JSONFailureIsStillJSON(t *testing.T) {
	var stdout, stderr strings.Builder
	loc := injectedLocator(filepath.Join(shortDir(t), "gone.sock"), "tok")
	if code := pushInject(&stdout, &stderr, true, loc, emit.SocketEnqueuer{}, testPushEvent); code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1", code)
	}
	var got struct {
		Accepted bool   `json:"accepted"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("stdout %q is not JSON on failure: %v", stdout.String(), err)
	}
	if got.Accepted {
		t.Fatalf("report = %+v, want accepted=false", got)
	}
	if !strings.Contains(got.Error, "no running core") {
		t.Fatalf("report error = %q, want the cause", got.Error)
	}
}

// A malformed / non-schema-valid event is rejected BEFORE any core is located, so a
// typo never depends on a core being up to be reported.
func TestPushInject_RejectsMalformedEvent(t *testing.T) {
	cases := map[string]string{
		"not json":              `{not json`,
		"missing type":          `{"schemaVersion":"1","id":"x"}`,
		"bad at":                `{"schemaVersion":"1","id":"x","type":"t","at":"yesterday"}`,
		"bad expiresAt":         `{"schemaVersion":"1","id":"x","type":"t","expiresAt":"soon"}`,
		"legacy duration field": `{"schemaVersion":"1","id":"x","type":"t","ttl":"5m"}`,
	}
	for desc, arg := range cases {
		t.Run(desc, func(t *testing.T) {
			var stdout, stderr strings.Builder
			// An empty locator would yield ErrNoCore if locate ran at all.
			if code := pushInject(&stdout, &stderr, false, emit.Locator{}, emit.SocketEnqueuer{}, arg); code != conformance.ExitError {
				t.Fatalf("exit = %d, want 1", code)
			}
			if strings.Contains(stderr.String(), "no running core") {
				t.Fatalf("stderr = %q: the core was located before the event was validated", stderr.String())
			}
		})
	}
}

// The trap, guarded at the SUBCOMMAND boundary: wiring push-inject through
// QueueEnqueuer against a located core must FAIL LOUDLY rather than report a
// successful injection into a queue that dies with this process.
func TestPushInject_QueueEnqueuerIsRefused(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	code := pushInject(&stdout, &stderr, false, emit.Locator{Discover: emit.Discoverer(dir)},
		emit.QueueEnqueuer{Q: svc.Queue()}, testPushEvent)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 — QueueEnqueuer cannot serve a discovered core", code)
	}
	if !strings.Contains(stderr.String(), "cannot reach the located core") {
		t.Fatalf("stderr = %q, want the wrong-enqueuer diagnostic", stderr.String())
	}
	if strings.Contains(stdout.String(), "accepted") {
		t.Fatalf("stdout = %q, want NO success report", stdout.String())
	}
}

// Usage errors exit 1, never 2: push-inject reaches the core over the same
// ingest-event transport, where 2 is the common contract's pre-accept BUSY.
func TestRunPushInject_UsageErrorsAreExit1NotBusy(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":        {"--nope", testPushEvent},
		"missing event json":  {},
		"missing json, flags": {"--json"},
		"two positionals":     {testPushEvent, testPushEvent},
	}
	for desc, args := range cases {
		t.Run(desc, func(t *testing.T) {
			if code := runPushInject(args); code != conformance.ExitError {
				t.Fatalf("exit = %d, want %d (never %d/busy)", code, conformance.ExitError, conformance.ExitBusy)
			}
		})
	}
}

// -h prints the help and exits 0 rather than attempting an injection.
func TestRunPushInject_HelpExitsZero(t *testing.T) {
	if code := runPushInject([]string{"-h"}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
}

// Routing: `push-inject` is its own route, forwards its args, and is advertised.
func TestRoute_pushInject(t *testing.T) {
	r := route([]string{"pr-pool", "push-inject", "--json", "--socket", "/s", testPushEvent})
	if r.kind != routePushInject {
		t.Fatalf("kind = %v, want routePushInject", r.kind)
	}
	if strings.Join(r.rest, " ") != "--json --socket /s "+testPushEvent {
		t.Fatalf("rest = %v, want the args forwarded", r.rest)
	}
	if !strings.Contains(helpText, "push-inject") {
		t.Fatal("helpText does not mention push-inject")
	}
	if !strings.Contains(usageLine, "push-inject") {
		t.Fatal("usageLine does not mention push-inject")
	}
}

// The vocabulary defect: pr-pool's subcommand is `push-inject`. There is no
// `pr-pool-emit`, so nothing operator-facing may advertise one.
func TestHelpText_DoesNotAdvertisePrPoolEmit(t *testing.T) {
	if strings.Contains(helpText, "pr-pool-emit") {
		t.Fatal("helpText advertises a `pr-pool-emit` subcommand, which does not exist")
	}
}

// injectedRef is the shared injected-core precedence: flags win, else env, and
// PR_POOL_TOKEN is consulted only when the socket also came from the environment.
func TestInjectedRef_Precedence(t *testing.T) {
	t.Run("flags win", func(t *testing.T) {
		t.Setenv(envSocket, "/env/sock")
		t.Setenv(envToken, "env-token")
		if s, tok := injectedRef("/flag/sock", "flag-token"); s != "/flag/sock" || tok != "flag-token" {
			t.Fatalf("got %q/%q, want the flag values", s, tok)
		}
	})
	t.Run("env is the fallback", func(t *testing.T) {
		t.Setenv(envSocket, "/env/sock")
		t.Setenv(envToken, "env-token")
		if s, tok := injectedRef("", ""); s != "/env/sock" || tok != "env-token" {
			t.Fatalf("got %q/%q, want the env values", s, tok)
		}
	})
	t.Run("a flag socket does not adopt the env token", func(t *testing.T) {
		t.Setenv(envSocket, "/env/sock")
		t.Setenv(envToken, "env-token")
		if s, tok := injectedRef("/flag/sock", ""); s != "/flag/sock" || tok != "" {
			t.Fatalf("got %q/%q: a --socket/--token pair must travel together", s, tok)
		}
	})
	t.Run("nothing injected", func(t *testing.T) {
		t.Setenv(envSocket, "")
		t.Setenv(envToken, "")
		if s, tok := injectedRef("", ""); s != "" || tok != "" {
			t.Fatalf("got %q/%q, want nothing injected", s, tok)
		}
	})
}
