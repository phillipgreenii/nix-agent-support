package changes

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
)

// encodeAwaited is the stored form of an awaited list, for the tests that seed
// a pending row directly.
func encodeAwaited(aw []Awaiting) (json.RawMessage, error) { return json.Marshal(aw) }

func TestNewAppliesTheGitHubDefaultsToAZeroConfigAndRejectsNegatives(t *testing.T) {
	if _, err := New(Config{}); err != nil {
		t.Fatalf("a zero Config was rejected: %v", err)
	}
	if _, err := New(Config{SettleTimeout: -1}); err == nil {
		t.Error("a negative settle timeout was accepted")
	}
	if _, err := New(Config{BodyCap: -1}); err == nil {
		t.Error("a negative body cap was accepted")
	}
}

func TestTheRegistrationIsValidAndKeepsTheLegacyModes(t *testing.T) {
	s := Spec()
	if err := s.Validate(); err != nil {
		t.Fatalf("Spec().Validate() = %v", err)
	}
	if s.EntityKind != LogKindPR || !slices.Equal(s.Changes, append(classify.Kinds(), "added")) {
		t.Errorf("spec = %+v", s)
	}
	if !s.Membership.StoreRows || !s.AdHocQueryWatch {
		t.Errorf("membership policy = %+v, ad hoc watch = %v", s.Membership, s.AdHocQueryWatch)
	}
}

// TestRefFold is the GitHub-flavoured fold assertion: owner and repo compare
// case-insensitively, the number exactly.
func TestRefFoldFoldsOwnerAndRepoButNotTheNumber(t *testing.T) {
	fold := Spec().FoldRef
	if fold("Acme-Org/Widget#3") != fold("acme-org/widget#3") {
		t.Error("the owner/repo case mattered")
	}
	if fold("Acme-Org/Widget#3") == fold("Acme-Org/Widget#4") {
		t.Error("the number was folded away")
	}
	if got := fold("Acme-Org/Widget#3"); got != "acme-org/widget#3" {
		t.Errorf("fold = %q", got)
	}
	if got := fold("NoNumber"); got != "nonumber" {
		t.Errorf("fold without a number = %q", got)
	}
}
