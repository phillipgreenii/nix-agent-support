package detail

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestErrorPolicyClassifiesByTheScriptoutTaxonomy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"unauthenticated", &origin.CallError{Kind: origin.KindUnauthenticated, Err: errors.New("pjira: 401 Unauthorized")}, scriptout.ErrUnauthenticated},
		{"rate limited", &origin.CallError{Kind: origin.KindRateLimited, Err: errors.New(text429)}, scriptout.ErrUnavailable},
		{"not found", &origin.CallError{Kind: origin.KindError, Err: errors.New("pjira: issue does not exist or not found")}, scriptout.ErrNotFound},
		{"other failure", &origin.CallError{Kind: origin.KindError, Err: errors.New("pjira: search: status 500")}, scriptout.ErrUnavailable},
		{"plain error", errors.New("decode failed"), scriptout.ErrUnavailable},
		{"paused", &origin.PausedError{Until: time.Now().Add(time.Minute), Reason: "429"}, scriptout.ErrUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ErrorPolicy{}.Classify(nil, time.Minute, c.err)
			if !errors.Is(got, c.want) {
				t.Errorf("Classify = %v, want %v", got, c.want)
			}
			if !errors.Is(got, c.err) {
				t.Errorf("Classify dropped the original error: %v", got)
			}
			// The policy still recognises what it classified.
			p := ErrorPolicy{}
			if p.IsRateLimit(got) != p.IsRateLimit(c.err) || p.IsUnauthenticated(got) != p.IsUnauthenticated(c.err) {
				t.Errorf("classified error lost its rate-limit/unauthenticated reading: %v", got)
			}
		})
	}
}

func TestErrorPolicyReadsTheOriginsAnswers(t *testing.T) {
	p := ErrorPolicy{}
	rl := &origin.CallError{Kind: origin.KindRateLimited, Err: errors.New(text429)}
	ua := &origin.CallError{Kind: origin.KindUnauthenticated, Err: errors.New("401")}
	if !p.IsRateLimit(rl) || p.IsRateLimit(ua) || p.IsRateLimit(errors.New("x")) {
		t.Error("IsRateLimit is not origin.IsRateLimited")
	}
	if !p.IsUnauthenticated(ua) || p.IsUnauthenticated(rl) || p.IsUnauthenticated(errors.New("x")) {
		t.Error("IsUnauthenticated is not origin.IsUnauthenticated")
	}
	if !p.IsRateLimit(fmt.Errorf("wrapped: %w", rl)) {
		t.Error("a wrapped rate-limit answer is not recognised")
	}
}

func TestErrorPolicyLeavesNilAndGovernorRefusalsAlone(t *testing.T) {
	if got := (ErrorPolicy{}).Classify(nil, 0, nil); got != nil {
		t.Errorf("Classify(nil) = %v", got)
	}
	refusal := fmt.Errorf("admit: %w", &governor.Refusal{Kind: governor.RefusedCap})
	if got := (ErrorPolicy{}).Classify(nil, 0, refusal); got != refusal {
		t.Errorf("a governor refusal was reclassified: %v", got)
	}
}

func TestErrorPolicyNeverPausesTheGovernor(t *testing.T) {
	h := newHarness(t)
	rl := &origin.CallError{Kind: origin.KindRateLimited, Err: errors.New(text429)}
	_ = ErrorPolicy{}.Classify(h.gov, time.Hour, rl)
	if bs, ok := h.gov.State().Bucket(origin.Bucket); ok && bs.Paused {
		t.Errorf("the policy paused the governor: %+v", bs)
	}
}
