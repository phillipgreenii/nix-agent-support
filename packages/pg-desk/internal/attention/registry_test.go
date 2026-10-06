package attention

import (
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func TestInitialRuleKindsAreRegistered(t *testing.T) {
	want := []string{KindIssueStaleInProgress, KindOwnCIFailing, KindOwnNeedsAction, KindReviewRequested}
	if got := RuleKinds(); !reflect.DeepEqual(got, want) {
		t.Errorf("RuleKinds() = %v, want %v", got, want)
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register must panic on a duplicate rule kind, as classify.Register does")
		}
		if !strings.Contains(r.(string), KindReviewRequested) {
			t.Errorf("panic %q should name the kind", r)
		}
	}()
	Register(reviewRequested{})
}

func TestResolveDefaultsAndOverrides(t *testing.T) {
	no := false
	s, err := Resolve(config.AttentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for kind, wantSev := range map[string]Severity{KindIssueStaleInProgress: SeverityMedium, KindReviewRequested: SeverityMedium, KindOwnCIFailing: SeverityHigh, KindOwnNeedsAction: SeverityMedium} {
		got := s.Rules[kind]
		if !got.Enabled || got.Severity != wantSev || got.SeverityConfigured {
			t.Errorf("default for %s = %+v, want enabled at %s", kind, got, wantSev)
		}
	}

	s, err = Resolve(config.AttentionConfig{Rules: map[string]config.AttentionRuleConfig{
		KindOwnCIFailing:    {Enabled: &no},
		KindReviewRequested: {Severity: "high"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Rules[KindOwnCIFailing].Enabled {
		t.Error("enabled:false must disable the rule")
	}
	if got := s.Rules[KindReviewRequested]; got.Severity != SeverityHigh || !got.SeverityConfigured {
		t.Errorf("severity override = %+v", got)
	}
	if !s.Rules[KindOwnNeedsAction].Enabled {
		t.Error("a kind missing from the block takes its default")
	}
}

func TestResolveRejectsUnknownRuleKind(t *testing.T) {
	_, err := Resolve(config.AttentionConfig{Rules: map[string]config.AttentionRuleConfig{"pr.no-such-rule": {}}})
	if err == nil {
		t.Fatal("an unknown rule kind must be a configuration error")
	}
	for _, want := range []string{"pr.no-such-rule", KindOwnCIFailing} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q", err, want)
		}
	}
	// And Evaluate surfaces it rather than evaluating with a typo'd config.
	st := store.OpenNewSchemaForTest(t)
	cfg := baseConfig()
	cfg.Attention = config.AttentionConfig{Rules: map[string]config.AttentionRuleConfig{"pr.no-such-rule": {}}}
	if _, err := Evaluate(Inputs{Store: st, Repo: testRepo, Config: cfg, Clock: testClock()}); err == nil {
		t.Error("Evaluate must fail on an unknown rule kind")
	}
}

func TestConfiguredRuleBehavior(t *testing.T) {
	no := false
	st := store.OpenNewSchemaForTest(t)
	put(t, st, prSpec{number: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	put(t, st, prSpec{number: 2, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success", approvals: interpret.Approvals{HumanApproved: true}})

	cfg := baseConfig()
	cfg.Attention.Rules = map[string]config.AttentionRuleConfig{
		KindReviewRequested: {Enabled: &no},
		KindOwnNeedsAction:  {Severity: "high"},
	}
	res := evaluate(t, st, cfg)

	if _, ok := itemFor(res, 1); ok {
		t.Error("a disabled rule must not raise")
	}
	if why := res.Traces[Ref("pr", pid(1))].NotRaised[KindReviewRequested]; why != "disabled" {
		t.Errorf("explain reason = %q, want disabled", why)
	}
	it := mustItem(t, res, 2)
	if it.Severity != SeverityHigh {
		t.Errorf("an explicitly configured severity must override the approved-and-ready low default, got %s", it.Severity)
	}
}
