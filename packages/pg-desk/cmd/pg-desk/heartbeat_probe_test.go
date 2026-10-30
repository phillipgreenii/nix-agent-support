package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/freshness"
)

// prCapabilitiesJSON is the shape `pg-connector pr capabilities` answers for
// the daemon-backed GitHub backend (captured from a live run, one backend, two
// configured queries).
const prCapabilitiesJSON = `{"sources":[{"source":"pg-connector-pr-github","status":"succeeded","count":1}],
"backends":[{"backend":"pg-connector-pr-github","protocolVersion":1,
"vocabulary":{"owns_changes":true},
"queries":{"team":{"watched":true},"mine":{"watched":true}}},
{"backend":"pg-connector-pr-other","vocabulary":{"owns_changes":false},"queries":{"x":{}}}]}`

type connectorCalls struct {
	mu    sync.Mutex
	calls [][]string
}

func (c *connectorCalls) all() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// stubConnector answers capabilities with caps and every other call with
// changeErr; it records each call.
func stubConnector(t *testing.T, caps string, changeErr error) *connectorCalls {
	t.Helper()
	rec := &connectorCalls{}
	orig := heartbeatRunConnector
	t.Cleanup(func() { heartbeatRunConnector = orig })
	heartbeatRunConnector = func(_ context.Context, args ...string) ([]byte, error) {
		rec.mu.Lock()
		rec.calls = append(rec.calls, slices.Clone(args))
		rec.mu.Unlock()
		if len(args) > 1 && args[1] == "capabilities" {
			return []byte(caps), nil
		}
		return []byte(`{"changes":[]}`), changeErr
	}
	return rec
}

func TestHeartbeatProbesEveryQueryOfAnOwningBackendThenReadsTheLedger(t *testing.T) {
	st, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	// The ledger the probe's forwarded calls leave behind: the daemon's own
	// last_success_at for each query, and the probe's last_seen.
	withHeartbeatLedger(t, `[
 {"type":"pr","backend":"pg-connector-pr-github","query":"mine","consumers":{"pg-desk-freshness-mine":{"last_seen":"2026-10-06T11:59:30Z"}},"refreshed_at":"2026-10-06T11:59:00Z","last_error":null},
 {"type":"pr","backend":"pg-connector-pr-github","query":"team","consumers":{"pg-desk-freshness-team":{"last_seen":"2026-10-06T11:59:30Z"}},"refreshed_at":"2026-10-06T11:58:00Z","last_error":null}]`, nil)
	rec := stubConnector(t, prCapabilitiesJSON, nil)

	if _, err := runHeartbeat(t); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	var probed []string
	for _, c := range rec.all() {
		if c[1] == "changes" {
			j := strings.Join(c, " ")
			for _, want := range []string{"--cached", "--no-ack", "--backend pg-connector-pr-github", "--consumer " + heartbeatProbeConsumerFor(c[slices.Index(c, "--query")+1])} {
				if !strings.Contains(j, want) {
					t.Errorf("probe %q lacks %q", j, want)
				}
			}
			probed = append(probed, c[slices.Index(c, "--query")+1])
		}
	}
	if !slices.Equal(probed, []string{"mine", "team"}) {
		t.Errorf("probed queries = %v, want [mine team] (sorted, owning backend only)", probed)
	}

	rep, err := freshness.Build(st, &config.Config{}, freshnessTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Sources) != 1 || rep.Sources[0].Source != "pg-connector-pr-github" {
		t.Fatalf("sources = %+v", rep.Sources)
	}
	// Oldest of the two queries (11:58:00) against now (12:00:00).
	if age := rep.Sources[0].AgeSeconds; age == nil || *age != 120 || rep.Sources[0].Stale {
		t.Errorf("pr-github row = %+v, want age 120 and fresh", rep.Sources[0])
	}
}

func TestHeartbeatProbeFailureIsReportedAndNeverFailsTheHeartbeat(t *testing.T) {
	_, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	withHeartbeatLedger(t, `[]`, nil)
	rec := stubConnector(t, prCapabilitiesJSON, errors.New("daemon_unreachable"))

	stderr, err := runHeartbeat(t)
	if err != nil {
		t.Fatalf("heartbeat must survive a failed probe: %v", err)
	}
	if !strings.Contains(stderr, "not probed") || !strings.Contains(stderr, "daemon_unreachable") {
		t.Errorf("stderr = %q, want the failure named", stderr)
	}
	// An unreachable daemon fails every query alike: only the first is tried.
	changes := 0
	for _, c := range rec.all() {
		if c[1] == "changes" {
			changes++
		}
	}
	if changes != 1 {
		t.Errorf("changes probes = %d, want 1 (rest of the backend skipped)", changes)
	}
}

func TestHeartbeatProbeSurvivesAnAbsentOrGarbledConnector(t *testing.T) {
	for name, caps := range map[string]string{"garbled": "not json"} {
		t.Run(name, func(t *testing.T) {
			_, openFresh := openTestStore(t)
			withOpenSeams(t, openTestConfig("o/r"), openFresh)
			withHeartbeatLedger(t, `[]`, nil)
			stubConnector(t, caps, nil)
			stderr, err := runHeartbeat(t)
			if err != nil || !strings.Contains(stderr, "pr sources not probed") {
				t.Errorf("err=%v stderr=%q", err, stderr)
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		_, openFresh := openTestStore(t)
		withOpenSeams(t, openTestConfig("o/r"), openFresh)
		withHeartbeatLedger(t, `[]`, nil)
		orig := heartbeatRunConnector
		t.Cleanup(func() { heartbeatRunConnector = orig })
		heartbeatRunConnector = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("exec: not found") }
		stderr, err := runHeartbeat(t)
		if err != nil || !strings.Contains(stderr, "pr sources not probed") {
			t.Errorf("err=%v stderr=%q", err, stderr)
		}
	})
}
