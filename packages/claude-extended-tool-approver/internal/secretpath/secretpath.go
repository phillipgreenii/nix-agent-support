// Package secretpath recognizes well-known credential/secret file paths so the
// rule chain can prompt (Ask) before an agent reads them, rather than silently
// auto-approving (pg2-to8pe). Matching is purely lexical on the path string —
// it never touches the filesystem — so it works uniformly for Bash arguments
// and for the Read/Grep/Glob/Edit tool paths.
//
// # Case handling
//
// EVERY arm of the match is case-insensitive, and every arm uses the SAME
// primitive: Unicode simple case FOLDING (strings.EqualFold, or the fold-aware
// helpers below for the prefix/substring shapes stdlib does not cover).
//
// This is a correctness requirement, not a style choice. This machine's home
// volume is APFS and case-INSENSITIVE, so `~/.SSH/id_rsa` and `~/.ssh/id_rsa`
// name the SAME real private key, and `.ENV` and `.env` the same secret file.
// Before pg2-faaq2 exactly ONE arm folded (the `*token*.json` arm, and only via
// ToLower); every other arm compared exactly, so each case-varied spelling of a
// genuine credential path fell straight through this rule and on to the
// downstream auto-approvers — the very silent approval this package exists to
// prevent.
//
// FOLDING, NOT LOWERCASING — strings.EqualFold, never strings.ToLower.
// EqualFold implements Unicode simple case FOLDING; ToLower implements simple
// case MAPPING, and the two disagree on codepoints APFS treats as equal.
// Verified on this machine's APFS volume while fixing the sibling bead
// pg2-2ng80: after writing `.claude/settings.local.json`, reading
// `.claude/ſettings.local.json` (U+017F LATIN SMALL LETTER LONG S) returns the
// same bytes, because the filesystem folds `ſ` to `s`. ToLower leaves U+017F
// alone and MISSES that spelling, so a ToLower-based predicate is the identical
// bypass one codepoint over. The precedent and the fuller argument live in
// internal/rules/pathsafety/pathsafety.go's isAgentConfigPath (commit
// 75ca1189); the FoldsNotMerelyLowercases tests in both packages fail if anyone
// downgrades the primitive.
//
// The two error directions are wildly asymmetric, which is why over-matching is
// acceptable here and under-matching is not: an over-match costs ONE
// unnecessary Ask (a human confirms a non-secret read), while an under-match
// costs the ENTIRE control (a credential file is auto-approved silently). That
// is also why this MUST NOT be "optimized" into a runtime probe of the volume's
// case-sensitivity — some paths in this workspace are on case-SENSITIVE volumes
// (e.g. /Volumes/acme), so a per-volume answer would be
// correct-but-fragile in the direction that loses the control.
//
// Folding case is NOT the same as widening the match. Every structural bound
// survives it unchanged: secretDirs still matches a WHOLE path component (not a
// substring), the directory arm still requires a real separator, and the
// credential basenames still match the WHOLE basename.
//
// # This package stays filesystem-free; SCOPING lives in the caller
//
// Classify reports WHICH arm matched, not merely whether one did, because the
// arms do not all deserve the same treatment: `secrets` is a ROLE-DESCRIBING
// component that a source tree can hold innocently (pg2-pmk9q — this repo's own
// internal/rules/secrets/ matches it), whereas `.ssh` and the credential
// basenames NAME a specific credential store. The operator's 2026-08-13 ruling on
// pg2-fhb9q/pg2-pmk9q relaxes only the first, and only for paths inside a git
// repository — a FILESYSTEM question. Answering it here would break this
// package's defining property, so the split is exported and the decision is made
// by internal/rules/secrets, which holds a *patheval.PathEvaluator. Read that
// package's doc comment for the full ruling.
package secretpath

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind identifies WHICH arm of the lexical classifier matched a path. A caller
// that only needs the yes/no answer uses IsSecret.
//
// THE ORDER IS LOAD-BEARING: the values ascend in how SPECIFIC the evidence is,
// and Classify keeps the highest-valued match it finds, so a path that matches
// both arms (`secrets/.ssh/id_rsa`) reports the stronger one. Nothing else may be
// inserted between them without re-reading Classify.
type Kind int

const (
	// NotSecret: no arm matched.
	NotSecret Kind = iota
	// GenericSecretsDir: the only match is the bare, ROLE-DESCRIBING path
	// component `secrets`. It says a directory was NAMED "secrets" — which a
	// deployment tree means literally and a source tree does not (pg2-pmk9q).
	GenericSecretsDir
	// WellKnownSecret: an arm that names a SPECIFIC credential store or
	// credential file — the `.ssh`/`.gnupg` components, the credential
	// basenames, `.env*`, `*token*.json`, the `.aws/credentials` and
	// `.docker/config.json` scoped basenames (M3), and the unconditional
	// `*.pem`/`*.key` suffix (M4). Repo-blind: these mean the same thing
	// wherever they appear.
	WellKnownSecret
)

