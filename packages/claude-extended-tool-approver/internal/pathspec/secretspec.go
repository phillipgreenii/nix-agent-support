package pathspec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/userconfig"
)

// ADR 0068 P4 (tc-mkpaz.4, docs/adr/0068-ceta-unified-path-access-
// resolution.md, "Secret handling folds in, with one named, deliberately
// non-generalized exception") folds internal/secretpath's lexical secret
// classification into this package's PathAccess model as spec-level
// entries, so a caller resolving a path's access no longer needs a
// separate secret-aware code path layered on top — see
// ResolveAccessWithSecrets.
//
// Two DIFFERENT treatments, matching secretpath.Kind's own two arms:
//
//  1. secretpath.WellKnownSecret (a specific credential store or file —
//     .ssh/.gnupg, the credential basenames, *.pem/*.key) folds in as an
//     ORDINARY spec entry at the SAME priority as an operator's
//     denyRead/denyWrite: Read, Write, and Delete are all Forbidden,
//     unconditionally — no project declaration can relax it. (K8,
//     tc-o14i5.3.8 narrows this: that holds for NAMED STORES; a
//     credential-FORMAT file may be lifted by a tracked-file vouch or a
//     not-secret declaration — see ClassifySecretPath's precedence doc.)
//     "Ordinary
//     spec entry" is aspirational, not literal, though:
//     secretpath.Classify matches by PATH COMPONENT/BASENAME anywhere in a
//     path, which is not a shape the root-identifying Kind candidate
//     mechanism (workspace.go's Markers/Home/Temp/Roots) can express (no
//     single ancestor root "identifies" a .ssh directory the way a marker
//     file identifies a git or go workspace). So this is implemented as a
//     RESOLVER-LEVEL pre-step, mirroring how secretRead/classifiedSecretRead
//     work TODAY in internal/effectpolicy/policy.go (just relocated here,
//     ahead of P5 deleting that duplication), rather than as a new Kind —
//     see this file's package-level freedom-boundary note in tc-mkpaz.4's
//     packet text for the two admissible shapes and why this one was
//     chosen. It still composes correctly with the ordinary Kind fold: a
//     WellKnownSecret match's Forbidden wins outright over any deeper
//     Kind's opinion, exactly like the "Protected always wins" /
//     "Forbidden wins outright" rule ResolveAccess itself already applies
//     (workspace.go's updateFacet) — implemented here by never even
//     consulting ResolveAccess for a WellKnownSecret path.
//
//  2. secretpath.GenericSecretsDir (the bare, role-describing "secrets"
//     path component) does NOT fold into ordinary depth-precedence. It
//     stays a distinct, NAMED, low-priority DEFAULT — Forbidden unless
//     vouched otherwise — overridable ONLY by a workspace's
//     tracked-and-not-ignored fact (gitKind's existing Secrecy facet,
//     consulted via NonSecretWith, unchanged from before this packet).
//     Two alternatives were considered and rejected during design (ADR
//     0068's own "Secret handling folds in..." paragraph):
//     - "Depth alone points the wrong way": the vouching git Kind is
//     usually SHALLOWER than the secrets/ directory it vouches for, so
//     ordinary innermost-wins precedence would let the "secrets"
//     component guess beat the shallower vouch.
//     - "Widening to 'any deeper spec's ordinary opinion overrides the
//     guess' would silently loosen protection": an UNTRACKED file
//     inside an otherwise ordinary git project's secrets/ directory
//     would be waved through by the git Kind's broad "this is project
//     content" opinion (gitKind's holdKeep — "needs consent", not a
//     veto) if the generic-secrets default merely participated in the
//     same fold as every other Kind.
//     So GenericSecretsDir handling here NEVER consults ResolveAccess's
//     ordinary Kind-fold result at all when unvouched: it is Forbidden by
//     itself, independent of what any Kind (including git) would otherwise
//     say about the same path. Only NonSecretWith's own single-vouch,
//     first-match-wins walk (already innermost-first, unchanged from
//     before this packet) can lift the default — and when it does, this
//     mechanism steps aside entirely (falls through to ordinary
//     ResolveAccess) rather than asserting Permitted itself.
const (
	wellKnownSecretReason   = "well-known credential store"
	genericSecretsDirReason = `bare "secrets" path component; no project declaration vouches for it as non-secret`
)

// forbidAllFacets returns a PathAccess whose Read, Write, and Delete are all
// Forbidden with the same reason — the shape both secretpath.Kind arms
// collapse to when this packet's spec-level handling applies unconditionally
// (WellKnownSecret always; GenericSecretsDir when unvouched).
func forbidAllFacets(reason string) PathAccess {
	v := Verdict{Result: Forbidden, Reason: reason}
	return PathAccess{Read: v, Write: v, Delete: v}
}

