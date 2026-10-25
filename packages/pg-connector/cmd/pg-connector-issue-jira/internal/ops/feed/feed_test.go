package feed

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
)

func TestNew_DefaultsTheSpecToTheJiraRegistration(t *testing.T) {
	r := newRig(t)
	f, err := New(Config{Store: r.st, Queries: r.queries, Retention: time.Hour, ConsumerStaleAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.Spec().EntityKind, changes.Spec().EntityKind; got != want || got == "" {
		t.Errorf("EntityKind = %q, want %q", got, want)
	}
	if f.Spec().Delivery != changes.Spec().Delivery {
		t.Errorf("Delivery = %v, want the registration's", f.Spec().Delivery)
	}
}

func TestNew_RefusesIncompleteConfig(t *testing.T) {
	r := newRig(t)
	cases := map[string]Config{
		"no store":     {Queries: r.queries, Retention: time.Hour, ConsumerStaleAfter: time.Hour},
		"no queries":   {Store: r.st, Retention: time.Hour, ConsumerStaleAfter: time.Hour},
		"no retention": {Store: r.st, Queries: r.queries, ConsumerStaleAfter: time.Hour},
		"no stale":     {Store: r.st, Queries: r.queries, Retention: time.Hour},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}
