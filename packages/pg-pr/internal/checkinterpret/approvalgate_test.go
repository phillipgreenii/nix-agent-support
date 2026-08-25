package checkinterpret

import "testing"

func TestClassifyApprovalGate(t *testing.T) {
	cases := []struct {
		name        string
		conclusion  string
		description string
		want        Result
	}{
		{
			"all-approved sentence, agreeing conclusion", "success", "All rules are approved",
			Result{State: Satisfied},
		},
		{
			"0/1 with failing conclusion, fraction retained", "failure", "0/1 rules approved",
			Result{State: Unsatisfied, M: 1},
		},
		{
			"1/2 with failing conclusion, fraction retained", "failure", "1/2 rules approved",
			Result{State: PartiallySatisfied, N: 1, M: 2},
		},
		{
			"2/2 resolves via fraction path, not sentence path", "success", "2/2 rules approved",
			Result{State: Satisfied},
		},
		{
			"0/0 zero denominator is unknown, never satisfied", "failure", "0/0 rules approved",
			Result{State: Unknown},
		},
		{
			"3/2 n greater than m is unknown, never partially-satisfied", "failure", "3/2 rules approved",
			Result{State: Unknown},
		},
		{
			"non-numeric fraction-looking text", "failure", "x/y rules approved",
			Result{State: Unknown},
		},
		{
			"missing slash", "failure", "01 rules approved",
			Result{State: Unknown},
		},
		{
			"empty description", "success", "",
			Result{State: Unknown},
		},
		{
			"absent description (Go zero value, same as empty)", "failure", "",
			Result{State: Unknown},
		},
		{
			"leading/trailing whitespace and case variation still satisfied", "success", "   ALL rules ARE approved   ",
			Result{State: Satisfied},
		},
		{
			"long multi-sentence description, fraction embedded mid-string", "failure",
			"Evaluation started at 10:00 UTC. Waiting on required reviewers for this change. " +
				"1/2 rules approved. See the gate's own status page for the full rule tree and pending approvers.",
			Result{State: PartiallySatisfied, N: 1, M: 2},
		},
		// State/description disagreement, both directions — description
		// is authoritative (see ClassifyApprovalGate's doc comment,
		// design decision 6). An unpinned disagreement is how a silent
		// wrong verdict ships.
		{
			"disagreement: success conclusion, unsatisfied-shaped description", "success", "0/1 rules approved",
			Result{State: Unsatisfied, M: 1},
		},
		{
			"disagreement: failure conclusion, all-approved description", "failure", "All rules are approved",
			Result{State: Satisfied},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyApprovalGate(tc.conclusion, tc.description)
			if got != tc.want {
				t.Errorf("ClassifyApprovalGate(%q, %q) = %+v, want %+v", tc.conclusion, tc.description, got, tc.want)
			}
		})
	}
}

// TestClassifyApprovalGateUnknownNeverSatisfied pins, explicitly and by
// itself, the single most important negative case named in
// pg2-4dz88.2.4's brief: a parse failure must never read as Satisfied.
// Every description below is deliberately unparseable or degenerate.
func TestClassifyApprovalGateUnknownNeverSatisfied(t *testing.T) {
	descriptions := []string{
		"",
		"x/y rules approved",
		"0/0 rules approved",
		"3/2 rules approved",
		"no fraction and no sentence in this description at all",
	}
	for _, d := range descriptions {
		got := ClassifyApprovalGate("success", d)
		if got.State != Unknown {
			t.Errorf("ClassifyApprovalGate(%q, %q).State = %v, want Unknown", "success", d, got.State)
		}
	}
}
