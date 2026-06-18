package main

import (
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestResolveRole(t *testing.T) {
	d := config.Default()
	rs := roles.BuiltinRoleSet(roles.BuiltinParams{
		WorktreeDir:  d.WorktreeDir,
		MaxFeedback:  d.MaxFeedback,
		MaxWorker:    d.MaxWorker,
		WorkerBudget: d.WorkerBudget(),
	})
	if r, ok := resolveRole(rs, "feedback"); !ok || r.Name != "feedback" {
		t.Errorf("feedback should resolve; ok=%v r=%+v", ok, r)
	}
	if r, ok := resolveRole(rs, "worker"); !ok || r.Name != "worker" {
		t.Errorf("worker should resolve; ok=%v r=%+v", ok, r)
	}
	if _, ok := resolveRole(rs, "bogus"); ok {
		t.Errorf("unknown role must not resolve")
	}
}

func TestRoleNames(t *testing.T) {
	rs := roles.RoleSet{{Name: "feedback"}, {Name: "worker"}}
	if got := roleNames(rs); got != "feedback, worker" {
		t.Errorf("roleNames = %q, want \"feedback, worker\"", got)
	}
}
