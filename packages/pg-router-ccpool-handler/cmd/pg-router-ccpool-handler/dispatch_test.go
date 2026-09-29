package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/complete"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// fixedClock returns a now func() time.Time seam that always answers t —
// mirroring the fixed-clock fakes used throughout this module's own tests
// (e.g. internal/executor/ccpool_test.go's Deps.Now overrides).
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestStampExternalID_matchesDocumentedShape proves stampExternalID — the
// fix for pg2-nmpvs (buildDeps never stamped Deps.ExternalID, so it stayed
// Go's zero value "" and every real ccpool dispatch failed at `ccpool new`
// with "ExternalID is required") — produces a non-empty id in
// roles.Role.ExternalID's own documented <prefix><name>-<beadid>-<stamp>
// shape (internal/roles/roles.go:66-69), using this module's own
// pre-reserved stamp layout: internal/dtest.TestStamp ("20260616T010203")
// documents the second-precision "20060102T150405" layout this fix reuses
// (with nanosecond precision appended — see externalIDStampLayout's own doc
// comment), so a fixed clock at that exact instant reproduces TestStamp as
// this id's stamp prefix.
func TestStampExternalID_matchesDocumentedShape(t *testing.T) {
	role := roles.Role{Name: "worker"}
	at := time.Date(2026, 6, 16, 1, 2, 3, 0, time.UTC)

	got := stampExternalID(role, "pg-router-", "TEST-VERIFY-000", fixedClock(at))

	if got == "" {
		t.Fatal("stampExternalID returned the empty string — the original pg2-nmpvs bug's zero value")
	}
	wantPrefix := role.ExternalID("pg-router-", "TEST-VERIFY-000", dtest.TestStamp)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("stampExternalID = %q, want it to start with %q (role.ExternalID with this module's dtest.TestStamp convention)", got, wantPrefix)
	}
	const wantNamePrefix = "pg-router-worker-TEST-VERIFY-000-"
	if !strings.HasPrefix(got, wantNamePrefix) {
		t.Fatalf("external id %q missing documented <prefix><name>-<beadid>- shape %q", got, wantNamePrefix)
	}
	if stamp := strings.TrimPrefix(got, wantNamePrefix); stamp == "" {
		t.Errorf("external id %q has no stamp suffix after prefix %q", got, wantNamePrefix)
	}
}

// TestStampExternalID_defaultsToTimeNow proves a nil clock — buildDeps/
// runDispatch's own production call, since buildDeps never sets Deps.Now —
// does not panic and still mints a real, non-zero-value stamp rather than
// reproducing the original bug's empty ExternalID.
func TestStampExternalID_defaultsToTimeNow(t *testing.T) {
	role := roles.Role{Name: "worker"}

	got := stampExternalID(role, "pg-router-", "TEST-VERIFY-000", nil)

	zero := role.ExternalID("pg-router-", "TEST-VERIFY-000", "")
	if got == "" || got == zero {
		t.Fatalf("stampExternalID(nil clock) = %q, want a real time.Now()-stamped id, not the zero-stamp value %q", got, zero)
	}
}

// TestStampExternalID_distinctPerAttempt proves two dispatches for the SAME
// role+bead at different attempt times don't collide — pg2-nmpvs's
// acceptance criterion 2. Two fixed, distinct clock instants (rather than two
// bare time.Now() calls) keep this deterministic while still proving the
// stamp formula: two different "now" instants must never mint the same
// ExternalID for the same role+bead.
func TestStampExternalID_distinctPerAttempt(t *testing.T) {
	role := roles.Role{Name: "worker"}
	t1 := time.Date(2026, 6, 16, 1, 2, 3, 0, time.UTC)
	t2 := t1.Add(time.Second)

	id1 := stampExternalID(role, "pg-router-", "TEST-VERIFY-000", fixedClock(t1))
	id2 := stampExternalID(role, "pg-router-", "TEST-VERIFY-000", fixedClock(t2))

	if id1 == "" || id2 == "" {
		t.Fatalf("stampExternalID must never return the zero value; got %q and %q", id1, id2)
	}
	if id1 == id2 {
		t.Fatalf("two attempts for the same role+bead at different times collided: both %q", id1)
	}
}

// TestStampExternalID_distinctWithinSameSecond proves the appended
// nanosecond precision (externalIDStampLayout's own doc comment) actually
// does its job: two attempts stamped within the SAME wall-clock second still
// mint distinct ids, which a bare second-resolution stamp (this module's
// internal/dtest.TestStamp fixture on its own) could not guarantee.
func TestStampExternalID_distinctWithinSameSecond(t *testing.T) {
	role := roles.Role{Name: "worker"}
	t1 := time.Date(2026, 6, 16, 1, 2, 3, 123, time.UTC)
	t2 := time.Date(2026, 6, 16, 1, 2, 3, 456, time.UTC) // same second, different nanosecond

	id1 := stampExternalID(role, "pg-router-", "TEST-VERIFY-000", fixedClock(t1))
	id2 := stampExternalID(role, "pg-router-", "TEST-VERIFY-000", fixedClock(t2))

	if id1 == id2 {
		t.Fatalf("two attempts within the same wall-clock second collided: both %q", id1)
	}
}

