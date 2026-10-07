package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func TestTypeGroupsExistAndAreCached(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		g := typeGroup(typ)
		if g != typeGroup(typ) {
			t.Errorf("typeGroup(%q) is not cached", typ)
		}
		found, _, err := rootCmd.Find([]string{typ})
		if err != nil || found != g {
			t.Errorf("rootCmd.Find(%q) = %v, %v; want the typeGroup command", typ, found, err)
		}
		if g.Parent() != rootCmd {
			t.Errorf("group %q is not attached to rootCmd", typ)
		}
	}
}

// TestTypeGroupRegistrationIsOrderIndependent proves a verb can register
// under a group whether or not the group already exists.
func TestTypeGroupRegistrationIsOrderIndependent(t *testing.T) {
	saved := typeGroups["probe"]
	t.Cleanup(func() {
		if g, ok := typeGroups["probe"]; ok {
			rootCmd.RemoveCommand(g)
		}
		delete(typeGroups, "probe")
		if saved != nil {
			typeGroups["probe"] = saved
		}
	})
	delete(typeGroups, "probe")
	verb := &cobra.Command{Use: "probe-verb", Run: func(*cobra.Command, []string) {}}
	typeGroup("probe").AddCommand(verb)
	if c, _, err := rootCmd.Find([]string{"probe", "probe-verb"}); err != nil || c != verb {
		t.Fatalf("verb registered before the group was used is unreachable: %v, %v", c, err)
	}
}

func TestExitCodeFor(t *testing.T) {
	base := errors.New("boom")
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"plain error", base, 1},
		{"partial", newExitError(exitPartial, base), 2},
		{"total", newExitError(exitTotal, base), 3},
		{"wrapped partial", fmt.Errorf("changes: %w", newExitError(exitPartial, base)), 2},
	} {
		if got := exitCodeFor(tc.err); got != tc.want {
			t.Errorf("%s: exitCodeFor = %d, want %d", tc.name, got, tc.want)
		}
	}
	e := newExitError(exitTotal, base)
	if !errors.Is(e, base) {
		t.Errorf("exit error does not unwrap to its cause")
	}
	if e.Error() != "boom" {
		t.Errorf("exit error text = %q, want the wrapped text", e.Error())
	}
}

// TestMainExitsWithTypedVerbCode runs the real main() in a child process
// with a probe verb that returns each kind of error, and checks the child's
// exit status and that the error text still reaches stderr.
func TestMainExitsWithTypedVerbCode(t *testing.T) {
	if mode := os.Getenv("PG_DESK_TEST_EXIT_PROBE"); mode != "" {
		rootCmd.AddCommand(&cobra.Command{
			Use:           "exit-probe",
			SilenceUsage:  true,
			SilenceErrors: true,
			RunE: func(*cobra.Command, []string) error {
				switch mode {
				case "partial":
					return newExitError(exitPartial, errors.New("probe partial"))
				case "total":
					return newExitError(exitTotal, errors.New("probe total"))
				}
				return errors.New("probe plain")
			},
		})
		os.Args = []string{"pg-desk", "exit-probe"}
		main()
		os.Exit(0)
	}
	for mode, want := range map[string]int{"partial": 2, "total": 3, "plain": 1} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainExitsWithTypedVerbCode$")
		cmd.Env = append(os.Environ(), "PG_DESK_TEST_EXIT_PROBE="+mode)
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("%s: want a non-zero exit, got err=%v\n%s", mode, err, out)
		}
		if ee.ExitCode() != want {
			t.Errorf("%s: exit code = %d, want %d\n%s", mode, ee.ExitCode(), want, out)
		}
		if !strings.Contains(string(out), "probe "+mode) {
			t.Errorf("%s: error text not printed to stderr:\n%s", mode, out)
		}
	}
}

func TestRequireNewSchemaForTypedVerbRefusesOldSchema(t *testing.T) {
	old, err := store.Open(storeAtVersion(t, "old"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	err = requireNewSchemaForTypedVerb(old)
	if !errors.Is(err, store.ErrOldSchema) {
		t.Fatalf("err = %v, want it to wrap store.ErrOldSchema", err)
	}
	if !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
		t.Errorf("refusal text %q does not say to run pg-desk migrate --cutover", err)
	}

	cut, err := store.Open(storeAtVersion(t, "new"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cut.Close() }()
	if err := requireNewSchemaForTypedVerb(cut); err != nil {
		t.Errorf("new-schema store refused: %v", err)
	}
}

func TestResolveTypedRef(t *testing.T) {
	cfg := openTestConfig("acme/api")
	for _, tc := range []struct {
		typ, ref, wantID string
	}{
		{"pr", "123", "acme/api#123"},
		{"pr", "acme/api#7", "acme/api#7"},
		{"issue", "PROJ-9", "PROJ-9"},
		{"thread", "C123/1700000000.000100", "C123/1700000000.000100"},
	} {
		repo, id, err := resolveTypedRef(cfg, tc.typ, tc.ref)
		if err != nil || repo != "acme/api" || id != tc.wantID {
			t.Errorf("resolveTypedRef(%s, %q) = %q, %q, %v; want acme/api, %q", tc.typ, tc.ref, repo, id, err, tc.wantID)
		}
	}
	if _, _, err := resolveTypedRef(cfg, "pr", "not-a-pr"); err == nil {
		t.Errorf("unparseable pr ref accepted")
	}
	// pg2-5eus1: a pr ref naming a non-configured repository is rejected,
	// not silently resolved against the configured one.
	if _, _, err := resolveTypedRef(cfg, "pr", "other/repo#7"); err == nil {
		t.Errorf("pr ref naming a non-configured repository accepted")
	}
	for _, typ := range []string{"pr", "issue", "thread"} {
		if _, _, err := resolveTypedRef(openTestConfig("acme/api"), typ, "1"); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
		cfg0 := openTestConfig("x")
		cfg0.Repos = nil
		if _, _, err := resolveTypedRef(cfg0, typ, "1"); err == nil {
			t.Errorf("%s: no configured repository must error", typ)
		}
	}
}

func TestInstallFakePGConnectorPutsScriptOnPath(t *testing.T) {
	installFakePGConnector(t, `echo "fake: $*"`)
	path, err := exec.LookPath("pg-connector")
	if err != nil {
		t.Fatalf("pg-connector not on PATH: %v", err)
	}
	args := []string{"pr", "show"}
	out, err := exec.Command(path, args...).Output()
	if err != nil {
		t.Fatalf("run fake: %v", err)
	}
	if strings.TrimSpace(string(out)) != "fake: pr show" {
		t.Errorf("fake output = %q", out)
	}
}