// ResolveAccessWithSecrets is ResolveAccess (workspace.go) with the secret
// classification (ClassifySecretPath, below) folded in ahead of the ordinary
// Kind fold, per this file's package-level doc comment. It takes the same
// []Kind, abs shape ResolveAccess/ClassifyWith/NonSecretWithKinds already
// do, so P5's policy-layer collapse can call this in place of ResolveAccess
// uniformly. A path ClassifySecretPath calls secret resolves Read/Write/
// Delete all Forbidden WITHOUT consulting ResolveAccess (so no Kind's
// opinion, including a Permitted one, can relax it); every other path
// returns ResolveAccess(kinds, abs) unchanged — a strict superset of
// ResolveAccess for non-secret paths.
//
// It uses no per-evaluation cache; a caller holding one calls
// ResolveAccessWithSecretsCached.
func ResolveAccessWithSecrets(kinds []Kind, abs string) PathAccess {
	return ResolveAccessWithSecretsCached(kinds, abs, nil)
}

// ResolveAccessWithSecretsCached is ResolveAccessWithSecrets with K7's
// per-evaluation R2Cache threaded through to the tracked-file check. A nil
// cache behaves exactly like ResolveAccessWithSecrets.
func ResolveAccessWithSecretsCached(kinds []Kind, abs string, cache *R2Cache) PathAccess {
	if v := ClassifySecretPath(kinds, abs, cache); v.Secret {
		return forbidAllFacets(v.Reason)
	}
	return ResolveAccess(kinds, abs)
}

// P17 SECRET/NOT-SECRET DECLARATIONS AND PRECEDENCE (tc-o14i5.3.8, K8;
// design "Secret classification", rulings #6/#7, item h part 2).
//
// SOURCES (the "schema a human can extend"), each a pair of pattern lists:
//   - built-in: secretpath.Classify's lexical match arms (WellKnownSecret,
//     GenericSecretsDir) — the canonical built-in credential enumeration;
//   - user-level: userconfig.Config.SecretPaths / NotSecretPaths;
//   - repo-level: `<git root>/.ceta/secret-paths.json`
//     ({"secret": [...], "notSecret": [...]}), the repo's P17 layer
//     (specfmt.DefaultRepoDir). Patterns are absolute, `~/`-prefixed, or
//     (repo layer only) repo-root-relative, gitignore-style glob syntax
//     (globMatch), and also cover everything beneath a matching directory.
//
// PRECEDENCE: repo-level > user-level > built-in default, EXCEPT that a
// `secret` declaration from ANY source wins over `not-secret` for the same
// path (so a repo's not-secret never overrides a user-level secret). A
// not-secret declaration (or tracked-file vouch) overrides the built-in
// default ONLY for credential-FORMAT files (secretpath.IsCredentialFormat:
// .envrc, .env*, *.pem/*.key, .netrc, .pgpass, .npmrc) and for the
// bare `secrets` directory component; NAMED STORES (.ssh/.gnupg/
// vault-secrets directories, .git-credentials, ~/.config/beads/credentials,
// the Claude credential files, ...) are secret by built-in default and
// nothing but removing the match can lift that — a hostile repo's
// `.ceta/secret-paths.json` must not be able to un-secret ~/.ssh.
//
// TRACKED FILES (ruling #7): a file tracked by git and not gitignored is
// non-secret by default, including a tracked credential-format file.
// Untracked, gitignored, or outside-any-repo credential-format files stay
// default-secret (ruling #6). Tracked status is gitKind.Secrecy via
// NonSecretWith, which calls gitTrackedProbe — realGitTracked, K11's
// hardened plumbing (trustedGitPath, hermeticGitEnviron, timeout, process
// group) — never a second git invocation; the result is memoized per
// evaluation in R2Cache (K7's cache context). A probe error is NO OPINION,
// i.e. stays secret (P3: unknown never approves).
//
// A word like "secret" in a path is NEVER an indicator: only the exact
// `secrets` COMPONENT (GenericSecretsDir, folded in by ADR 0068 P4 above)
// and the named/format arms match; "my-secret.txt", "secret/x" do not.

// SecretDeclarations is one layer's secret/not-secret pattern lists.
type SecretDeclarations struct {
	Secret    []string `json:"secret"`
	NotSecret []string `json:"notSecret"`
}

// repoSecretDeclFile is the repo-level declaration file name under `.ceta/`.
const repoSecretDeclFile = "secret-paths.json"

// SecretVerdict is ClassifySecretPath's answer. Reason names the deciding
// source for both outcomes.
type SecretVerdict struct {
	Secret bool
	Reason string
}

type vouchEntry struct {
	ok     bool
	reason string
}

