package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// TestRoleListener_RetryBackoff_roleOverrideSelected is Task 1.3's first
// required RED test: a role carrying its OWN non-zero backoff.Policy (a
// [role.retry] override — config decode already merges it onto the pool
// default, so a config-decoded role's RetryBackoff field is never zero even
// unconfigured; a hand-built Role literal here stands in for that) is
// selected over the pool-wide default injected at construction.
func TestRoleListener_RetryBackoff_roleOverrideSelected(t *testing.T) {
	pool := backoff.Policy{Initial: time.Second, Factor: 2, Max: time.Minute}
	override := backoff.Policy{Initial: 3 * time.Second, Factor: 3, Max: 5 * time.Minute}
	o := &Orchestrator{Cfg: config.Config{RetryBackoff: pool}}
	role := roles.Role{Name: "custom", RetryBackoff: override}

	l := o.NewListener(context.Background(), role)
	bl, ok := l.(eventqueue.BackoffListener)
	if !ok {
		t.Fatalf("roleListener returned by NewListener does not implement eventqueue.BackoffListener")
	}
	if got := bl.RetryBackoff(); got != override {
		t.Fatalf("RetryBackoff() = %+v, want the role's own override %+v", got, override)
	}
}

// TestRoleListener_RetryBackoff_builtinKeepsPoolCadence is Task 1.3's second
// required RED test: a role carrying the ZERO backoff.Policy — every built-in
// role, per roles.BuiltinRoleSet, which never sets RetryBackoff at all — MUST
// keep the injected pool-wide [pool.retry] cadence, never fall through to
// backoff.Default() via backoff.Policy.Duration's own sanitized(). pool is
// deliberately chosen to differ from backoff.Default()'s own values, so a
// silent fall-through to the package default would be observable.
func TestRoleListener_RetryBackoff_builtinKeepsPoolCadence(t *testing.T) {
	pool := backoff.Policy{Initial: 3 * time.Second, Factor: 3, Max: 5 * time.Minute}
	if pool == backoff.Default() {
		t.Fatalf("test fixture bug: pool %+v must differ from backoff.Default() %+v", pool, backoff.Default())
	}
	o := &Orchestrator{Cfg: config.Config{RetryBackoff: pool}}
	role := roles.Role{Name: "feedback"} // zero-value RetryBackoff, as every built-in role carries

	l := o.NewListener(context.Background(), role)
	bl, ok := l.(eventqueue.BackoffListener)
	if !ok {
		t.Fatalf("roleListener returned by NewListener does not implement eventqueue.BackoffListener")
	}
	if got := bl.RetryBackoff(); got != pool {
		t.Fatalf("RetryBackoff() = %+v, want the injected pool default %+v", got, pool)
	}
}
