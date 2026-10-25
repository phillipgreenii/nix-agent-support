package changes

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

// legacySpec is the registration as it was before the PR lane flip: a row for
// every observed PR (EmitAll) and delivery by query membership
// (DeliverByMembership). The engine tests that pin the detection, pending-row
// and settle rules run under it, because those rules do not depend on the modes
// and their fixtures are not watched.
func legacySpec() registry.Spec {
	s := Spec()
	s.Emission = registry.EmitAll
	s.Delivery = registry.DeliverByMembership
	return s
}

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

func TestTheRegistrationIsValidAndEmitsAndDeliversByTheWatchedFlag(t *testing.T) {
	s := Spec()
	if err := s.Validate(); err != nil {
		t.Fatalf("Spec().Validate() = %v", err)
	}
	if s.EntityKind != LogKindPR || !slices.Equal(s.Changes, append(classify.Kinds(), "added")) {
		t.Errorf("spec = %+v", s)
	}
	if s.Emission != registry.EmitWatchedOnly || s.Delivery != registry.DeliverByWatched {
		t.Errorf("emission = %v, delivery = %v, want EmitWatchedOnly and DeliverByWatched", s.Emission, s.Delivery)
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
