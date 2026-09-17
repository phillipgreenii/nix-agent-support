package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// deskConfigLoad and deskStoreOpen are the shared config/store seams for
// every command this packet implements (open, hide, unhide, wip, feedback,
// show, status, doctor, heartbeat, heartbeat-item) — mirroring run.go's own
// runConfigLoad/runStoreOpen convention (packet 6's own injectable-seam
// pattern), but declared once here rather than duplicated per file, since
// every command below needs the identical seam and this packet authors all
// of them together. Tests override these to inject a fixture config/store
// without touching the real $PG_DESK_CONFIG / $XDG_STATE_HOME/pg-desk/store.db.
var deskConfigLoad = func(ctx context.Context) (*config.Config, error) { return config.Load(ctx) }

var deskStoreOpen = func() (*store.Store, error) { return store.Open(store.DefaultPath()) }

// entityTypePR is the one entity type every command in this packet operates
// on — Phase 9 supports PR entities only (docs/behavior/pg-desk/README.md's
// "Scope" section); issue/thread commands are later phases.
const entityTypePR = "pr"

// Panel name constants, redeclared locally rather than imported from
// internal/interpret — mirroring internal/httpapi/server.go's own identical
// redeclaration (see that file's comment): this packet's Contract pins
// internal/store and internal/pipeline as Consumes, not internal/interpret,
// so the panel and ownership STRING VALUES a stored interpretation row
// carries are compared here as plain strings rather than importing
// packet 5's Go API. Values pinned verbatim from the design doc's section
// 7.7 ("the five named selectors").
const (
	panelMineActNow              = "mine_act_now"
	panelMineAwaitingOthers      = "mine_awaiting_others"
	panelMineAwaitingOtherThings = "mine_awaiting_other_things"
	panelTeamActNow              = "team_act_now"
	panelTeamBlocked             = "team_blocked"
)

// ownershipMine and ownershipCoOwned mirror internal/interpret.Ownership's
// "acts as mine" values (see that package's Ownership.ActsAsMine) — again
// as plain strings, per this file's panel-constant comment above.
const (
	ownershipMine    = "mine"
	ownershipCoOwned = "co-owned"
)

// actsAsMine reports whether a stored interpretation row's Ownership value
// should be treated like the operator's own PR.
func actsAsMine(ownership string) bool {
	return ownership == ownershipMine || ownership == ownershipCoOwned
}

// Feedback disposition verdicts, redeclared locally for the same reason as
// the panel constants above — mirrors internal/interpret's
// DispositionOpen/DispositionWillFix/DispositionWontFix/DispositionNoAction
// (docs/behavior/pg-desk/feedback.md's pinned vocabulary).
const (
	dispositionOpen     = "open"
	dispositionWillFix  = "will-fix"
	dispositionWontFix  = "wont-fix"
	dispositionNoAction = "no-action"
)

func validDisposition(v string) bool {
	switch v {
	case dispositionOpen, dispositionWillFix, dispositionWontFix, dispositionNoAction:
		return true
	default:
		return false
	}
}

// outputEnvVar is pg-desk's own output-mode override [design "Human views
// and operator commands": "The PG_DESK_OUTPUT=json environment variable
// overrides the default text rendering"], mirroring pg-pr's PGPR_OUTPUT
// (packages/pg-pr/internal/output.EnvVar).
const outputEnvVar = "PG_DESK_OUTPUT"

// resolveJSONOutput reports whether JSON output is selected: either the
// command's own --json flag, or outputEnvVar=json with no flag passed.
func resolveJSONOutput(flag bool) bool {
	if flag {
		return true
	}
	return strings.EqualFold(os.Getenv(outputEnvVar), "json")
}

// prURLNumberRE matches a PR URL's trailing /pull/<N> (with an optional
// trailing slash) — the GitHub PR-URL shape named by hide-unhide-wip.md's
// "<pr> MUST accept OWNER/REPO#N, a PR URL, or a bare number".
var prURLNumberRE = regexp.MustCompile(`/pull/(\d+)/?$`)

// parsePRNumber extracts the bare PR number from any of the three accepted
// forms: a bare number ("123"), "OWNER/REPO#123", or a PR URL ending
// "/pull/123". Phase 9 supports exactly one configured repository
// (docs/behavior/pg-desk/README.md's "Scope"), so the owner/repo portion of
// the first two forms is not otherwise consulted — resolvePRRef below
// always resolves against cfg.Repos[0] regardless of what the reference
// itself named.
func parsePRNumber(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty PR reference")
	}
	if n, err := strconv.Atoi(ref); err == nil && n > 0 {
		return strconv.Itoa(n), nil
	}
	if i := strings.LastIndexByte(ref, '#'); i >= 0 {
		if n, err := strconv.Atoi(ref[i+1:]); err == nil && n > 0 {
			return strconv.Itoa(n), nil
		}
	}
	if m := prURLNumberRE.FindStringSubmatch(ref); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("cannot parse PR reference %q (want OWNER/REPO#N, a PR URL, or a bare number)", ref)
}

// resolvePRRef resolves a <pr> command-line argument to (repo, entityID).
// Phase 9 supports exactly one configured repository
// (docs/behavior/pg-desk/README.md's "Scope"), so repo is always
// cfg.Repos[0].Remote; resolving against the current working directory when
// no repo is configured at all (hide-unhide-wip.md's "or the current
// working directory resolves one") is out of scope for this phase, since a
// config with zero repos fails config.Load's own finalize() validation
// before any command here would even run.
//
// entityID is reconstructed as the FULL "<repo>#<n>" form, not the bare
// number parsePRNumber extracts — matching what the real gather pipeline
// actually writes into the entity/interpretation tables:
// internal/gather/gather.go's Gather takes entityID verbatim from
// pg-desk run's own CLI argument and passes it straight through to
// pg-connector's `pr show <entityID>`, and pg-connector's own PR-reference
// CLI convention requires the qualified owner/repo#N form (confirmed
// empirically against the live store — pg2-276sg). The writer side is
// authoritative here since gather/pg-connector's convention is fixed by a
// dependency this package does not control; this reader-side helper is
// the one that must match it, regardless of which of the three documented
// input forms (bare number / OWNER/REPO#N / URL) the caller passed — the
// owner/repo portion of the OWNER/REPO#N form is still not otherwise
// consulted (see parsePRNumber's doc comment): resolvePRRef always
// rebuilds the qualified id from cfg.Repos[0].Remote, never from whatever
// owner/repo the input itself named.
func resolvePRRef(cfg *config.Config, ref string) (repo, entityID string, err error) {
	if cfg == nil || len(cfg.Repos) == 0 {
		return "", "", fmt.Errorf("no repository configured")
	}
	n, err := parsePRNumber(ref)
	if err != nil {
		return "", "", err
	}
	repo = cfg.Repos[0].Remote
	return repo, repo + "#" + n, nil
}
