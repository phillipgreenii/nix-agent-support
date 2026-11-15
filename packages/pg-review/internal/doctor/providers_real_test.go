package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/appcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/ccaudit"
)

// providerEnv makes the re-exec'd test binary behave as a Go flag-package
// provider instead of running tests (see TestMain).
const providerEnv = "PG_REVIEW_DOCTOR_TEST_AS_PROVIDER"

// TestMain lets the test binary double as a real provider process: it runs the
// built-in ccaudit provider's real command line (ccaudit.Main), which exits 2
// on --help like every Go flag-package provider. This replaces a per-test go build of the real provider, which
// measured compile time and was cache-sensitive.
func TestMain(m *testing.M) {
	if os.Getenv(providerEnv) != "" {
		os.Exit(ccaudit.Main(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// buildProvider returns a dir holding an executable named name that re-execs
// this test binary as a flag-package provider process.
func buildProvider(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nexec env " + providerEnv + "=1 '" + self + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
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
	if r.Status != Fail || r.Code != appcode.Provider || !strings.Contains(r.Reason, "not found") {
		t.Fatalf("got %+v, want not-found failure", r)
	}
}