// TestBuildDeps_scopesCCToRolePoolDir proves buildDeps (bead pg2-mr0sl)
// actually threads a ccpool role's own PoolDir into the returned Deps.CC —
// not merely accept it as a parameter and drop it — by asserting the
// concrete *ccpool.CLIRunner's own PoolDir field. Without this wiring, dev-
// ing on "one dedicated pool per role" (this bead's whole point) would still
// silently dispatch every role against whatever CCPOOL_POOL this process
// happened to inherit.
func TestBuildDeps_scopesCCToRolePoolDir(t *testing.T) {
	role := roles.Role{Name: "review", CCPool: &roles.CCPoolConfig{PoolDir: "/state/pg-router-ccpool-review"}}

	deps := buildDeps(config.Default(), role)

	cli, ok := deps.CC.(*ccpool.CLIRunner)
	if !ok {
		t.Fatalf("Deps.CC = %T, want *ccpool.CLIRunner", deps.CC)
	}
	if cli.PoolDir != "/state/pg-router-ccpool-review" {
		t.Errorf("CLIRunner.PoolDir = %q, want %q", cli.PoolDir, "/state/pg-router-ccpool-review")
	}
}

// TestBuildDeps_noPoolDirLeavesCCUnscoped is the positive control: a role
// with no PoolDir (command roles, or a ccpool role that never opted in)
// gets a CLIRunner with PoolDir == "" — no override, exactly today's
// unchanged single-pool behavior.
func TestBuildDeps_noPoolDirLeavesCCUnscoped(t *testing.T) {
	role := roles.Role{Name: "worker", CCPool: &roles.CCPoolConfig{}}

	deps := buildDeps(config.Default(), role)

	cli, ok := deps.CC.(*ccpool.CLIRunner)
	if !ok {
		t.Fatalf("Deps.CC = %T, want *ccpool.CLIRunner", deps.CC)
	}
	if cli.PoolDir != "" {
		t.Errorf("CLIRunner.PoolDir = %q, want \"\" (no per-role override configured)", cli.PoolDir)
	}
}

// TestBuildDeps_BD_resolvesRoleTracker is the pg2-2grpj regression: an item
// whose id prefix belongs to a tracker other than cfg.RepoRoot's (a pg2 bead
// dispatched by a handler whose RepoRoot is the ZR monorepo) must be read from
// the role's own BeadsDir, so completion fires once that bead closes. A fake
// `bd` on PATH answers only when run from the pg2 dir, like the real tracker
// resolution (BEADS_DIR is scrubbed, cwd decides).
func TestBuildDeps_BD_resolvesRoleTracker(t *testing.T) {
	pg2, zr, bin := t.TempDir(), t.TempDir(), t.TempDir()
	script := "#!/bin/sh\nif [ \"$(pwd -P)\" = \"" + mustEval(t, pg2) + "\" ]; then\n" +
		"  echo '{\"data\":[{\"id\":\"pg2-x\",\"status\":\"closed\",\"labels\":[\"escalated\"]}]}'\n" +
		"else echo 'no issues found matching the provided IDs' >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config.Default()
	cfg.RepoRoot = zr
	role := roles.Role{Name: "pg2-escalation-triager", CCPool: &roles.CCPoolConfig{Completion: roles.CloseOrTriage, BeadsDir: pg2}}

	// Without BeadsDir the ZR tracker cannot see the bead (the bug).
	plain := buildDeps(cfg, roles.Role{Name: "x", CCPool: &roles.CCPoolConfig{}})
	if iss, err := beads.ShowObj(context.Background(), plain.BD, "pg2-x"); err == nil {
		t.Fatalf("control: ZR-rooted runner unexpectedly saw the pg2 bead: %+v", iss)
	}

	deps := buildDeps(cfg, role)
	iss, err := beads.ShowObj(context.Background(), deps.BD, "pg2-x")
	if err != nil {
		t.Fatalf("role-tracker read failed: %v", err)
	}
	var tr complete.Tracker
	obs := complete.Observation{Status: iss.Status, Labels: iss.Labels, Comments: iss.CommentCount}
	if !tr.Done(role.CCPool.Completion, obs, true) {
		t.Error("DoneSignal must fire once the pg2 bead closes")
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
