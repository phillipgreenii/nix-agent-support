package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// Run-scoped operator selectors (STORY-OP-3), realized per
// `phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions · DEC-CLI-1`:
// --only (allow-list) / --disable (deny-list), wired onto `run` and
// `run-until-idle` only (parseRunLikeArgs, args.go). This file holds the
// selector grammar, the repeatable flag.Value, the PR_POOL_ONLY/PR_POOL_DISABLE
// environment fold-in, and applySelectors — the function that turns a parsed
// request into a run-scoped copy of config.Config.

// selectorKindRole / selectorKindQuery are the two participant kinds a
// selector may name. A selector is "<kind>:<name>", where <name> is that
// participant's OWN configured name: roles.Role.Name (a configured [[role]],
// the operator-facing "handler") or query.Source.Name (a configured
// [[query]], the operator-facing "source"). "role"/"query" are this
// codebase's own nouns — matching how run-role/run-query already spell the
// same two participant kinds on this CLI — rather than the behavior docs'
// narrative "handler"/"source" terms (DEC-CLI-1).
const (
	selectorKindRole  = "role"
	selectorKindQuery = "query"
)

// envOnly / envDisable are PR_POOL_ONLY / PR_POOL_DISABLE (DEC-CLI-1): each a
// comma-separated list of selectors, UNIONED with any --only/--disable flag
// occurrences on the same invocation (resolveSelectors) — never one
// overriding the other.
const (
	envOnly    = "PR_POOL_ONLY"
	envDisable = "PR_POOL_DISABLE"
)

// stringSliceFlag is a repeatable flag.Value: each `--flag value` occurrence
// APPENDS to values, unlike the stdlib's scalar flag.String/flag.Bool
// (last-one-wins). No precedented multi-value flag exists elsewhere in this
// package (args.go/run.go/push_inject.go all use scalar flags), so this is
// the small custom flag.Value the design calls for.
type stringSliceFlag struct{ values []string }

func (s *stringSliceFlag) String() string { return strings.Join(s.values, ",") }

func (s *stringSliceFlag) Set(v string) error {
	s.values = append(s.values, v)
	return nil
}

// registerSelectorFlags wires --only/--disable onto fs (the same
// flag.NewFlagSet pattern push_inject.go's runPushInject uses), returning the
// flag.Value so the caller can read the parsed occurrences after fs.Parse.
func registerSelectorFlags(fs *flag.FlagSet) (only, disable *stringSliceFlag) {
	only = &stringSliceFlag{}
	disable = &stringSliceFlag{}
	fs.Var(only, "only", "restrict this run to the named source(s)/handler(s) (allow-list; repeatable; role:<name> or query:<name>)")
	fs.Var(disable, "disable", "exclude the named source(s)/handler(s) from this run (deny-list; repeatable; role:<name> or query:<name>)")
	return only, disable
}

// runSelectors is one run/run-until-idle invocation's parsed, combined
// --only/--disable request: the flag occurrences UNIONED with their
// PR_POOL_ONLY/PR_POOL_DISABLE equivalents.
type runSelectors struct {
	Only    []string
	Disable []string
}

// resolveSelectors unions parsed --only/--disable flag values with
// PR_POOL_ONLY/PR_POOL_DISABLE. Reading the environment deliberately does NOT
// happen in args.go's route()/parseRunLikeArgs (which stay I/O-free per
// pg2-52rn); it happens here, in the runRun/runRunUntilIdle entry points —
// the same split push_inject.go uses (injectedRef reads PR_POOL_SOCKET/
// PR_POOL_TOKEN in runPushInject, never in route()).
//
// Flags and environment are UNIONED rather than one overriding the other.
// This deliberately differs from injectedRef's --socket/--token precedent
// (flag wins over env): those are a single scalar identifying ONE target,
// where a flag silently out-ranking an unrelated env value makes sense, but
// --only/--disable are repeatable, CUMULATIVE lists — an operator who sets
// PR_POOL_DISABLE in their environment and adds one more --disable on this
// invocation expects BOTH exclusions applied, not one discarding the other.
func resolveSelectors(flagOnly, flagDisable []string) runSelectors {
	return runSelectors{
		Only:    append(append([]string{}, flagOnly...), splitEnvList(os.Getenv(envOnly))...),
		Disable: append(append([]string{}, flagDisable...), splitEnvList(os.Getenv(envDisable))...),
	}
}

