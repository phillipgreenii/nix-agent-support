package groupsvc_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	sharedsvc "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
)

// TestTheRetentionTableBooksTheSameSetAsBeforeTheMove is HT-38's GitHub half: the
// pre-move keep-alive rule (tombstoned out, member of a configured query in,
// otherwise summary.Service.InKeepAlive) runs behind the shared service's
// KeepAlive interface and books exactly the same set.
func TestTheRetentionTableBooksTheSameSetAsBeforeTheMove(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	register(r, groups.Detail)
	window := r.Settings.KeepaliveWindow.Std()
	now := r.Clock.Now()
	const (
		tombstoned = "PR_tombstoned"
		member     = "PR_member"
		inside     = "PR_inside"
		outside    = "PR_outside"
	)
	// The tombstoned entity was accessed inside the window and the member outside
	// it, so only the two facts (not the window) can decide them.
	r.SeedSummary(tombstoned, now)
	r.SeedSummary(member, now.Add(-window-time.Hour))
	r.SeedSummary(inside, now.Add(-window/2))
	r.SeedSummary(outside, now.Add(-window-time.Hour))
	e, _ := r.Entity(tombstoned)
	e.TombstonedAt = now
	if err := r.Store.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	r.PutQuery("mine", member)

	if _, err := r.Svc.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{tombstoned: false, member: true, inside: true, outside: false} {
		if _, queued, _, err := r.Svc.Inspect(ctx, id, groups.Detail); err != nil || queued != want {
			t.Errorf("%s: detail queued = %v (err %v), want %v", id, queued, err, want)
		}
	}
	if c, _, _, _ := r.Svc.Inspect(ctx, member, groups.Detail); c != governor.ClassQuery {
		t.Errorf("member task class = %v, want 3", c)
	}
	if c, _, _, _ := r.Svc.Inspect(ctx, inside, groups.Detail); c != governor.ClassKeepAlive {
		t.Errorf("kept-alive task class = %v, want 4", c)
	}
}

func TestTheFacadeKeepsTheGitHubSentinelsThroughTheSharedService(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	register(r, groups.Detail)

	if _, err := r.Svc.Ensure(ctx, "acme-org/widget#41", []string{groups.Detail}, groupsvc.EnsureOptions{}); !errors.Is(err, groupsvc.ErrNoSuchPR) {
		t.Errorf("Ensure of a pull request the origin does not know = %v, want ErrNoSuchPR", err)
	}
	for _, id := range []string{"acme-org/widget#0", "not a node id", ""} {
		if _, err := r.Svc.Ensure(ctx, id, []string{groups.Detail}, groupsvc.EnsureOptions{}); !errors.Is(err, groupsvc.ErrBadRef) {
			t.Errorf("Ensure(%q) = %v, want ErrBadRef", id, err)
		}
	}
	if err := r.Svc.QueueGroup(ctx, "acme-org/widget#41", groups.Detail, governor.ClassQuery, time.Time{}); !errors.Is(err, summary.ErrUnknownEntity) {
		t.Errorf("QueueGroup of an unknown reference = %v, want summary.ErrUnknownEntity", err)
	}
	if !errors.Is(groupsvc.ErrNoFetcher, sharedsvc.ErrNoFetcher) {
		t.Error("the facade's ErrNoFetcher must be the shared sentinel")
	}
	if got := groupsvc.ErrNoFetcher.Error(); got != "groupsvc: no fetcher registered for the group" {
		t.Errorf("ErrNoFetcher text = %q", got)
	}
	if got := groupsvc.ErrNoSuchPR.Error(); got != "groupsvc: no such pull request" {
		t.Errorf("ErrNoSuchPR text = %q", got)
	}
	if got := groupsvc.ErrBadRef.Error(); got != "groupsvc: malformed pull request reference" {
		t.Errorf("ErrBadRef text = %q", got)
	}
}

func TestAGroupFetcherThatReturnsErrGoneTombstonesTheEntityThroughTheFacade(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	r.Start()
	f := register(r, groups.Detail)
	seed(r, node)
	f[groups.Detail].SetGone(node, true)
	if _, err := r.Svc.Ensure(ctx, node, []string{groups.Detail}, groupsvc.EnsureOptions{}); !errors.Is(err, groupsvc.ErrNoSuchPR) {
		t.Fatalf("Ensure = %v, want ErrNoSuchPR", err)
	}
	if e, _ := r.Entity(node); e.TombstonedAt.IsZero() {
		t.Error("the entity was not tombstoned")
	}
}

func TestNewAppliesTheDefaultsTheGitHubEngineAlwaysApplied(t *testing.T) {
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	// Zero Expiry, FallbackPause and Clock: New fills the defaults and succeeds
	// where the shared service would refuse a non-positive Expiry.
	svc, err := groupsvc.New(groupsvc.Config{Store: r.Store, Scheduler: r.Sched, Summary: r.Summary, Rules: r.Rules, Aliases: r.Aliases})
	if err != nil || svc == nil {
		t.Fatalf("New with only the required members = (%v, %v)", svc, err)
	}
	for name, cfg := range map[string]groupsvc.Config{
		"no store":     {Scheduler: r.Sched, Summary: r.Summary, Rules: r.Rules, Aliases: r.Aliases},
		"no scheduler": {Store: r.Store, Summary: r.Summary, Rules: r.Rules, Aliases: r.Aliases},
		"no summary":   {Store: r.Store, Scheduler: r.Sched, Rules: r.Rules, Aliases: r.Aliases},
		"no rules":     {Store: r.Store, Scheduler: r.Sched, Summary: r.Summary, Aliases: r.Aliases},
		"no aliases":   {Store: r.Store, Scheduler: r.Sched, Summary: r.Summary, Rules: r.Rules},
	} {
		if _, err := groupsvc.New(cfg); err == nil {
			t.Errorf("New accepted a config with %s", name)
		}
	}
}

// TestTheFacadeImportsNeitherTheChangesNorTheQueryPackage pins the facade rule
// (the unedited changes tests import this package while query imports changes):
// go list -deps of the facade lists neither.
func TestTheFacadeImportsNeitherTheChangesNorTheQueryPackage(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	out, err := exec.Command(goBin, "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/changes") || strings.HasSuffix(dep, "/internal/query") {
			t.Errorf("the facade depends on %s", dep)
		}
	}
}
