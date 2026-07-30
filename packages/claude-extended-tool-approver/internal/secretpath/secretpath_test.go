package secretpath

import (
	"strings"
	"testing"
)

func TestIsSecret(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// Claude credential files (the motivating case, pg2-to8pe)
		{"claude credentials tilde", "~/.claude/.credentials", true},
		{"claude credentials absolute", "/Users/phillipg/.claude/.credentials", true},
		{"claude credentials json (linux oauth file)", "~/.claude/.credentials.json", true},
		{"claude auth.json", "~/.claude/auth.json", true},

		// secrets/ directory component (requires a path separator)
		{"secrets dir nested json", "secrets/gnosis/svc/foo.json", true},
		{"secrets dir absolute", "/repo/secrets/prod.yaml", true},
		{"secrets substring is not a component", "secretsauce/config.yaml", false},
		{"bare secrets word is not a path (kubectl get secrets)", "secrets", false},

		// .ssh/ directory component (requires a path separator)
		{"ssh config tilde", "~/.ssh/config", true},
		{"ssh private key", "~/.ssh/id_rsa", true},
		{"ssh dir itself", "~/.ssh", true},
		{"ssh known_hosts absolute", "/home/user/.ssh/known_hosts", true},
		{"ssh substring is not a component", "myssh/notes.txt", false},
		{"bare .ssh word without separator", ".ssh", false},

		// .env files
		{"bare dotenv", ".env", true},
		{"dotenv dot-slash", "./.env", true},
		{"dotenv nested", "project/.env", true},
		{"dotenv local", ".env.local", true},
		{"dotenv production", ".env.production", true},
		{"dotenv example is not secret", ".env.example", false},
		{"dotenv sample is not secret", ".env.sample", false},
		{"dotenv template is not secret", ".env.template", false},
		{"dotenv dist is not secret", ".env.dist", false},

		// *token*.json
		{"api token json", "config/api-token.json", true},
		{"github token json", "github-token.json", true},
		{"bare token json", "token.json", true},
		{"uppercase Token json", "Auth-Token.json", true},
		{"token but not json", "token.txt", false},

		// Non-secrets
		{"empty", "", false},
		{"go source", "internal/main.go", false},
		{"readme", "README.md", false},
		{"claude settings is not a secret", "~/.claude/settings.json", false},
		{"generic config json", "src/config.json", false},
		{"credentials with extra suffix is exact-match only", "~/.claude/.credentials.bak", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSecret(tt.path); got != tt.want {
				t.Errorf("IsSecret(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// CRITERION: EVERY arm of IsSecret agrees on case handling (pg2-faaq2). Before
// the fix exactly one arm folded — the *token*.json arm, and only via ToLower —
// so `~/.SSH/id_rsa`, `~/proj/.ENV`, `~/.claude/Auth.json` and `SECRETS/k.pem`
// all returned false while naming the SAME real credential files as their
// lowercase spellings on this case-insensitive APFS volume, and fell through to
// the downstream auto-approvers.
//
// The matching and NON-matching shapes are deliberately in the SAME table,
// adjacent per arm: a table of positives alone would pass just as well for a
// predicate that had been widened into "contains the letters", and the bound
// rows are what distinguish folding from widening. Every structural bound is
// re-asserted here in its case-VARIED spelling, because a bound that only holds
// for the lowercase spelling is not a bound.
//
// Witnesses here are ASCII case variations ONLY. The Unicode fold witnesses live
// in TestIsSecret_FoldsNotMerelyLowercases so that downgrading the fold
// primitive to ToLower fails exactly that one test.
func TestIsSecret_AllArmsFoldCase(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// --- arm 1: secretDirs, whole-component directory match ---
		{"dir .ssh baseline", "/Users/u/.ssh/id_rsa", true},
		{"dir .SSH varied (the worst item in the gap)", "/Users/u/.SSH/id_rsa", true},
		{"dir .Ssh varied", "/Users/u/.Ssh/id_rsa", true},
		{"dir secrets baseline", "/Users/u/proj/secrets/k.pem", true},
		{"dir SECRETS varied", "/Users/u/proj/SECRETS/k.pem", true},
		{"dir Secrets varied", "/Users/u/proj/Secrets/k.pem", true},
		// bounds: folding must not become substring matching...
		{"bound: SECRETSAUCE is not a component", "SECRETSAUCE/config.yaml", false},
		{"bound: MYSSH is not a component", "MYSSH/notes.txt", false},
		{"bound: .SSHX is a different component", "/Users/u/.SSHX/id_rsa", false},
		// ...and must not drop the separator requirement (kubectl get SECRETS).
		{"bound: bare SECRETS word without separator", "SECRETS", false},
		{"bound: bare .SSH word without separator", ".SSH", false},

		// --- arm 2: secretBasenames, whole-basename match ---
		{"base auth.json baseline", "/Users/u/.claude/auth.json", true},
		{"base Auth.json varied", "/Users/u/.claude/Auth.json", true},
		{"base AUTH.JSON varied", "/Users/u/.claude/AUTH.JSON", true},
		{"base .credentials varied", "~/.claude/.CREDENTIALS", true},
		{"base .credentials.json varied", "~/.claude/.Credentials.Json", true},
		// bounds: still the WHOLE basename, and still an exact name.
		{"bound: .CREDENTIALS.BAK is a different basename", "~/.claude/.CREDENTIALS.BAK", false},
		{"bound: MY-AUTH.JSON is a different basename", "~/.claude/MY-AUTH.JSON", false},
		{"bound: SETTINGS.JSON is not a credential", "~/.claude/SETTINGS.JSON", false},

		// --- arm 3: .env / .env.* ---
		{"dotenv baseline", "/Users/u/proj/.env", true},
		{"dotenv .ENV varied", "/Users/u/proj/.ENV", true},
		{"dotenv .Env varied", "/Users/u/proj/.Env", true},
		{"dotenv .ENV.PRODUCTION varied", ".ENV.PRODUCTION", true},
		{"dotenv .Env.Local varied", ".Env.Local", true},
		// bounds: the non-secret variants MUST fold too, or a committed sample
		// file newly prompts (false positive in the annoying direction).
		{"bound: .env.example baseline is not secret", ".env.example", false},
		{"bound: .ENV.EXAMPLE varied is not secret", ".ENV.EXAMPLE", false},
		{"bound: .Env.Sample varied is not secret", ".Env.Sample", false},
		{"bound: .ENV.TEMPLATE varied is not secret", ".ENV.TEMPLATE", false},
		{"bound: .Env.Dist varied is not secret", ".Env.Dist", false},
		{"bound: .ENV.EXAMPLE nested is not secret", "proj/config/.ENV.EXAMPLE", false},
		// bounds: folding must not widen ".env" into a prefix/substring match.
		{"bound: .ENVRC is not a dotenv", ".ENVRC", false},
		{"bound: ENV is not a dotenv (no leading dot)", "ENV", false},
		{"bound: MY.ENV is not a dotenv", "MY.ENV", false},
		{"bound: ..ENV is not a dotenv", "..ENV", false},

		// --- arm 4: *token*.json (the only arm that folded before the fix) ---
		{"token baseline", "config/api-token.json", true},
		{"token TOKEN.JSON varied", "config/API-TOKEN.JSON", true},
		{"token Auth-Token.json varied", "Auth-Token.json", true},
		// bounds: the extension still has to be .json.
		{"bound: TOKEN.TXT is not json", "TOKEN.TXT", false},
		{"bound: JSON-TOKENS.YAML is not json", "JSON-TOKENS.YAML", false},

		// --- every arm varied at once ---
		{"varied dir plus varied credential basename", "/Users/u/.SSH/AUTH.JSON", true},
		{"varied dir plus non-secret basename still matches on the dir", "/Users/u/SECRETS/README.MD", true},
		{"all-caps non-secret path matches nothing", "/USERS/U/PROJ/INTERNAL/MAIN.GO", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSecret(tt.path); got != tt.want {
				t.Errorf("IsSecret(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// THE FOLD PRIMITIVE IS Unicode simple case FOLDING, NOT strings.ToLower — a
// correctness requirement, not a style choice, and the same one pinned for the
// sibling bead pg2-2ng80 by
// internal/rules/pathsafety.TestPathSafety_WriteAgentConfig_FoldsNotMerelyLowercases
// (commit 75ca1189).
//
// EqualFold implements case FOLDING; ToLower implements case MAPPING, and the
// two disagree on codepoints APFS treats as equal. Verified on this machine's
// APFS volume: after writing `.claude/settings.local.json`, reading
// `.claude/ſettings.local.json` (U+017F LATIN SMALL LETTER LONG S) returns the
// same bytes — the filesystem folds `ſ` to `s`. So every witness below names a
// GENUINE credential file, and a ToLower-based predicate misses all of them:
// the pg2-faaq2 bypass one codepoint over.
//
// U+017F is not an arbitrary pick. Scanning the entire Unicode range, it is the
// ONLY rune that folds onto an ASCII letter while ToLower leaves it unchanged,
// which makes it the single decisive witness for this property. It folds to `s`,
// so it reaches every arm whose pattern contains an `s` — ".ssh", "secrets",
// ".credentials", "auth.json" (the `s` in "json"), ".env.sample". The ".env"
// arm's own letters (e/n/v) have no such fold partner, so that arm is witnessed
// through its exclusion list instead.
//
// This test fails if the predicate is ever "simplified" to a ToLower or
// lowercased-key form.
func TestIsSecret_FoldsNotMerelyLowercases(t *testing.T) {
	tests := []struct {
		name string
		path string
		// canonical is the real file the witness names on a folding filesystem;
		// asserted below so a Go stdlib change cannot turn a row into a tautology.
		canonical string
		want      bool
	}{
		{"dir .ſsh names the real .ssh", "/Users/u/.ſsh/id_rsa", "/Users/u/.ssh/id_rsa", true},
		{"dir ſecrets names the real secrets", "/Users/u/proj/ſecrets/k.pem", "/Users/u/proj/secrets/k.pem", true},
		{"base auth.jſon names the real auth.json", "~/.claude/auth.jſon", "~/.claude/auth.json", true},
		{"base .credentialſ names the real .credentials", "~/.claude/.credentialſ", "~/.claude/.credentials", true},
		{"token arm: api-token.jſon names the real api-token.json", "config/api-token.jſon", "config/api-token.json", true},
		// The exclusion list must fold with the same primitive: `.env.ſample`
		// names the committed `.env.sample`, so it must stay a NON-secret. Under
		// ToLower the positive .env arm still fires (its letters are ASCII) while
		// the exclusion misses, so this row flips to a false positive — the
		// annoying direction, and the reason the negative half is pinned here too.
		{"exclusion .env.ſample still names the non-secret .env.sample", ".env.ſample", ".env.sample", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.EqualFold(tt.path, tt.canonical) {
				t.Fatalf("premise: EqualFold(%q, %q) must be true for this witness to exercise the fold", tt.path, tt.canonical)
			}
			if strings.ToLower(tt.path) == tt.canonical {
				t.Fatalf("premise: ToLower(%q) must NOT equal %q, or the witness cannot distinguish folding from lowercasing", tt.path, tt.canonical)
			}
			if got := IsSecret(tt.path); got != tt.want {
				t.Errorf("IsSecret(%q) = %v, want %v — the predicate must FOLD case (strings.EqualFold and the fold-aware helpers), not merely lowercase; %q names the real %q on this APFS volume",
					tt.path, got, tt.want, tt.path, tt.canonical)
			}
		})
	}
}

// BLAST RADIUS OF FOLDING THE DIRECTORY ARM (pg2-faaq2 acceptance criterion 5,
// and the separability argument for sibling bead pg2-pmk9q).
//
// pg2-pmk9q reports that `secretDirs` matching the whole component `secrets`
// already over-matches: it fires on this repo's OWN
// packages/claude-extended-tool-approver/internal/rules/secrets/ source
// directory, so reading the secrets rule's source prompts. That false positive
// is on the LITERAL LOWERCASE spelling, which matched before this fix and still
// matches now — folding neither creates nor worsens it. Folding only ADDS the
// case-varied spellings (`Secrets/`, `SECRETS/`, `.SSH/`), which is why the two
// beads are separable and pg2-faaq2 does NOT touch pg2-pmk9q's over-match.
//
// This test pins that separation from both sides so a future "fix" to either
// bead cannot silently absorb the other.
func TestIsSecret_DirectoryArmFoldBlastRadius(t *testing.T) {
	// The pg2-pmk9q false positive is PRE-EXISTING, not introduced here.
	preexisting := "packages/claude-extended-tool-approver/internal/rules/secrets/secrets.go"
	if !IsSecret(preexisting) {
		t.Errorf("IsSecret(%q) = false; this repo's own secrets-rule source is pg2-pmk9q's pre-existing false positive and must still match — if it stopped matching, pg2-pmk9q was fixed here by accident", preexisting)
	}

	// What folding ADDS is exactly the case-varied component spellings.
	for _, p := range []string{
		"repo/Secrets/prod.yaml",
		"repo/SECRETS/prod.yaml",
		"repo/SeCrEtS/prod.yaml",
		"/Users/u/.SSH/config",
		"/Users/u/.Ssh/config",
	} {
		if !IsSecret(p) {
			t.Errorf("IsSecret(%q) = false, want true; folding the directory arm must add the case-varied spellings", p)
		}
	}

	// What folding does NOT add: the component test is still a WHOLE-component
	// test and still requires a separator, so the widening is bounded to
	// spellings of the same two names.
	for _, p := range []string{
		"SECRETS",                    // kubectl get SECRETS — no separator
		"Secrets",                    // ditto
		"SECRETSAUCE/config.yaml",    // substring, not a component
		"repo/SECRETS-BACKUP/x.yaml", // different component name
		"repo/NOTSECRETS/x.yaml",     // different component name
		"repo/.SSHD/config",          // different component name
	} {
		if IsSecret(p) {
			t.Errorf("IsSecret(%q) = true, want false; folding must not relax the whole-component or separator bounds", p)
		}
	}
}
