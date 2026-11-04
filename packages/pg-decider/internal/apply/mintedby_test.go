package apply

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

func TestMintedByAddsTheLabelSortedAndOnce(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"none", nil, []string{MintedByLabel}},
		{"sorted with others", []string{"zeta", "alpha"}, []string{"alpha", MintedByLabel, "zeta"}},
		{"already present", []string{MintedByLabel, "human"}, []string{"human", MintedByLabel}},
	}
	for _, c := range cases {
		if got := MintedBy(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: MintedBy(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestCreateArgsAlwaysCarriesTheMintedByLabel(t *testing.T) {
	args := strings.Join(createArgs(Env{}, action.Fields{Title: "t"}), " ")
	if !strings.Contains(args, "--labels "+MintedByLabel) {
		t.Fatalf("a create with no labels lacks the minted-by label: %s", args)
	}
}

// labelsOf reads the --labels value of a create argv ("" when absent).
func labelsOf(args []string) []string {
	for i, a := range args {
		if a == "--labels" && i+1 < len(args) {
			return strings.Split(args[i+1], ",")
		}
	}
	return nil
}

// Every kind the decider mints, through the real createArgs path, carries the
// stamp and keeps the labels its contract row lists.
func TestCreateArgsStampsEveryMintedKind(t *testing.T) {
	for _, k := range workitem.Kinds() {
		c := workitem.ContractFor(k)
		got := labelsOf(createArgs(Env{}, action.Fields{Title: "t", IssueType: c.IssueType, Labels: c.Labels}))
		if !slices.Contains(got, MintedByLabel) {
			t.Errorf("kind %s: labels %q lack %s", k, got, MintedByLabel)
		}
		for _, l := range c.Labels {
			if !slices.Contains(got, l) {
				t.Errorf("kind %s: contract label %q dropped from %q", k, l, got)
			}
		}
	}
}

// The decider's escalation beads carry human and the stamp together.
func TestCreateArgsStampsHumanEscalationAndKeepsHuman(t *testing.T) {
	got := labelsOf(createArgs(Env{}, action.Fields{Title: "t", Labels: []string{"human"}}))
	if !reflect.DeepEqual(got, []string{"human", MintedByLabel}) {
		t.Fatalf("labels = %q, want human and the stamp", got)
	}
}

// INV-DECIDER-4: the stamp is written at creation only. An update never adds it
// and never touches the labels of an existing (human) item.
func TestUpdateArgsNeverAddsTheStamp(t *testing.T) {
	args := updateArgs(Env{}, "bd-1", action.Fields{Title: "t", AddLabels: []string{"fbsum:x"}}, false)
	for i, a := range args {
		if strings.Contains(a, MintedByLabel) {
			t.Fatalf("update argv %v carries the stamp at %d", args, i)
		}
	}
	if !slices.Contains(args, "fbsum:x") {
		t.Fatalf("update argv %v lost the requested label", args)
	}
}
