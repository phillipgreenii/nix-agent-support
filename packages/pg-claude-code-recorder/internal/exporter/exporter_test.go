package exporter

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strconv"
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

// checkExposition is a minimal Prometheus text-exposition grammar check: every
// line is blank, a "# HELP|TYPE name ..." comment, or "name[{k="v",...}] value"
// with a valid name, well-formed labels and a float value; each series has a
// preceding # TYPE of its family and no family is typed twice.
func checkExposition(t *testing.T, text string) {
	t.Helper()
	nameRe := `[a-zA-Z_:][a-zA-Z0-9_:]*`
	typeRe := regexp.MustCompile(`^# TYPE (` + nameRe + `) (counter|gauge|histogram|summary|untyped)$`)
	seriesRe := regexp.MustCompile(`^(` + nameRe + `)(\{([a-zA-Z_][a-zA-Z0-9_]*="[^"\\]*"(,[a-zA-Z_][a-zA-Z0-9_]*="[^"\\]*")*)?\})? (\S+)$`)
	typed := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "# HELP "):
		case strings.HasPrefix(line, "# TYPE "):
			m := typeRe.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("line %d: bad TYPE line %q", i+1, line)
				continue
			}
			if typed[m[1]] {
				t.Errorf("line %d: family %s typed twice", i+1, m[1])
			}
			typed[m[1]] = true
		case strings.HasPrefix(line, "#"):
			t.Errorf("line %d: unknown comment %q", i+1, line)
		default:
			m := seriesRe.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("line %d: bad series line %q", i+1, line)
				continue
			}
			if _, err := strconv.ParseFloat(m[5], 64); err != nil && m[5] != "+Inf" && m[5] != "-Inf" && m[5] != "NaN" {
				t.Errorf("line %d: bad value %q", i+1, m[5])
			}
			base := m[1]
			for _, suf := range []string{"_bucket", "_sum", "_count"} {
				if strings.HasSuffix(base, suf) && typed[strings.TrimSuffix(base, suf)] {
					base = strings.TrimSuffix(base, suf)
				}
			}
			if !typed[base] {
				t.Errorf("line %d: series %s has no preceding # TYPE", i+1, m[1])
			}
		}
	}
}

func TestRenderedExpositionPassesGrammarCheck(t *testing.T) {
	o := obs.New(nil)
	o.Reg.Add(obs.MRequestsTotal, map[string]string{"client": "other", "op": "status", "outcome": "ok"}, 2)
	o.Reg.Observe(obs.MQueryDuration, 0.003)
	o.Reg.SetCounter(obs.MProcessCPUSeconds, nil, 1.5)
	o.Reg.Set(obs.MWALBytes, nil, 4096)
	var b bytes.Buffer
	Render(&b, o.Reg)
	checkExposition(t, b.String())
	if !strings.Contains(b.String(), "# TYPE "+Prefix+obs.MProcessCPUSeconds+" counter") {
		t.Fatal("process cpu counter missing")
	}
}
