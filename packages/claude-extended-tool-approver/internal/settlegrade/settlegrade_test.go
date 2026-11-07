package settlegrade

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
)

func row(tool, input, engine, by, settlement string, converted bool) trackeradapter.Row {
	r := trackeradapter.Row{EngineVerdict: engine, SettledBy: by, Settlement: settlement, ConvertedAsk: converted}
	r.ToolName = tool
	r.ToolInputJSON = input
	return r
}

func TestPlace(t *testing.T) {
	cases := []struct {
		name string
		r    trackeradapter.Row
		cell Cell
		kind Kind
	}{
		{"ask person approved", row("Bash", "{}", "ask", "person", "approved", false), AskPersonApproved, Placed},
		{"ask person refused", row("Bash", "{}", "ask", "person", "refused", false), AskPersonRefused, Placed},
		{"converted ask is an ask", row("Bash", "{}", "ask", "ceta", "refused", true), AskConverted, Placed},
		{"classifier approved", row("Bash", "{}", "abstain", "classifier", "approved", false), AbstainClassifierApproved, Placed},
		{"classifier refused", row("Bash", "{}", "abstain", "classifier", "refused", false), AbstainClassifierRefused, Placed},
		{"abstain person approved", row("Bash", "{}", "abstain", "person", "approved", false), AbstainPerson, Placed},
		{"abstain person refused", row("Bash", "{}", "abstain", "person", "refused", false), AbstainPerson, Placed},
		{"rule", row("Bash", "{}", "abstain", "rule", "approved", false), AbstainRuleOrMode, Placed},
		{"mode", row("Bash", "{}", "abstain", "mode", "refused", false), AbstainRuleOrMode, Placed},
		{"no CETA verdict counts as abstain", row("Bash", "{}", "", "rule", "approved", false), AbstainRuleOrMode, Placed},
		{"CETA settled", row("Bash", "{}", "approve", "ceta", "approved", false), "", SettledByCeta},
		{"pending is not placed", row("Bash", "{}", "ask", "person", "pending", false), "", NotPlaced},
		{"unresolved is not placed", row("Bash", "{}", "ask", "person", "unresolved", false), "", NotPlaced},
		{"no settled_by is not placed", row("Bash", "{}", "abstain", "", "approved", false), "", NotPlaced},
		{"ask settled by a rule fits no cell", row("Bash", "{}", "ask", "rule", "approved", false), "", NotPlaced},
	}
	for _, c := range cases {
		cell, kind := Place(c.r)
		if cell != c.cell || kind != c.kind {
			t.Errorf("%s: got (%q, %d), want (%q, %d)", c.name, cell, kind, c.cell, c.kind)
		}
	}
}

func TestBuildRanksByCountThenName(t *testing.T) {
	rows := []trackeradapter.Row{
		row("Write", "{}", "ask", "person", "approved", false),
		row("Bash", `{"command":"zeta"}`, "ask", "person", "approved", false),
		row("Bash", `{"command":"alpha"}`, "ask", "person", "approved", false),
		row("Bash", `{"command":"alpha"}`, "ask", "person", "approved", false),
		row("Bash", `{"command":"x"}`, "approve", "ceta", "approved", false),
		row("Bash", `{"command":"x"}`, "ask", "person", "pending", false),
	}
	tab := Build(rows)
	if len(tab.Cells) != len(Order) || tab.Cells[0].Cell != AskPersonApproved || tab.Cells[len(Order)-1].Cell != AbstainRuleOrMode {
		t.Fatalf("cells not in priority order: %+v", tab.Cells)
	}
	got := tab.Cells[0].Groups
	want := []Group{{"alpha", 2}, {"Write", 1}, {"zeta", 1}}
	if !reflect.DeepEqual(got, want) || tab.Cells[0].Count != 4 {
		t.Errorf("groups %+v, want %+v", got, want)
	}
	if tab.SettledByCeta != 1 || tab.NotPlaced != 1 {
		t.Errorf("counted: ceta=%d notPlaced=%d", tab.SettledByCeta, tab.NotPlaced)
	}
}
