package groupsvc_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// These tests call the GitHub ErrorPolicy directly: nothing wires it into a
// service before the facade does.

func TestTheGitHubErrorPolicyReadsARateLimitAnswerAsAPauseThatIsNotAnAuthenticationFailure(t *testing.T) {
	clk := governor.NewManualClock(groupsvctest.T0)
	gov := newGov(t, clk, 1000)
	var p groupsvc.ErrorPolicy
	// gh turns every 403 into an authentication error: the text is what tells.
	answer := fmt.Errorf("gh: You have exceeded a secondary rate limit (HTTP 403): %w", vcs.ErrAuthInvalid)

	got := p.Classify(gov, time.Minute, answer)
	var rl *ratefeed.RateLimitError
	if !errors.As(got, &rl) || errors.Is(got, vcs.ErrAuthInvalid) {
		t.Fatalf("Classify = %v, want a rate-limit error that is not an authentication failure", got)
	}
	if !p.IsRateLimit(got) {
		t.Error("IsRateLimit = false for the classified rate-limit answer")
	}
	if p.IsUnauthenticated(got) {
		t.Error("IsUnauthenticated = true for a rate-limit answer")
	}
	bs, _ := gov.State().Bucket(governor.BucketGraphQL)
	if !bs.Paused {
		t.Error("the background classes were not paused")
	}
	// Background spend is refused for the pause; an interactive read still goes.
	if err := gov.Check(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassKeepAlive, Cost: 1}); !errors.Is(err, governor.ErrRefused) {
		t.Errorf("background check = %v, want a refusal", err)
	}
	if err := gov.Check(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Cost: 1}); err != nil {
		t.Errorf("interactive check = %v", err)
	}
}

func TestTheGitHubErrorPolicyReadsABadCredentialAsUnauthenticated(t *testing.T) {
	clk := governor.NewManualClock(groupsvctest.T0)
	gov := newGov(t, clk, 1000)
	var p groupsvc.ErrorPolicy
	answer := fmt.Errorf("gh: Bad credentials (HTTP 401): %w", vcs.ErrAuthInvalid)

	got := p.Classify(gov, time.Minute, answer)
	if got != answer {
		t.Errorf("Classify changed a plain authentication failure: %v", got)
	}
	if !p.IsUnauthenticated(got) || p.IsRateLimit(got) {
		t.Errorf("IsUnauthenticated = %v, IsRateLimit = %v, want true and false", p.IsUnauthenticated(got), p.IsRateLimit(got))
	}
	if bs, _ := gov.State().Bucket(governor.BucketGraphQL); bs.Paused {
		t.Error("an authentication failure paused the background classes")
	}
	if p.IsUnauthenticated(errors.New("gh: something else")) || p.IsUnauthenticated(nil) {
		t.Error("IsUnauthenticated is true for an error that does not wrap vcs.ErrAuthInvalid")
	}
}

func TestTheGitHubErrorPolicyNeverReclassifiesAGovernorRefusal(t *testing.T) {
	gov := newGov(t, governor.NewManualClock(groupsvctest.T0), 1000)
	var p groupsvc.ErrorPolicy
	refusal := &governor.Refusal{Reason: "paused: rate limit"}

	if got := p.Classify(gov, time.Minute, refusal); got != error(refusal) {
		t.Errorf("Classify rewrote a refusal: %v", got)
	}
	if p.IsRateLimit(refusal) {
		t.Error("IsRateLimit is true for a refusal: it must not extend the pause that caused it")
	}
}
