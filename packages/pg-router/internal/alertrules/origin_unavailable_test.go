package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// originRule returns the YAML block of rule pg-router-origin-unavailable.
func originRule(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: pg-router-origin-unavailable")
	if i < 0 {
		t.Fatal("rule pg-router-origin-unavailable not found")
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestOriginUnavailableRule(t *testing.T) {
	rest := originRule(t)
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	const wantOriginExpr = `sum(rate(pg_router_failures_total{reason="origin-unavailable"}[10m]))`
	if m[1] != wantOriginExpr {
		t.Errorf("expr drifted:\n got %q\nwant %q", m[1], wantOriginExpr)
	}
	for _, need := range []string{"for: 15m", "noDataState: OK", "execErrState: Error", "severity: warning", "params: [0]"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
}

// The annotations MUST be the static per-class remediation map: no templated
// label/value interpolation (which could carry stderr or URLs) and every probe
// class present.
func TestOriginUnavailableAnnotationsAreStatic(t *testing.T) {
	rest := originRule(t)
	i := strings.Index(rest, "annotations:")
	j := strings.Index(rest, "data:")
	if i < 0 || j < i {
		t.Fatal("annotations block not found")
	}
	ann := rest[i:j]
	if strings.Contains(ann, "{{") {
		t.Errorf("annotations must not interpolate labels/values: %q", ann)
	}
	for _, need := range []string{
		"mount-missing: check that the checkout volume is mounted",
		"auth-unavailable: renew the step cert in an interactive shell",
		"network:", "timeout:", "unknown:",
		"origin status", "origin ignore",
	} {
		if !strings.Contains(ann, need) {
			t.Errorf("annotations lost %q", need)
		}
	}
}
