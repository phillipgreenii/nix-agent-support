package apply

import (
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
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
