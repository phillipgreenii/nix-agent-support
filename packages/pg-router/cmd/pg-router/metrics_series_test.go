package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
)

var metricLabelRE = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)="([^"]*)"`)

// seriesValue returns the value of every exposition line for metric name whose
// labels include all of want, matching per label (label order and extra
// otel_scope_* labels vary, so no fixed-string match).
func seriesValue(body, name string, want map[string]string) []float64 {
	var out []float64
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		open, closeIdx := strings.Index(line, "{"), strings.LastIndex(line, "}")
		if open < 0 || closeIdx < open {
			continue
		}
		got := map[string]string{}
		for _, m := range metricLabelRE.FindAllStringSubmatch(line[open+1:closeIdx], -1) {
			got[m[1]] = m[2]
		}
		ok := true
		for k, v := range want {
			if got[k] != v {
				ok = false
			}
		}
		if !ok {
			continue
		}
		fields := strings.Fields(line[closeIdx+1:])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// The orchestrator and metrics packages share the shutdown hand-back reason as two
// constants (metrics does not import orchestrator); this pins them equal.
func TestShutdownHandBackReasonIsSharedByOrchestratorAndMetrics(t *testing.T) {
	if orchestrator.DeclineDetailShutdown != metrics.ReasonShutdown {
		t.Fatalf("orchestrator.DeclineDetailShutdown = %q, metrics.ReasonShutdown = %q: they must be equal",
			orchestrator.DeclineDetailShutdown, metrics.ReasonShutdown)
	}
}
