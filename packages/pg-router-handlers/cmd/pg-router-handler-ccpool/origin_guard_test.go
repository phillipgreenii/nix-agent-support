package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/originguard"
	"github.com/phillipgreenii/pg-router-handlers/internal/originprobe"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router/conformance"
)

func ccpRole(isolation string) roles.Role {
	return roles.Role{Name: "worker", CCPool: &roles.CCPoolConfig{Isolation: roles.IsolationConfig{Type: isolation}}}
}

func guardGate(t *testing.T, check originguard.Check) (string, bool, *[]string) {
	t.Helper()
	var asked []string
	wrapped := func(ctx context.Context, repo string) (originguard.Verdict, error) {
		asked = append(asked, repo)
		return check(ctx, repo)
	}
	cfg := config.Default()
	cfg.RepoRoot = "/repo"
	p := originprobe.New(config.OriginProbe{StateDir: t.TempDir(), FailureThreshold: 1})
	reason, declined := originGate(context.Background(), p, wrapped, ccpRole("worktree"), cfg, "b-1")
	return reason, declined, &asked
}

func TestOriginGate_guardAvailableDispatches(t *testing.T) {
	reason, declined, asked := guardGate(t, func(context.Context, string) (originguard.Verdict, error) {
		return originguard.Verdict{Available: true}, nil
	})
	if declined || reason != "" {
		t.Fatalf("declined = %v %q, want dispatch", declined, reason)
	}
	if len(*asked) != 1 || (*asked)[0] != "/repo" {
		t.Fatalf("guard asked %v, want [/repo]", *asked)
	}
}

func TestOriginGate_guardUnavailableDeclinesOriginUnavailable(t *testing.T) {
	reason, declined, _ := guardGate(t, func(context.Context, string) (originguard.Verdict, error) {
		return originguard.Verdict{Available: false, Reason: "cert-expired"}, nil
	})
	if !declined || reason != "origin-unavailable" {
		t.Fatalf("got %v %q, want a decline with reason origin-unavailable", declined, reason)
	}
}

func TestOriginGate_guardFaultFailsOpen(t *testing.T) {
	reason, declined, _ := guardGate(t, func(context.Context, string) (originguard.Verdict, error) {
		return originguard.Verdict{}, errors.New("exit status 1")
	})
	if declined || reason != "" {
		t.Fatalf("a guard fault must fail open; got %v %q", declined, reason)
	}
}

func TestOriginGate_noRepoRootIsNotChecked(t *testing.T) {
	cfg := config.Default()
	p := originprobe.New(config.OriginProbe{StateDir: t.TempDir(), FailureThreshold: 1})
	called := false
	_, declined := originGate(context.Background(), p, func(context.Context, string) (originguard.Verdict, error) {
		called = true
		return originguard.Verdict{}, nil
	}, ccpRole("workforest"), cfg, "b-1")
	if declined || called {
		t.Fatalf("a role without a repo root must not consult the guard (declined=%v called=%v)", declined, called)
	}
}

// Through the whole dispatch: a guard that reports unavailable declines with
// exit 9 and makes no bd/ccpool call.
func TestDispatch_guardUnavailableDeclinesWithoutBeadsCalls(t *testing.T) {
	cfgPath, _, callLog := originFixture(t, t.TempDir())
	// no watched origin matches: only the guard can decline.
	old := newGuardCheck
	newGuardCheck = func(config.Config) originguard.Check {
		return func(context.Context, string) (originguard.Verdict, error) {
			return originguard.Verdict{Reason: "no-human"}, nil
		}
	}
	t.Cleanup(func() { newGuardCheck = old })
	r := workerWithIsolation(t, `{}`)
	code, out := dispatchWith(t, r.name, r.cfg, cfgPath, defaultEvent)
	if code != conformance.ExitBusy || decodeReply(t, out)["reason"] != "origin-unavailable" {
		t.Fatalf("exit=%d out=%q, want a busy origin-unavailable decline", code, out)
	}
	if b, err := os.ReadFile(callLog); err == nil {
		t.Fatalf("a decline must make no bd/ccpool call, saw:\n%s", strings.TrimSpace(string(b)))
	}
}