// secretDirs are path components whose presence anywhere in a path marks the
// whole path as secret (e.g. "secrets/prod.yaml", "~/.ssh/id_rsa"), each mapped
// to the Kind of evidence it constitutes.
//
// Membership is tested by an EqualFold SCAN (anyEqualFold / lookupFold), not by a
// hash lookup: the match must fold case, and no single normalization of the
// candidate reproduces EqualFold's folding. Keys are written in their canonical
// lowercase spelling for readability; folding makes that a convention, not a
// matching constraint.
var secretDirs = map[string]Kind{
	"secrets": GenericSecretsDir,
	".ssh":    WellKnownSecret,
	// .gnupg added by pg2-ia640.1 (2026-07-25 brainstorm pg2-c7zhc): a GnuPG
	// home directory names a specific credential store exactly like .ssh, so
	// it is WellKnownSecret rather than GenericSecretsDir.
	".gnupg": WellKnownSecret,
}

// secretBasenames are whole basenames that are credential files by name alone:
// the Claude credential files, the generic "auth.json", and the pg2-ia640.1
// additions below. Matched by anyEqualFold (see secretDirs).
var secretBasenames = map[string]bool{
	".credentials":      true,
	".credentials.json": true,
	"auth.json":         true,
	// pg2-ia640.1 (2026-07-25 brainstorm pg2-c7zhc): straightforward exact-
	// basename adds. ".netrc"/".pgpass" hold plaintext machine credentials;
	// ".npmrc" can hold an npm `_authToken`; ".envrc" is direnv's per-project
	// script, almost always exporting secrets into the shell.
	".netrc":  true,
	".pgpass": true,
	".npmrc":  true,
	".envrc":  true,
}

// scopedBasenames pairs a basename that is too GENERIC to treat as secret on
// its own with the single IMMEDIATE-PARENT component that makes it one — the
// pg2-ia640.1 M3 mechanism. "credentials" bare, or under some unrelated
// directory, says nothing; "credentials" directly inside ".aws" is the AWS
// CLI's plaintext access-key file. Likewise "config.json" is a name any tool
// might use, but directly inside ".docker" it is the Docker CLI's auth store
// (it can embed base64 basic-auth credentials).
//
// IMMEDIATE parent only, not "anywhere in the path": ".docker/contexts/meta/
// config.json" must NOT match — that config.json's immediate parent is
// "meta", not ".docker" — so the scan below tracks only the ONE component
// preceding the basename, never the whole ancestor list.
//
// Matching folds case on BOTH halves via strings.EqualFold, consistent with
// every other arm in this package.
var scopedBasenames = []struct {
	basename string
	parent   string
}{
	{"credentials", ".aws"},
	{"config.json", ".docker"},
}

// isScopedSecretBasename reports whether base is one of scopedBasenames'
// generic names AND parent — the component immediately preceding base in the
// path — is that name's required directory.
func isScopedSecretBasename(base, parent string) bool {
	for _, sb := range scopedBasenames {
		if strings.EqualFold(base, sb.basename) && strings.EqualFold(parent, sb.parent) {
			return true
		}
	}
	return false
}

// nonSecretDotEnv are .env.* variants that are conventionally committed and
// carry no real secrets, so they must NOT match.
//
// These fold too, and that direction matters as much as the positive arms: a
// folding .env arm with an exact-match exclusion list would newly classify
// `.ENV.EXAMPLE` as a secret — a false positive in the annoying direction, on
// a file that is committed to the repo precisely because it holds no secret.
var nonSecretDotEnv = map[string]bool{
	".env.example":  true,
	".env.sample":   true,
	".env.template": true,
	".env.dist":     true,
}

