package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
)

// buildProvider compiles a real built-in provider into dir and returns dir.
func buildProvider(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", filepath.Join(dir, name), "../../cmd/"+name)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return dir
}

func realProviderDeps(dir, name string) *Deps {
	return &Deps{
		Config: &config.Effective{Providers: []config.Provider{{Name: "p", Command: []string{name}}}},
		LookPath: func(n string) (string, error) {
			p := filepath.Join(dir, n)
			if _, err := os.Stat(p); err != nil {
				return "", err
			}
			return p, nil
		},
		Exec: OSExec,
	}
}

// A real Go flag-package provider exits 2 on --help; the providers check must
// still pass it.
func TestRealFlagProviderPassesProvidersCheck(t *testing.T) {
	const name = "pg-review-provider-ccaudit"
	dir := buildProvider(t, name)
	_, code, err := OSExec(context.Background(), filepath.Join(dir, name), []string{"--help"}, "", nil, "")
	if err != nil || code != flagUsageExit {
		t.Fatalf("premise: provider --help = exit %d err %v, want exit %d", code, err, flagUsageExit)
	}
	r := checkProviders(context.Background(), realProviderDeps(dir, name))
	if r.Status != Pass {
		t.Fatalf("providers check = %v %q, want pass", r.Status, r.Reason)
	}
}

func TestRealProviderCheckStillFailsOnMissingCommand(t *testing.T) {
	r := checkProviders(context.Background(), realProviderDeps(t.TempDir(), "pg-review-provider-ccaudit"))
	if r.Status != Fail || r.Code != exitcode.Provider || !strings.Contains(r.Reason, "not found") {
		t.Fatalf("got %+v, want not-found failure", r)
	}
}