// ClassifySecretPath is the secret-path classification function consumed by
// the path-read judgment sites (K6 data-read-paths, PathAccessPolicy): it
// reports whether abs (an already RESOLVED absolute path — K7's
// ResolveComponentwise output) is secret, per the precedence documented
// above. cache may be nil (no memoization).
func ClassifySecretPath(kinds []Kind, abs string, cache *R2Cache) SecretVerdict {
	user := cache.userDecls()
	var repo SecretDeclarations
	repoRoot := ""
	if root, ok := patheval.GitRoot(abs); ok {
		repoRoot = root
		repo = cache.repoDecls(root)
	}

	// 1. A secret declaration from any source wins over everything.
	if matchSecretPatterns(repo.Secret, repoRoot, abs) {
		return SecretVerdict{true, "declared secret by repo-level " + repoSecretDeclFile}
	}
	if matchSecretPatterns(user.Secret, "", abs) {
		return SecretVerdict{true, "declared secret by user-level config (secretPaths)"}
	}

	kind := secretpath.Classify(abs)
	if kind == secretpath.NotSecret {
		return SecretVerdict{}
	}
	// 2. Named stores: secret by built-in default, not overridable.
	if kind == secretpath.WellKnownSecret && !secretpath.IsCredentialFormat(abs) {
		return SecretVerdict{true, wellKnownSecretReason}
	}

	// 3. Credential-format files and the bare `secrets` component: lifted by
	// a not-secret declaration (repo > user) or by the tracked-file vouch.
	if matchSecretPatterns(repo.NotSecret, repoRoot, abs) {
		return SecretVerdict{false, "declared not-secret by repo-level " + repoSecretDeclFile}
	}
	if matchSecretPatterns(user.NotSecret, "", abs) {
		return SecretVerdict{false, "declared not-secret by user-level config (notSecretPaths)"}
	}
	if ok, reason := cache.trackedVouch(kinds, abs); ok {
		return SecretVerdict{false, reason}
	}
	if kind == secretpath.GenericSecretsDir {
		return SecretVerdict{true, genericSecretsDirReason}
	}
	return SecretVerdict{true, wellKnownSecretReason}
}

// trackedVouch is NonSecretWith memoized per abs in the per-evaluation cache
// (nil cache: unmemoized).
func (c *R2Cache) trackedVouch(kinds []Kind, abs string) (bool, string) {
	if c == nil {
		return NonSecretWith(kinds, abs)
	}
	c.mu.Lock()
	if e, ok := c.vouch[abs]; ok {
		c.mu.Unlock()
		return e.ok, e.reason
	}
	c.mu.Unlock()
	ok, reason := NonSecretWith(kinds, abs)
	c.mu.Lock()
	c.vouch[abs] = vouchEntry{ok, reason}
	c.mu.Unlock()
	return ok, reason
}

// userDecls returns the user-level declarations (memoized when c != nil).
func (c *R2Cache) userDecls() SecretDeclarations {
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.userDecl != nil {
			return *c.userDecl
		}
	}
	cfg := userconfig.LoadDefault()
	d := SecretDeclarations{Secret: cfg.SecretPaths, NotSecret: cfg.NotSecretPaths}
	if c != nil {
		c.userDecl = &d
	}
	return d
}

// repoDecls returns the repo-level declarations of the repo at root
// (memoized when c != nil). A missing or malformed file is empty.
func (c *R2Cache) repoDecls(root string) SecretDeclarations {
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if d, ok := c.repoDecl[root]; ok {
			return d
		}
	}
	var d SecretDeclarations
	if data, err := os.ReadFile(filepath.Join(specfmt.DefaultRepoDir(root), repoSecretDeclFile)); err == nil {
		if json.Unmarshal(data, &d) != nil {
			d = SecretDeclarations{}
		}
	}
	if c != nil {
		c.repoDecl[root] = d
	}
	return d
}

// matchSecretPatterns reports whether abs matches (or lies beneath a match
// of) any pattern. `~/` expands against $HOME; a relative pattern is joined
// to base (the repo root) and skipped when base is "" (never matches
// everything by accident).
func matchSecretPatterns(patterns []string, base, abs string) bool {
	for _, p := range patterns {
		pat := p
		if strings.HasPrefix(pat, "~/") {
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				continue
			}
			pat = filepath.Join(home, pat[2:])
		}
		if !filepath.IsAbs(pat) {
			if base == "" {
				continue
			}
			pat = filepath.Join(base, pat)
		}
		pat = filepath.ToSlash(filepath.Clean(pat))
		a := filepath.ToSlash(abs)
		if globMatch(pat, a) || globMatch(pat+"/**", a) {
			return true
		}
	}
	return false
}
