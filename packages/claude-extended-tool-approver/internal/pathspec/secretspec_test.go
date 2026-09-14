package pathspec

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestResolveAccessWithSecrets_WellKnownSecret: a secretpath.WellKnownSecret
// match resolves Read/Write/Delete all Forbidden, reason "well-known
// credential store", REGARDLESS of what the ordinary Kind fold would say —
// exercised with an EXPLICIT, non-default kind set (per tc-mkpaz.4's
// Contract: "unit-test through it, exactly as P2/P3 do") that would
// otherwise permit the path, to prove ResolveAccess is never consulted for
// this arm at all.
func TestResolveAccessWithSecrets_WellKnownSecret(t *testing.T) {
	permitEverything := []Kind{{
		Name: "permit-all",
		Classify: func(root, rel, abs string, isDir bool) (read, write, del AccessResult) {
			return Permitted, Permitted, Permitted
		},
		Markers: nil,
	}}

	cases := []struct {
		name string
		abs  string
	}{
		{"dotssh under home", "/home/user/.ssh/id_rsa"},
		{"dotgnupg under an arbitrary project dir", "/home/user/project/foo/.gnupg/keyring"},
		{"pem suffix anywhere", "/home/user/project/testdata/cert.pem"},
		{"key suffix anywhere", "/home/user/project/node_modules/pkg/id.key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveAccessWithSecrets(permitEverything, c.abs)
			want := forbidAllFacets(wellKnownSecretReason)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ResolveAccessWithSecrets(%q) = %+v, want %+v", c.abs, got, want)
			}
		})
	}
}

// TestResolveAccessWithSecrets_WellKnownSecret_EmptyKinds: the same
// forbidding happens even with NO kinds at all (an empty []Kind), since this
// arm never calls ResolveAccess — demonstrating the mechanism is a
// resolver-level pre-step, not a Kind participating in the ordinary fold.
func TestResolveAccessWithSecrets_WellKnownSecret_EmptyKinds(t *testing.T) {
	got := ResolveAccessWithSecrets(nil, "/x/.ssh/id_rsa")
	want := forbidAllFacets(wellKnownSecretReason)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveAccessWithSecrets(nil, ...) = %+v, want %+v", got, want)
	}
}

// TestResolveAccessWithSecrets_GenericSecretsDir_DefaultForbidden: a bare
// "secrets" path component with NO project declaration vouching for it
// resolves Forbidden on all three facets — the low-priority default — in a
// plain directory with no git workspace at all (so NonSecretWith has no
// candidate to consult in the first place).
func TestResolveAccessWithSecrets_GenericSecretsDir_DefaultForbidden(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "secrets", "prod.yaml")
	if err := os.WriteFile(abs, []byte("k: v\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ResolveAccessWithSecrets(DefaultKinds(), abs)
	want := forbidAllFacets(genericSecretsDirReason)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveAccessWithSecrets(%q) = %+v, want %+v", abs, got, want)
	}
	if got.Read.Result != Forbidden {
		t.Errorf("Read = %v, want Forbidden (blocks by default)", got.Read.Result)
	}
}

// TestResolveAccessWithSecrets_GenericSecretsDir_VouchedByGit: a bare
// "secrets" path component that IS vouched for by gitKind.Secrecy (tracked
// by git, not gitignored) does NOT resolve Forbidden for the
// generic-secrets-default reason — this mechanism steps aside entirely and
// the ordinary ResolveAccess result is returned unchanged.
func TestResolveAccessWithSecrets_GenericSecretsDir_VouchedByGit(t *testing.T) {
	root := gitTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, "secrets", "committed.yaml")
	if err := os.WriteFile(abs, []byte("k: v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "secrets/committed.yaml")
	git(t, root, "commit", "-q", "-m", "add tracked secrets file")

	got := ResolveAccessWithSecrets(DefaultKinds(), abs)
	if got.Read.Result == Forbidden && got.Read.Reason == genericSecretsDirReason {
		t.Errorf("vouched path still hit the generic-secrets default: %+v", got)
	}
	want := ResolveAccess(DefaultKinds(), abs)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vouched path: ResolveAccessWithSecrets(%q) = %+v, want it to fall through to ResolveAccess unchanged: %+v", abs, got, want)
	}
}

// TestResolveAccessWithSecrets_GenericSecretsDir_UntrackedNotWavedThrough is
// the exact counterexample ADR 0068 checked and rejected (tc-mkpaz.4's
// packet text, acceptance criterion 3): an UNTRACKED file inside a
// secrets/-named directory, in an otherwise ordinary git project, must NOT
// be waved through by the git Kind's broad "this is project content"
// opinion (gitKind's holdKeep, which exports as Delete: Unknown/"needs
// consent" — not a veto, and not the Forbidden this path must still get).
// This proves the generic-secrets default does NOT fold into ordinary
// depth-precedence: if it did, git's non-Unknown (holdKeep) opinion — being
// the innermost candidate — would win the fold and this path would resolve
// merely Unknown instead of Forbidden.
func TestResolveAccessWithSecrets_GenericSecretsDir_UntrackedNotWavedThrough(t *testing.T) {
	root := gitTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, "secrets", "token")
	if err := os.WriteFile(abs, []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT `git add`ed: untracked, non-ignored.

	// Sanity check the premise: the ordinary Kind fold (git's holdKeep,
	// exported as Unknown) would NOT be Forbidden on its own — so if this
	// path ends up Forbidden below, it is this packet's default doing it,
	// not an accidental git-kind opinion.
	ordinary := ResolveAccess(DefaultKinds(), abs)
	if ordinary.Delete.Result == Forbidden {
		t.Fatalf("test premise violated: ordinary ResolveAccess already Forbidden (%+v) — counterexample would not distinguish anything", ordinary)
	}

	got := ResolveAccessWithSecrets(DefaultKinds(), abs)
	want := forbidAllFacets(genericSecretsDirReason)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("untracked secrets/-component file: ResolveAccessWithSecrets(%q) = %+v, want %+v (must not be waved through)", abs, got, want)
	}
}

// TestResolveAccessWithSecrets_NotSecret_Passthrough: a path matching
// neither secretpath arm is unaffected — ResolveAccessWithSecrets is a
// strict superset of ResolveAccess for every non-secret path.
func TestResolveAccessWithSecrets_NotSecret_Passthrough(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "ordinary.txt")
	if err := os.WriteFile(abs, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ResolveAccessWithSecrets(DefaultKinds(), abs)
	want := ResolveAccess(DefaultKinds(), abs)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("non-secret path: ResolveAccessWithSecrets(%q) = %+v, want ResolveAccess unchanged: %+v", abs, got, want)
	}
}