// IsSecret reports whether path points at a well-known credential/secret file.
// It recognizes, by default (no configuration required), across FOUR
// independent matching mechanisms:
//
//   - M1, whole path COMPONENT: any path under a "secrets/", ".ssh/" or
//     ".gnupg/" directory component;
//   - M2, exact whole BASENAME: the Claude credential basenames
//     ".credentials", ".credentials.json" (the Linux OAuth file) and
//     "auth.json"; ".netrc", ".pgpass", ".npmrc", ".envrc"; ".env" and
//     ".env.*" (excluding conventional non-secret variants such as
//     ".env.example"); and any "*token*.json" basename (a substring/suffix
//     shape within the exact-basename family, not a separate mechanism);
//   - M3, SCOPED basename: "credentials" and "config.json" are too generic to
//     treat as secret bare, so they match only under their specific
//     IMMEDIATE-parent directory — ".aws/credentials", ".docker/config.json"
//     (see scopedBasenames);
//   - M4, unconditional SUFFIX: any basename ending ".pem" or ".key",
//     EVERYWHERE — including test fixtures, "node_modules/", "testdata/" —
//     with no directory or repo scoping at all.
//
// Every one of those comparisons folds case — see the package comment for why
// that is load-bearing and why the primitive must stay EqualFold.
//
// Directory-component matching (M1) fires only when path actually looks like
// a path (contains a "/"), so a bare word like the `secrets` argument of
// `kubectl get secrets` is not misread as a secret path.
//
// # Accepted false-positive boundary (M4, pg2-ia640.1)
//
// M4 is a DELIBERATE, human, security-first ruling (2026-07-25 brainstorm
// pg2-c7zhc) that accepts a known false-positive cost: `cat testdata/cert.pem`
// or `grep foo node_modules/pkg/id.key` now Ask, even though such fixture and
// vendor files overwhelmingly hold no real secret. The alternative — leaving
// bare "*.pem"/"*.key" unmatched — was rejected because the under-match
// direction is the one this package's package comment already treats as
// unacceptable (a real private key silently auto-approved), and no directory
// or content heuristic reliably tells a fixture key from a real one. So the
// cost of M4 is bounded to extra Asks, never a missed credential, and it is
// intentionally NOT narrowed to skip test/vendor trees.
//
// TWO CONSUMER-SIDE CONSEQUENCES were measured (not guessed) when M4 landed,
// by replaying the production asklog through before/after binaries — because
// this package is filesystem/command-free, the argument-SHAPE fallout lives
// in the CALLERS, not here: internal/rules/secrets' package comment (decision
// 4) and secretCandidateArgs' doc records the `yq` filter-token fix and the
// `git grep` pattern-token gap (filed as pg2-e1163) that this breadth caused.
func IsSecret(path string) bool {
	return Classify(path) != NotSecret
}

// Classify reports which arm of IsSecret matched path — see Kind, and the package
// comment for why the distinction is exported rather than resolved here.
//
// It scans the WHOLE path rather than returning at the first matching component,
// because a path can match two arms and the STRONGER one must win: a caller that
// relaxes GenericSecretsDir must not have `secrets/.ssh/id_rsa` handed to it as
// merely generic. The scan is over a handful of components of one string, so the
// lost short-circuit costs nothing measurable.
func Classify(path string) Kind {
	if path == "" {
		return NotSecret
	}
	hasSeparator := strings.Contains(path, "/")
	// parent tracks the IMMEDIATE-parent component of base (the previous
	// non-skipped component), threaded through for M3's scoped-basename
	// mechanism (pg2-ia640.1). It is deliberately just the ONE preceding
	// component, not the whole ancestor chain — M3 is an immediate-parent
	// test, not a path-substring test.
	base, parent := "", ""
	kind := NotSecret
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			continue
		}
		if hasSeparator {
			if k, ok := lookupFold(secretDirs, part); ok && k > kind {
				kind = k
			}
		}
		parent = base
		base = part
	}
	if isSecretBasename(base) || isScopedSecretBasename(base, parent) {
		return WellKnownSecret
	}
	return kind
}

func isSecretBasename(base string) bool {
	if anyEqualFold(secretBasenames, base) {
		return true
	}
	if isSecretDotEnv(base) {
		return true
	}
	// *token*.json — API/OAuth token dumps.
	//
	// THE SUBSTRING HALF CANNOT USE EqualFold — EqualFold is a whole-string
	// compare, so it does not substitute for strings.Contains — and it must not
	// fall back to ToLower either. That "token" and "json" are pure ASCII does
	// NOT make ToLower safe: the hazard is a non-ASCII codepoint in the
	// CANDIDATE that folds ONTO an ASCII letter of the pattern. Measured over
	// the entire Unicode range, exactly ONE rune folds onto an ASCII letter and
	// is left unchanged by ToLower — U+017F (ſ), which folds to `s` — and
	// ".json" contains an `s`. So `api-token.jſon` names the real
	// `api-token.json` on this APFS volume, and a ToLower-based predicate misses
	// it. (U+212A KELVIN SIGN folds to `k` in "token"; ToLower happens to map it
	// too, so it is only a LENGTH witness — 3 bytes where `k` is 1.) Hence:
	//   - the EXTENSION is compared with EqualFold via filepath.Ext, a
	//     whole-string compare that folds correctly across differing byte
	//     lengths (U+017F is two bytes where `s` is one);
	//   - the "token" SUBSTRING uses containsFold, a fold-aware scan, because
	//     stdlib has no strings.ContainsFold and every "normalize then Contains"
	//     form is the bug above.
	if strings.EqualFold(filepath.Ext(base), ".json") && containsFold(base, "token") {
		return true
	}
	// *.pem / *.key — private keys and certificates (M4, pg2-ia640.1).
	//
	// UNCONDITIONAL and case-insensitive, matching EVERYWHERE (testdata/,
	// node_modules/, any fixture tree) with no directory/repo scoping — see
	// IsSecret's "Accepted false-positive boundary" doc for why that breadth
	// is a deliberate, ruled-on tradeoff rather than an oversight.
	//
	// filepath.Ext + EqualFold is the SAME idiom the *token*.json arm above
	// uses for its extension half: it is a whole-string compare on the
	// extension, so it folds correctly without the byte-length hazard
	// containsFold exists for (see that arm's comment). It is basename-only
	// by construction — Ext looks at base, not the full path — which is what
	// keeps "certs.pem/README.md" (a directory literally named "certs.pem")
	// from matching: that base is "README.md", whose extension is ".md".
	ext := filepath.Ext(base)
	return strings.EqualFold(ext, ".pem") || strings.EqualFold(ext, ".key")
}

