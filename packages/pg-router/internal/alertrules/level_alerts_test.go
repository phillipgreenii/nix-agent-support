package alertrules

import (
	"os"
	"strings"
	"testing"
)

// unquoteExpr strips the quotes a YAML scalar carries when the expression needs
// them.
func unquoteExpr(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

const awakeExpr = `((host_power_last_wake_timestamp_seconds{kind="full"} > on() host_power_last_sleep_timestamp_seconds) or (host_power_last_wake_timestamp_seconds{kind="full"} unless on() host_power_last_sleep_timestamp_seconds) or (absent(host_power_last_wake_timestamp_seconds{kind="full"}) and on() absent(host_power_last_sleep_timestamp_seconds) and on() count(host_power_transitions_total)))`

// The six level alerts. Each has a rule test in grafana/alerting/rule-tests named for its
// uid, a rule_uids entry in check-alert-rules.sh (TestLevelAlertsAreChecked) and static
// annotations (the probe copies them into beads).
var levelAlerts = []struct {
	uid, expr, forDur, noData string
}{
	{
		"pg-router-sensor-stale",
		`(max by (sensor)(pg_router_sensor_last_observed_age_seconds > 600) and on() ` + awakeExpr + `) or (vector(0) unless on() ` + awakeExpr + `)`,
		"for: 5m", "noDataState: Alerting",
	},
	{
		"pg-router-resource-held-nonbest",
		`max by (resource)(last_over_time(pg_router_resource_level[10m]) > 0)`,
		"for: 6h", "noDataState: OK",
	},
	{
		"pg-router-role-paused-long",
		`max by (role)(last_over_time(pg_router_role_paused[10m]) > 0)`,
		"for: 4h", "noDataState: OK",
	},
	{
		"pg-router-sensor-pull-failing",
		`sum by (sensor)(increase(pg_router_sensor_pull_failures_total[15m])) / sum by (sensor)(increase(pg_router_sensor_pull_total[15m])) > 0.3`,
		"for: 5m", "noDataState: OK",
	},
	{
		"pg-router-override-left",
		`max by (resource)(pg_router_resource_level_override_active == 1) and on(resource) pg_router_resource_level_lease_remaining_seconds`,
		"for: 12h", "noDataState: OK",
	},
	{
		"pg-router-level-events-rejected",
		`sum by (reason)(increase(pg_router_level_events_rejected_total[15m])) > 0`,
		"for: 0s", "noDataState: OK",
	},
}

func TestLevelAlerts(t *testing.T) {
	for _, tc := range levelAlerts {
		r := ruleBlock(t, tc.uid)
		if got := unquoteExpr(ruleExpr(t, r)); got != tc.expr {
			t.Errorf("%s expr:\n got %q\nwant %q", tc.uid, got, tc.expr)
		}
		for _, need := range []string{tc.forDur, tc.noData, "execErrState: Error", "severity: warning", "condition: C", "params: [0]", "type: gt", "reducer: last", "from: 600"} {
			if !strings.Contains(r, need) {
				t.Errorf("%s lost %q", tc.uid, need)
			}
		}
		i, j := strings.Index(r, "annotations:"), strings.Index(r, "data:")
		if i < 0 || j < i || strings.Contains(r[i:j], "{{") {
			t.Errorf("%s annotations must exist and be static", tc.uid)
		}
		if strings.Contains(r, "pg_router_gate") {
			t.Errorf("%s names a removed gate metric", tc.uid)
		}
	}
}

// A sleeping laptop must not page the sensor-stale rule even though no data alerts: the
// expression returns a 0 sample while the host sleeps (an empty result would read as no
// data), and its first branch is gated on the same awake condition the liveness rule uses.
func TestSensorStaleIsGatedOnAwake(t *testing.T) {
	r := ruleBlock(t, "pg-router-sensor-stale")
	live := unquoteExpr(ruleExpr(t, ruleBlock(t, "pg-router-liveness-down")))
	if !strings.Contains(live, awakeExpr) {
		t.Fatalf("test bug: the liveness rule no longer carries the awake condition %q", awakeExpr)
	}
	if got := strings.Count(unquoteExpr(ruleExpr(t, r)), awakeExpr); got != 2 {
		t.Errorf("sensor-stale carries the awake condition %d times, want 2 (the gate and the asleep branch)", got)
	}
	if !strings.Contains(r, "noDataState: Alerting") {
		t.Error("sensor-stale must alert on no data while the host is awake")
	}
}

// override-left excludes an until-cleared override: it joins on the lease series, which
// the metrics emit only for a level that has an expiry.
func TestOverrideLeftExcludesUntilCleared(t *testing.T) {
	got := unquoteExpr(ruleExpr(t, ruleBlock(t, "pg-router-override-left")))
	if !strings.Contains(got, "and on(resource) pg_router_resource_level_lease_remaining_seconds") {
		t.Errorf("override-left must join on the lease series: %q", got)
	}
}

// Every rule uid the Go tests pin is wrapped and tested by check-alert-rules.sh.
func TestLevelAlertsAreChecked(t *testing.T) {
	b := readFileT(t, "../../check-alert-rules.sh")
	for _, tc := range levelAlerts {
		if !strings.Contains(b, `"`+tc.uid+`"`) {
			t.Errorf("check-alert-rules.sh rule_uids lacks %s", tc.uid)
		}
		readFileT(t, "../../grafana/alerting/rule-tests/"+tc.uid+".test.yaml")
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
