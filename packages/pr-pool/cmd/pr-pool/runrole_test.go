package main

import (
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestResolveRole(t *testing.T) {
	reg := roles.NewRegistry(config.Default())
	if r, ok := resolveRole(reg, "feedback"); !ok || r != reg.Feedback {
		t.Errorf("feedback should resolve to reg.Feedback; ok=%v r=%+v", ok, r)
	}
	if r, ok := resolveRole(reg, "worker"); !ok || r != reg.Worker {
		t.Errorf("worker should resolve to reg.Worker; ok=%v r=%+v", ok, r)
	}
	if _, ok := resolveRole(reg, "bogus"); ok {
		t.Errorf("unknown role must not resolve")
	}
}
