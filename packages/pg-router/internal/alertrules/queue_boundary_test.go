package alertrules

import (
	"strings"
	"testing"
)

// pg2-24s66z: pg-router-queue-depth-growing compares its rise with the backlog (a ratio
// against the 30m average depth), so a large standing backlog hides growth from it;
// pg-router-queue-slow-growth has no such ratio and catches it. The two rules are
// complementary by ruling (DEC-OBS-10), and each rule's description MUST name the other so
// the boundary is visible on the alert itself. The uids are the rules' own; the guard reads
// the descriptions out of alerts.yaml and does not retype them.
func TestQueueRulesNameTheirBoundary(t *testing.T) {
	const depth, slow = "pg-router-queue-depth-growing", "pg-router-queue-slow-growth"
	for _, tc := range []struct{ rule, other string }{{depth, slow}, {slow, depth}} {
		r := ruleBlock(t, tc.rule)
		i := strings.Index(r, "description:")
		j := strings.Index(r, "data:")
		if i < 0 || j < i {
			t.Fatalf("%s: description not found", tc.rule)
		}
		d := r[i:j]
		if !strings.Contains(d, tc.other) {
			t.Errorf("%s description must name the complementary rule %s", tc.rule, tc.other)
		}
		if !strings.Contains(d, "complementary") {
			t.Errorf("%s description must state that the two rules are complementary", tc.rule)
		}
	}
}