// isSecretDotEnv reports whether base is ".env" or a secret ".env.*" variant.
//
// It splits on "." rather than fold-prefix-matching ".env.", because "." has no
// case-fold equivalents: every fold-equivalent spelling of a basename therefore
// has the IDENTICAL dot-segment structure, so comparing the "env" SEGMENT with
// EqualFold is exact and needs no byte-window arithmetic on a string whose
// folded spelling may differ in LENGTH from its canonical one.
//
// ".env" splits to ["", "env"] and ".env.local" to ["", "env", "local"], so the
// single segment test covers both shapes; ".envrc" splits to ["", "envrc"] and
// correctly does not match THIS arm — segs[1] is "envrc", not "env". (".envrc"
// IS still secret overall, since pg2-ia640.1 added it as its own exact entry
// in secretBasenames; that is a SEPARATE arm from this one and this function's
// "no" is correctly about the dotenv arm only, not about IsSecret as a whole.)
func isSecretDotEnv(base string) bool {
	segs := strings.Split(base, ".")
	if len(segs) < 2 || segs[0] != "" || !strings.EqualFold(segs[1], "env") {
		return false
	}
	return !anyEqualFold(nonSecretDotEnv, base)
}

// lookupFold returns the value stored under the key of set that case-folds equal
// to s. A SCAN is required rather than a hash lookup because no single
// normalization of s reproduces EqualFold's folding (see the package comment);
// the sets here have a handful of entries each and are consulted once per
// candidate, so it is free.
//
// It is generic over the value type only so the same scan serves both the
// membership sets (map[string]bool) and secretDirs (map[string]Kind) — writing it
// twice is how the two drift apart on the fold primitive, which is the exact
// defect pg2-faaq2 fixed.
func lookupFold[V any](set map[string]V, s string) (V, bool) {
	for key, v := range set {
		if strings.EqualFold(s, key) {
			return v, true
		}
	}
	var zero V
	return zero, false
}

// anyEqualFold reports whether s case-folds equal to any key of set.
func anyEqualFold[V any](set map[string]V, s string) bool {
	_, ok := lookupFold(set, s)
	return ok
}

// containsFold reports whether substr occurs in s under Unicode simple case
// folding — the fold-aware strings.Contains that stdlib does not provide.
//
// It must not be written as strings.Contains(fold(s), substr): no per-string
// normalization reproduces folding (strings.ToLower is exactly the bypass this
// package's comment describes), so the fold has to happen in the COMPARISON.
// The candidate start offsets are the rune boundaries of s, since a match
// cannot begin mid-rune.
func containsFold(s, substr string) bool {
	if substr == "" {
		return true
	}
	for i := range s {
		if hasPrefixFold(s[i:], substr) {
			return true
		}
	}
	return false
}

// hasPrefixFold reports whether s begins with prefix under Unicode simple case
// folding.
//
// It walks RUNE BY RUNE instead of comparing s[:len(prefix)] with EqualFold: a
// fold-equivalent spelling can occupy a different number of BYTES than its
// canonical form (U+017F is two bytes where "s" is one), so a byte window sized
// from prefix is the wrong window.
func hasPrefixFold(s, prefix string) bool {
	for _, pr := range prefix {
		sr, size := utf8.DecodeRuneInString(s)
		if size == 0 || !equalFoldRune(sr, pr) {
			return false
		}
		s = s[size:]
	}
	return true
}

// equalFoldRune reports whether a and b are equal under Unicode simple case
// folding — the per-rune primitive strings.EqualFold applies internally.
// unicode.SimpleFold walks a rune's fold orbit as a cycle, so iterating from a
// until it returns to a enumerates every rune that folds together with it.
func equalFoldRune(a, b rune) bool {
	if a == b {
		return true
	}
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}