// splitEnvList parses a PR_POOL_ONLY/PR_POOL_DISABLE value: a comma-separated
// list of selectors, each trimmed, blanks dropped (so a trailing comma or
// stray spaces don't produce a spurious empty selector).
func splitEnvList(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseSelector splits "<kind>:<name>" and validates kind against the two
// DEC-CLI-1 selector kinds.
func parseSelector(s string) (kind, name string, err error) {
	k, n, ok := strings.Cut(s, ":")
	if !ok || n == "" {
		return "", "", fmt.Errorf("invalid selector %q (want role:<name> or query:<name>)", s)
	}
	switch k {
	case selectorKindRole, selectorKindQuery:
		return k, n, nil
	default:
		return "", "", fmt.Errorf("invalid selector %q: unknown kind %q (want role or query)", s, k)
	}
}

// splitByKind parses every selector in selectors and buckets its <name> by
// kind, or reports the first parse error encountered.
func splitByKind(selectors []string) (roleNames, queryNames []string, err error) {
	for _, s := range selectors {
		kind, name, perr := parseSelector(s)
		if perr != nil {
			return nil, nil, perr
		}
		switch kind {
		case selectorKindRole:
			roleNames = append(roleNames, name)
		case selectorKindQuery:
			queryNames = append(queryNames, name)
		}
	}
	return roleNames, queryNames, nil
}

// checkKnownSelectors reports the first name (across lists) absent from
// known. A selector naming a role/query this configuration never declares is
// a usage error — fail loud on a typo, rather than silently building an
// allow-list that excludes everything or a deny-list that excludes nothing.
func checkKnownSelectors(kind string, known map[string]bool, lists ...[]string) error {
	for _, l := range lists {
		for _, n := range l {
			if !known[n] {
				return fmt.Errorf("selector names unknown %s %q", kind, n)
			}
		}
	}
	return nil
}

// selectorActive applies DEC-CLI-1's combination rule for one participant
// name: --only (if non-empty) narrows the candidates to just the named ones
// — an EMPTY --only leaves every configured participant of that kind a
// candidate; --disable then removes any named participant from whatever
// --only left.
func selectorActive(name string, only, disable []string) bool {
	if len(only) > 0 && !slices.Contains(only, name) {
		return false
	}
	return !slices.Contains(disable, name)
}

// checkSmokeReachable reports an error when name (a role or query.Source
// name, per kind — selectorKindRole/selectorKindQuery) is excluded by sel's
// active --only/--disable selection. This is the "respect --only/--disable"
// half of Task 1.5c's smoke scoping: interfaces.md's "Run-scoped selectors"
// states the restriction scopes "which participants that run activates and
// which a smoke test may reach", so run-role/run-query MUST honor the same
// exclusion run/run-until-idle already do, even though a smoke test names its
// one target directly rather than through a --only/--disable flag of its own.
// Reuses splitByKind (same as applySelectors) so a "role:x" selector never
// leaks into a query-kind check or vice versa. Pure: sel is already resolved
// (flags ∪ env — run-role/run-query define no --only/--disable flags of
// their own, so callers pass resolveSelectors(nil, nil), the environment-only
// view); callers translate a non-nil error into their own usage-error exit.
func checkSmokeReachable(kind, name string, sel runSelectors) error {
	onlyRoles, onlyQueries, err := splitByKind(sel.Only)
	if err != nil {
		return err
	}
	disableRoles, disableQueries, err := splitByKind(sel.Disable)
	if err != nil {
		return err
	}
	only, disable := onlyRoles, disableRoles
	if kind == selectorKindQuery {
		only, disable = onlyQueries, disableQueries
	}
	if selectorActive(name, only, disable) {
		return nil
	}
	return fmt.Errorf("%s %q is excluded by the active selectors (PR_POOL_ONLY/PR_POOL_DISABLE)", kind, name)
}

// applySelectors returns cfg with its Roles/Queries restricted to the active
// subset sel computes (STORY-OP-3 / DEC-CLI-1's combination rule), WITHOUT
// mutating cfg's own Roles/Queries backing arrays — both are copied before
// anything is flipped or dropped, so the caller's already-loaded cfg (and,
// trivially, the config FILE it was read from — this function touches no
// file) is left exactly as loaded. It is the caller's responsibility to
// discard the returned config.Config on a non-nil error.
//
// CALLERS MUST apply this AFTER cfg.Validate() has already run (inside
// config.Load(), and again inside precheck) against the FULL, unfiltered
// cfg, and MUST NOT call Validate() again on the value this returns.
// Validate() has no notion of run-scoping — invariants.md's "Run-scoping is
// not a config defect" (INV-WORKFLOW-1) states validity is judged against
// the configuration, never the run's active subset, and Validate()'s own
// doc comment says as much ("nothing below reads Role.Enabled"). Roles are
// filtered by flipping Enabled (never removed), which stays safe to
// re-validate because Validate deliberately never reads it either way; but
// Queries are filtered by REMOVING entries from the slice (query.Source
// carries no Enabled field — queries have never had a run-scoped-disabled
// concept before this bead), so a role that binds a type ONLY an excluded
// query emits would read, to a re-run Validate, as a genuine
// orphan-consumer / no-events-to-listen-for finding, indistinguishable from
// an actual misconfiguration. The asymmetry is a property of WHEN this is
// called (after the one real Validate, never before another), not of the
// filtered value itself — so prepareRun (run.go) calls this only after
// precheck succeeds, and nothing downstream calls Validate again.
//
// Roles: flipping Enabled to false for an excluded role reuses bootCore's and
// declaredBindTypes' EXISTING "declared but inactive this run" handling
// (run.go) — bootCore already skips registering a Listener for
// role.Enabled == false, and declaredBindTypes already counts a disabled
// role's Binds regardless of Enabled. No new machinery for roles.
//
// Queries: declaredBindTypes / core.NewBindings read only cfg.Roles, never
// cfg.Queries, so dropping a query from the slice cannot desynchronize
// INV-DISP-3's "declared" view — an excluded query still exists in the
// CONFIGURATION this Cfg was loaded from; it simply does not fire THIS run,
// which is exactly "declared but inactive" from the source side.
//
// Its second return value (Task 4.1, Binding Decision 4) is the
// runExclusions this call itself computed, captured at the SAME
// decision point as the Enabled-flip/slice-removal above (never a second,
// re-derived pass) — see runExclusions' own doc.

// runExclusions is applySelectors' own record of which configured roles/
// queries it excluded THIS RUN (Task 4.1, Binding Decision 4) — captured at
// the exact moment each exclusion decision is made, BEFORE Role.Enabled is
// flipped or a query.Source is dropped: once those mutations run, the
// post-selector config can no longer distinguish "config-disabled" from
// "selector-excluded" (both look like Enabled==false), and an excluded
// query.Source has already vanished from the slice entirely. Consumed by
// cmd/pr-pool's bootCore, which threads it into
// core.Options.ExcludedRoles/ExcludedSources so composeStatusReply's
// listeners[]/sources[] can report `excluded` independently of `enabled`.
type runExclusions struct {
	Roles   []string
	Sources []string
}

func applySelectors(cfg config.Config, sel runSelectors) (config.Config, runExclusions, error) {
	onlyRoles, onlyQueries, err := splitByKind(sel.Only)
	if err != nil {
		return config.Config{}, runExclusions{}, err
	}
	disableRoles, disableQueries, err := splitByKind(sel.Disable)
	if err != nil {
		return config.Config{}, runExclusions{}, err
	}

	knownRoles := make(map[string]bool, len(cfg.Roles))
	for _, r := range cfg.Roles {
		knownRoles[r.Name] = true
	}
	knownQueries := make(map[string]bool, len(cfg.Queries))
	for _, s := range cfg.Queries {
		knownQueries[s.Name] = true
	}
	if err := checkKnownSelectors("role", knownRoles, onlyRoles, disableRoles); err != nil {
		return config.Config{}, runExclusions{}, err
	}
	if err := checkKnownSelectors("query", knownQueries, onlyQueries, disableQueries); err != nil {
		return config.Config{}, runExclusions{}, err
	}

	var excluded runExclusions

	newRoles := make(roles.RoleSet, len(cfg.Roles))
	copy(newRoles, cfg.Roles)
	for i, r := range newRoles {
		if !selectorActive(r.Name, onlyRoles, disableRoles) {
			newRoles[i].Enabled = false
			excluded.Roles = append(excluded.Roles, r.Name)
		}
	}
	cfg.Roles = newRoles

	var newQueries query.SourceSet
	for _, s := range cfg.Queries {
		if selectorActive(s.Name, onlyQueries, disableQueries) {
			newQueries = append(newQueries, s)
		} else {
			excluded.Sources = append(excluded.Sources, s.Name)
		}
	}
	cfg.Queries = newQueries

	return cfg, excluded, nil
}
