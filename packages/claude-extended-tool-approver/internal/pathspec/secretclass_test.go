package pathspec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// writeUserSecretConfig sets the user-level secretPaths/notSecretPaths in the
// config gitTestRepo already created under the fixture HOME, MERGING so its
// trustedExecPath (needed by the hardened git probe) survives.
func writeUserSecretConfig(t *testing.T, home string, secret, notSecret []string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "claude-extended-tool-approver-engine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	m := map[string]any{}
	if old, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(old, &m); err != nil {
			t.Fatal(err)
		}
	}
	m["secretPaths"] = secret
	m["notSecretPaths"] = notSecret
	data, _ := json.Marshal(m)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRepoSecretDecl(t *testing.T, root string, secret, notSecret []string) {
	t.Helper()
	dir := filepath.Join(root, ".ceta")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(SecretDeclarations{Secret: secret, NotSecret: notSecret})
	if err := os.WriteFile(filepath.Join(dir, repoSecretDeclFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSecretFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSecretClassification_Defaults: tracked/untracked/outside-repo
// credential-format files, the "secret"-word non-indicator, and the named
// stores the packet lists, with no declarations anywhere.
func TestSecretClassification_Defaults(t *testing.T) {
	root := gitTestRepo(t)
	for _, f := range []string{".envrc", "tracked.pem", "my-secret-notes.txt", "secret/x.txt"} {
		writeSecretFixture(t, filepath.Join(root, f))
	}
	writeSecretFixture(t, filepath.Join(root, "untracked.envrc.d", ".envrc"))
	writeSecretFixture(t, filepath.Join(root, ".gitignore"))
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.pem\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSecretFixture(t, filepath.Join(root, "ignored.pem"))
	git(t, root, "add", ".envrc", "tracked.pem", "my-secret-notes.txt", "secret/x.txt", ".gitignore")
	git(t, root, "add", "-f", "ignored.pem")
	git(t, root, "commit", "-q", "-m", "tracked credential-format files")

	kinds := DefaultKinds()
	cases := []struct {
		name   string
		abs    string
		secret bool
	}{
		{"tracked .envrc is not secret (ruling #7)", filepath.Join(root, ".envrc"), false},
		{"tracked .pem is not secret", filepath.Join(root, "tracked.pem"), false},
		{"tracked-but-gitignored .pem stays secret", filepath.Join(root, "ignored.pem"), true},
		{"untracked .envrc is secret (ruling #6)", filepath.Join(root, "untracked.envrc.d", ".envrc"), true},
		{"word secret in basename is not an indicator", filepath.Join(root, "my-secret-notes.txt"), false},
		{"word secret as a directory is not an indicator", filepath.Join(root, "secret", "x.txt"), false},
		{"credential-format outside any repo is secret", "/home/u/proj/.envrc", true},
		{"~/.ssh/id_rsa", "/home/u/.ssh/id_rsa", true},
		{"~/.config/beads/credentials", "/home/u/.config/beads/credentials", true},
		{"/run/vault-secrets/*", "/run/vault-secrets/db-password", true},
		{"~/.git-credentials", "/home/u/.git-credentials", true},
		{"public key is not secret by name", "/home/u/proj/id_rsa.pub", false},
		{"bare credentials file elsewhere is not secret", "/home/u/proj/credentials", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifySecretPath(kinds, c.abs, NewR2Cache())
			if got.Secret != c.secret {
				t.Errorf("ClassifySecretPath(%q) = %+v, want Secret=%v", c.abs, got, c.secret)
			}
		})
	}
}

// TestSecretClassification_NamedStoreTrackedStaysSecret: a tracked file under
// a named store (.ssh) is still secret; tracking only lifts credential
// FORMATS.
func TestSecretClassification_NamedStoreTrackedStaysSecret(t *testing.T) {
	root := gitTestRepo(t)
	writeSecretFixture(t, filepath.Join(root, "dot", ".ssh", "config"))
	git(t, root, "add", "dot/.ssh/config")
	git(t, root, "commit", "-q", "-m", "tracked ssh dir file")
	if v := ClassifySecretPath(DefaultKinds(), filepath.Join(root, "dot", ".ssh", "config"), NewR2Cache()); !v.Secret {
		t.Errorf("tracked file under .ssh = %+v, want secret", v)
	}
}

// TestSecretClassification_DeclarationPrecedence covers the P17 precedence
// rule, including the exact tie-break the packet requires: a repo-level
// not-secret declaration does NOT override a user-level SECRET declaration
// (secret always wins), while it does override the built-in default and a
// user-level not-secret ties to not-secret.
func TestSecretClassification_DeclarationPrecedence(t *testing.T) {
	root := gitTestRepo(t)
	home := os.Getenv("HOME") // the fixture HOME gitTestRepo installed
	target := filepath.Join(root, "conf", "app.env.local")
	writeSecretFixture(t, target)
	envrc := filepath.Join(root, "untracked", ".envrc")
	writeSecretFixture(t, envrc)
	kinds := DefaultKinds()
	classify := func(abs string) SecretVerdict { return ClassifySecretPath(kinds, abs, NewR2Cache()) }

	// Baseline: ordinary file, no declarations => not secret.
	if v := classify(target); v.Secret {
		t.Fatalf("baseline: %+v", v)
	}

	// Repo-level secret declaration (repo-relative pattern) => secret.
	writeRepoSecretDecl(t, root, []string{"conf/**"}, nil)
	if v := classify(target); !v.Secret {
		t.Errorf("repo-level secret decl ignored: %+v", v)
	}

	// User-level secret + repo-level not-secret for the same path => secret.
	writeRepoSecretDecl(t, root, nil, []string{"conf/**"})
	writeUserSecretConfig(t, home, []string{filepath.Join(root, "conf") + "/**"}, nil)
	if v := classify(target); !v.Secret {
		t.Errorf("repo not-secret overrode user secret (secret must always win): %+v", v)
	}

	// User-level NOT-secret + repo-level not-secret => not secret (tie).
	writeUserSecretConfig(t, home, nil, []string{filepath.Join(root, "conf") + "/**"})
	if v := classify(target); v.Secret {
		t.Errorf("not-secret + not-secret should be not secret: %+v", v)
	}

	// Repo-level not-secret overrides the built-in default for an untracked
	// credential-format file.
	writeUserSecretConfig(t, home, nil, nil)
	if v := classify(envrc); !v.Secret {
		t.Fatalf("premise: untracked .envrc must be default-secret: %+v", v)
	}
	writeRepoSecretDecl(t, root, nil, []string{"untracked/.envrc"})
	if v := classify(envrc); v.Secret {
		t.Errorf("repo not-secret did not override built-in default: %+v", v)
	}

	// User-level not-secret (~/ pattern) overrides the built-in default
	// outside any repo.
	writeRepoSecretDecl(t, root, nil, nil)
	writeSecretFixture(t, filepath.Join(home, "scratch", ".envrc"))
	writeUserSecretConfig(t, home, nil, []string{"~/scratch"})
	if v := classify(filepath.Join(home, "scratch", ".envrc")); v.Secret {
		t.Errorf("user not-secret (directory pattern) did not apply: %+v", v)
	}

	// A not-secret declaration can NOT un-secret a named store.
	writeRepoSecretDecl(t, root, nil, []string{"**"})
	writeUserSecretConfig(t, home, nil, []string{"/home/u/.ssh"})
	if v := classify("/home/u/.ssh/id_rsa"); !v.Secret {
		t.Errorf("not-secret declaration un-secreted a named store: %+v", v)
	}
	// ...nor a repo-declared one inside the repo.
	ssh := filepath.Join(root, ".ssh", "id_ed25519")
	writeSecretFixture(t, ssh)
	if v := classify(ssh); !v.Secret {
		t.Errorf("repo not-secret un-secreted a named store: %+v", v)
	}
}

// TestSecretClassification_TrackedCheckUsesHardenedProbeAndCaches: the
// tracked-file check goes through gitTrackedProbe (the seam realGitTracked,
// K11's hardened plumbing, occupies) and is memoized per evaluation: a second
// lookup of the same path with the same R2Cache does not call it again.
func TestSecretClassification_TrackedCheckUsesHardenedProbeAndCaches(t *testing.T) {
	root := gitTestRepo(t)
	target := filepath.Join(root, ".envrc")
	writeSecretFixture(t, target)

	var calls atomic.Int32
	restore := SetGitTrackedProbe(func(r, rel string, isDir bool) (bool, error) {
		calls.Add(1)
		return true, nil
	})
	defer restore()

	cache := NewR2Cache()
	for i := 0; i < 3; i++ {
		if v := ClassifySecretPath(DefaultKinds(), target, cache); v.Secret {
			t.Fatalf("probe says tracked => not secret, got %+v", v)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("tracked probe called %d times for one path in one evaluation, want 1 (cached)", n)
	}
	// A fresh evaluation (new cache) asks again.
	ClassifySecretPath(DefaultKinds(), target, NewR2Cache())
	if n := calls.Load(); n != 2 {
		t.Errorf("new evaluation: probe calls = %d, want 2", n)
	}
}

// TestSecretClassification_ProbeErrorStaysSecret: when the tracked question
// cannot be asked (hardened probe error) the credential-format file stays
// secret (P3).
func TestSecretClassification_ProbeErrorStaysSecret(t *testing.T) {
	root := gitTestRepo(t)
	target := filepath.Join(root, ".envrc")
	writeSecretFixture(t, target)
	restore := SetGitTrackedProbe(func(r, rel string, isDir bool) (bool, error) {
		return false, &gitTrackedProbeError{root: r, rel: rel, err: os.ErrDeadlineExceeded}
	})
	defer restore()
	if v := ClassifySecretPath(DefaultKinds(), target, NewR2Cache()); !v.Secret {
		t.Errorf("probe error must not read as tracked: %+v", v)
	}
}

// TestSecretClassification_ResolveAccessIntegration: the Forbidden fold and
// the tracked lift are visible through ResolveAccessWithSecrets.
func TestSecretClassification_ResolveAccessIntegration(t *testing.T) {
	root := gitTestRepo(t)
	writeSecretFixture(t, filepath.Join(root, ".envrc"))
	writeSecretFixture(t, filepath.Join(root, "sub", ".envrc"))
	git(t, root, "add", ".envrc")
	git(t, root, "commit", "-q", "-m", "tracked envrc")

	kinds := DefaultKinds()
	if got := ResolveAccessWithSecrets(kinds, filepath.Join(root, "sub", ".envrc")); got.Read.Result != Forbidden {
		t.Errorf("untracked .envrc Read = %+v, want Forbidden", got.Read)
	}
	tracked := filepath.Join(root, ".envrc")
	if got, want := ResolveAccessWithSecrets(kinds, tracked), ResolveAccess(kinds, tracked); got != want {
		t.Errorf("tracked .envrc = %+v, want ordinary ResolveAccess %+v", got, want)
	}
}
