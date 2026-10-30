package exporter

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func TestRenderPrefixesSeriesAndRegistersReasonsAtZero(t *testing.T) {
	o := obs.New(nil)
	o.Reg.Add(obs.MRequestsTotal, map[string]string{"client": "other", "op": "status", "outcome": "ok"}, 2)
	o.Reg.Observe(obs.MQueryDuration, 0.003)
	var b bytes.Buffer
	Render(&b, o.Reg)
	out := b.String()
	for _, want := range []string{
		`pg_claude_code_recorder_dropped_bytes_total{reason="file_vanished"} 0`,
		`pg_claude_code_recorder_dropped_bytes_total{reason="removal_race"} 0`,
		`pg_claude_code_recorder_requests_total{client="other",op="status",outcome="ok"} 2`,
		`pg_claude_code_recorder_query_duration_seconds_bucket{le="0.005"} 1`,
		`pg_claude_code_recorder_query_duration_seconds_count 1`,
		`pg_claude_code_recorder_feed_latency_seconds_count 0`,
		`pg_claude_code_recorder_state_free_bytes 0`,
		`# TYPE pg_claude_code_recorder_last_sweep_timestamp_seconds gauge`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, Prefix) {
			t.Errorf("unprefixed series %q", line)
		}
	}
}

func TestStartRefusesNonLoopbackAndServesMetrics(t *testing.T) {
	o := obs.New(nil)
	if _, err := Start("0.0.0.0:0", o.Reg, nil); err == nil {
		t.Fatal("wildcard address accepted")
	}
	refreshed := 0
	s, err := Start("127.0.0.1:0", o.Reg, func() { refreshed++ })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, err := http.Get("http://" + s.Addr() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), Prefix+"dropped_bytes_total") || refreshed != 1 {
		t.Fatalf("status %d refreshed %d body %q", resp.StatusCode, refreshed, body)
	}
}
