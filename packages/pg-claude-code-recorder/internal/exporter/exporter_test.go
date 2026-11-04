package exporter

import (
	"bytes"
	"io"
	"net/http"
	"os"
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

func TestRenderLabelledHistogramPutsLeLastAndKeepsLabelsOnSumAndCount(t *testing.T) {
	r := obs.NewRegistry()
	r.RegisterHistL("lat", []float64{1, 10}, []map[string]string{{"op": "x"}, {"op": "y"}})
	r.ObserveL("lat", map[string]string{"op": "x"}, 0.5)
	r.ObserveL("lat", map[string]string{"op": "x"}, 5)
	r.Help("lat", "Latency.")
	var b bytes.Buffer
	Render(&b, r)
	out := b.String()
	for _, want := range []string{
		"# HELP " + Prefix + "lat Latency.\n# TYPE " + Prefix + "lat histogram\n",
		Prefix + `lat_bucket{op="x",le="1"} 1`,
		Prefix + `lat_bucket{op="x",le="10"} 2`,
		Prefix + `lat_bucket{op="x",le="+Inf"} 2`,
		Prefix + `lat_sum{op="x"} 5.5`,
		Prefix + `lat_count{op="x"} 2`,
		Prefix + `lat_bucket{op="y",le="1"} 0`,
		Prefix + `lat_count{op="y"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "# TYPE "+Prefix+"lat "); n != 1 {
		t.Errorf("family typed %d times", n)
	}
	checkExposition(t, out)
}

func TestRenderEveryStandardMetricHasHelpBeforeType(t *testing.T) {
	o := obs.New(nil)
	o.Reg.ObserveL(obs.MQueryDuration, map[string]string{"op": "status"}, 0.01)
	var b bytes.Buffer
	Render(&b, o.Reg)
	lines := strings.Split(b.String(), "\n")
	types := 0
	for i, l := range lines {
		if !strings.HasPrefix(l, "# TYPE ") {
			continue
		}
		types++
		name := strings.Fields(l)[2]
		if i == 0 || !strings.HasPrefix(lines[i-1], "# HELP "+name+" ") {
			t.Errorf("no HELP line directly before %q", l)
		}
	}
	if types == 0 {
		t.Fatal("no TYPE lines")
	}
	checkExposition(t, b.String())
}

// TestRenderUnlabelledHistogramsGolden pins the phase 0 histogram series names
// and layout: the unlabelled histograms render exactly as before labelled ones
// existed.
func TestRenderUnlabelledHistogramsGolden(t *testing.T) {
	r := obs.NewRegistry()
	r.RegisterHist(obs.MCopyLagHistogram, obs.LagBuckets)
	r.RegisterHist(obs.MQueryDuration, obs.RequestBuckets)
	r.Observe(obs.MCopyLagHistogram, 0.3)
	r.Observe(obs.MQueryDuration, 0.003)
	r.Observe(obs.MQueryDuration, 3)
	var b bytes.Buffer
	Render(&b, r)
	want, err := os.ReadFile("testdata/unlabelled_histograms.golden")
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Fatalf("golden mismatch, got:\n%s\nwant:\n%s", b.String(), want)
	}
}
