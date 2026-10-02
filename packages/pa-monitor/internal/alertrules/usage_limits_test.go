// Package alertrules guards the provisioned Grafana alert rules shipped under
// packages/pa-monitor/grafana/alerting. The rules are YAML consumed by Grafana,
// so these tests pin the load-bearing fields textually (same approach as
// pg-router's internal/alertrules).
package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// usageRule returns the YAML block of the rule with the given uid in
// usage-limits.yaml, from its "- uid:" line up to the next rule (or EOF).
func usageRule(t *testing.T, uid string) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/usage-limits.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	marker := "- uid: " + uid
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("rule %s not found", uid)
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// pg2-hog2v: the 5h rule and its notification are WANTED (operator ruling
// 2026-09-30). It must exist, stay at warning severity, fire immediately
// (for: 0m) and NOT carry keepFiringFor, which would delay the clear after the
// window resets.
func TestFiveHourUsageLimitRuleFiresAndClearsPromptly(t *testing.T) {
	rule := usageRule(t, "pa-monitor-5h-usage-limit-hit")

	for _, need := range []string{
		"title: pa-monitor 5h usage limit hit",
		"condition: C",
		"for: 0m",
		"noDataState: OK",
		"execErrState: OK",
		"severity: warning",
		"window: 5h",
		"params: [0]",
	} {
		if !strings.Contains(rule, need) {
			t.Errorf("5h rule lost %q", need)
		}
	}

	// Only look at live YAML keys, not the explanatory comments (which mention
	// keepFiringFor on purpose).
	keepFiring := regexp.MustCompile(`(?m)^\s*keepFiringFor:`)
	if keepFiring.MatchString(rule) {
		t.Error("5h rule must not set keepFiringFor: it delays the clear after the window resets (pg2-hog2v)")
	}
	forDelay := regexp.MustCompile(`(?m)^\s*for:\s*(\S+)`).FindStringSubmatch(rule)
	if forDelay == nil || forDelay[1] != "0m" {
		t.Errorf("5h rule must fire immediately with for: 0m, got %v", forDelay)
	}
}

// The 5h rule's condition must keep both triggers: the percentage gauge and the
// limit-hit counter (pg2-l7s2l).
func TestFiveHourUsageLimitRuleCondition(t *testing.T) {
	rule := usageRule(t, "pa-monitor-5h-usage-limit-hit")
	for _, need := range []string{
		"expr: max(pa_monitor_block_usage_percentage)",
		"expr: sum(increase(pa_monitor_block_usage_limit_hits_total[5m])) or vector(0)",
		`expression: "($B > 99) || ($L > 0)"`,
	} {
		if !strings.Contains(rule, need) {
			t.Errorf("5h rule lost %q", need)
		}
	}
}

// The group must evaluate every minute: the evaluation interval is part of the
// documented worst-case detection delay.
func TestUsageLimitsGroupInterval(t *testing.T) {
	b, err := os.ReadFile("../../grafana/alerting/usage-limits.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^    interval: 1m$`).Match(b) {
		t.Error("usage-limits group interval drifted from 1m; update the worst-case detection delay comment in usage-limits.yaml")
	}
}

// The sibling weekly rule is intentionally untouched by pg2-hog2v.
func TestWeeklyUsageLimitRuleUnchanged(t *testing.T) {
	rule := usageRule(t, "pa-monitor-weekly-usage-limit-hit")
	for _, need := range []string{
		"for: 0m",
		"keepFiringFor: 5m",
		"noDataState: OK",
		"severity: warning",
		"window: weekly",
		"expr: max(pa_monitor_week_usage_percentage)",
	} {
		if !strings.Contains(rule, need) {
			t.Errorf("weekly rule lost %q", need)
		}
	}
}
