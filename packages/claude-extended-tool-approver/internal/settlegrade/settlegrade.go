// Package settlegrade places every call of the claude-tool-tracker history by
// CETA's verdict and by who settled it (settled_by) and how (settlement), and
// ranks the calls CETA did not settle. It is the "who settled it" dimension of
// CETA's miss analysis (evaluate, report and show under --source tracker).
//
// # The grading table
//
// A call is placed in exactly one cell, in this priority order (the order the
// output lists them; the last cell is the lowest priority):
//
//	ask/person/approved       CETA said ask, a person approved   (points at an approve rule)
//	ask/person/refused        CETA said ask, a person refused    (points at a reject rule)
//	ask/converted             CETA said ask, an unattended session delivered a
//	                          reject (converted_ask): an ask for miss analysis
//	abstain/classifier/approved   CETA abstained, the classifier approved
//	abstain/classifier/refused    CETA abstained, the classifier refused
//	abstain/person            CETA abstained, a person approved or refused
//	abstain/rule-or-mode      CETA abstained, a rule or the permission mode settled it
//
// A call with no CETA verdict at all counts as an abstain (CETA had no opinion).
// A call CETA itself settled (approve or reject delivered, settled_by ceta, not a
// converted ask) is counted, not ranked. A call that fits no cell (settlement
// pending or unresolved, no settled_by, or a combination the tracker does not
// produce) is counted as not placed. Nothing is dropped silently.
//
// # Ranking
//
// Within a cell, calls are grouped by tool name, and a Bash call by its command
// class (asklog.CommandClass, the key report --group-by command uses). Groups
// are ranked by count, highest first, ties broken by group name.
package settlegrade

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
)

// Cell names one row of the grading table.
type Cell string

// The cells, in priority order.
const (
	AskPersonApproved         Cell = "ask/person/approved"
	AskPersonRefused          Cell = "ask/person/refused"
	AskConverted              Cell = "ask/converted"
	AbstainClassifierApproved Cell = "abstain/classifier/approved"
	AbstainClassifierRefused  Cell = "abstain/classifier/refused"
	AbstainPerson             Cell = "abstain/person"
	AbstainRuleOrMode         Cell = "abstain/rule-or-mode"
)

// Order is the priority order of the cells.
var Order = []Cell{
	AskPersonApproved, AskPersonRefused, AskConverted,
	AbstainClassifierApproved, AbstainClassifierRefused, AbstainPerson, AbstainRuleOrMode,
}

// Kind says what Place decided about a call.
type Kind int

const (
	// Placed means the call is in a ranked cell.
	Placed Kind = iota
	// SettledByCeta means CETA settled the call itself (not a converted ask).
	SettledByCeta
	// NotPlaced means the call fits no cell (see the package doc).
	NotPlaced
)

// Place decides where one call goes. The cell is meaningful only for Placed.
func Place(r trackeradapter.Row) (Cell, Kind) {
	if r.ConvertedAsk {
		return AskConverted, Placed
	}
	if r.SettledBy == "ceta" {
		return "", SettledByCeta
	}
	if r.Settlement != "approved" && r.Settlement != "refused" {
		return "", NotPlaced
	}
	verdict := r.EngineVerdict
	if verdict == "" {
		verdict = "abstain"
	}
	approved := r.Settlement == "approved"
	switch verdict {
	case "ask":
		if r.SettledBy != "person" {
			return "", NotPlaced
		}
		if approved {
			return AskPersonApproved, Placed
		}
		return AskPersonRefused, Placed
	case "abstain":
		switch r.SettledBy {
		case "classifier":
			if approved {
				return AbstainClassifierApproved, Placed
			}
			return AbstainClassifierRefused, Placed
		case "person":
			return AbstainPerson, Placed
		case "rule", "mode":
			return AbstainRuleOrMode, Placed
		}
	}
	return "", NotPlaced
}

// GroupOf is the key a call is ranked under: the tool name, and for Bash the
// command class.
func GroupOf(r trackeradapter.Row) string {
	if r.ToolName == "Bash" {
		return asklog.CommandClass(r.ToolName, json.RawMessage(r.ToolInputJSON), r.CWD)
	}
	return r.ToolName
}

// Group is one ranked group inside a cell.
type Group struct {
	Group string `json:"group"`
	Count int    `json:"count"`
}

// CellTable is one cell with its ranked groups.
type CellTable struct {
	Cell   Cell    `json:"cell"`
	Count  int     `json:"count"`
	Groups []Group `json:"groups"`
}

// Table is the whole grading table. Cells always lists every cell, in priority
// order, so an empty cell reads as zero rather than as missing.
type Table struct {
	Cells         []CellTable `json:"cells"`
	SettledByCeta int         `json:"settled_by_ceta"`
	NotPlaced     int         `json:"not_placed"`
}

// Build places every row and ranks the result.
func Build(rows []trackeradapter.Row) Table {
	byCell := map[Cell]map[string]int{}
	var t Table
	for _, r := range rows {
		cell, kind := Place(r)
		switch kind {
		case SettledByCeta:
			t.SettledByCeta++
		case NotPlaced:
			t.NotPlaced++
		default:
			if byCell[cell] == nil {
				byCell[cell] = map[string]int{}
			}
			byCell[cell][GroupOf(r)]++
		}
	}
	for _, c := range Order {
		ct := CellTable{Cell: c, Groups: []Group{}}
		for g, n := range byCell[c] {
			ct.Groups = append(ct.Groups, Group{Group: g, Count: n})
			ct.Count += n
		}
		sort.Slice(ct.Groups, func(i, j int) bool {
			if ct.Groups[i].Count != ct.Groups[j].Count {
				return ct.Groups[i].Count > ct.Groups[j].Count
			}
			return ct.Groups[i].Group < ct.Groups[j].Group
		})
		t.Cells = append(t.Cells, ct)
	}
	return t
}

// Format renders the table as text: one block per cell in priority order, the
// groups ranked under it, then the two counted-but-not-ranked totals.
func (t Table) Format() string {
	var b strings.Builder
	b.WriteString("Who settled it (calls CETA did not settle, by priority):\n")
	for _, c := range t.Cells {
		fmt.Fprintf(&b, "  %-30s %5d\n", c.Cell, c.Count)
		for _, g := range c.Groups {
			fmt.Fprintf(&b, "      %5d  %s\n", g.Count, g.Group)
		}
	}
	fmt.Fprintf(&b, "  %-30s %5d\n", "settled by CETA (not ranked)", t.SettledByCeta)
	fmt.Fprintf(&b, "  %-30s %5d\n", "not placed (pending/unresolved/other)", t.NotPlaced)
	return b.String()
}
