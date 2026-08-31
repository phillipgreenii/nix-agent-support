package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// selTestCfg builds a minimal config.Config with two roles and two queries,
// wired 1:1 by event type, so applySelectors tests have something concrete to
// narrow/exclude. It deliberately does not call config.Load()/Validate() —
// these are pure-logic tests of applySelectors itself.
func selTestCfg() config.Config {
	return config.Config{
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Binds: []string{"t1"}},
			{Name: "r2", Enabled: true, Binds: []string{"t2"}},
		},
		Queries: query.SourceSet{
			{Name: "q1"},
			{Name: "q2"},
		},
	}
}

func roleEnabled(rs roles.RoleSet, name string) bool {
	for _, r := range rs {
		if r.Name == name {
			return r.Enabled
		}
	}
	panic("test: role not found: " + name)
}

func queryNames(ss query.SourceSet) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Name)
	}
	return out
}

func TestStringSliceFlag_repeatableAcrossOccurrences(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	only, disable := registerSelectorFlags(fs)
	if err := fs.Parse([]string{"--only", "role:a", "--only", "query:b", "--disable", "role:c"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := []string{"role:a", "query:b"}; !reflect.DeepEqual(only.values, want) {
		t.Errorf("only.values = %v, want %v", only.values, want)
	}
	if want := []string{"role:c"}; !reflect.DeepEqual(disable.values, want) {
		t.Errorf("disable.values = %v, want %v", disable.values, want)
	}
}

func TestParseSelector(t *testing.T) {
	cases := []struct {
		in       string
		wantKind string
		wantName string
		wantErr  bool
	}{
		{"role:foo", selectorKindRole, "foo", false},
		{"query:bar", selectorKindQuery, "bar", false},
		{"role:", "", "", true},
		{"bogus:foo", "", "", true},
		{"noColonAtAll", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			kind, name, err := parseSelector(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSelector(%q) = nil error, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSelector(%q) unexpected error: %v", tc.in, err)
			}
			if kind != tc.wantKind || name != tc.wantName {
				t.Errorf("parseSelector(%q) = (%q, %q), want (%q, %q)", tc.in, kind, name, tc.wantKind, tc.wantName)
			}
		})
	}
}

func TestSplitEnvList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"role:a", []string{"role:a"}},
		{"role:a,query:b", []string{"role:a", "query:b"}},
		{" role:a , query:b ,", []string{"role:a", "query:b"}}, // trims + drops trailing-comma blank
	}
	for _, tc := range cases {
		if got := splitEnvList(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitEnvList(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// PR_POOL_ONLY/PR_POOL_DISABLE are UNIONED with --only/--disable flags, not
// overridden by them (DEC-CLI-1) — deliberately unlike --socket/--token's
// flag-wins precedent in push_inject.go's injectedRef.
func TestResolveSelectors_unionsFlagsAndEnv(t *testing.T) {
	t.Setenv(envOnly, "role:fromenv")
	t.Setenv(envDisable, "query:fromenv")
	got := resolveSelectors([]string{"role:fromflag"}, []string{"query:fromflag"})
	if want := []string{"role:fromflag", "role:fromenv"}; !reflect.DeepEqual(got.Only, want) {
		t.Errorf("Only = %v, want %v", got.Only, want)
	}
	if want := []string{"query:fromflag", "query:fromenv"}; !reflect.DeepEqual(got.Disable, want) {
		t.Errorf("Disable = %v, want %v", got.Disable, want)
	}
}

func TestResolveSelectors_noEnvNoFlags(t *testing.T) {
	t.Setenv(envOnly, "")
	t.Setenv(envDisable, "")
	got := resolveSelectors(nil, nil)
	if len(got.Only) != 0 || len(got.Disable) != 0 {
		t.Errorf("resolveSelectors(nil, nil) = %+v, want both empty", got)
	}
}

// TestCheckSmokeReachable covers Task 1.5c's "respect --only/--disable" smoke
// scoping: a role/query excluded by the active selectors is reported
// unreachable; everything else stays reachable, same combination rule
// applySelectors already implements (selectorActive).
func TestCheckSmokeReachable(t *testing.T) {
	disabled := runSelectors{Disable: []string{"role:worker", "query:q2"}}
	if err := checkSmokeReachable(selectorKindRole, "worker", disabled); err == nil {
		t.Error("worker is disabled; want a non-nil error")
	} else if !strings.Contains(err.Error(), "worker") {
		t.Errorf("error should name worker; got %v", err)
	}
	if err := checkSmokeReachable(selectorKindRole, "feedback", disabled); err != nil {
		t.Errorf("feedback is not disabled; want no error, got %v", err)
	}
	if err := checkSmokeReachable(selectorKindQuery, "q2", disabled); err == nil {
		t.Error("q2 is disabled; want a non-nil error")
	}

	onlyRole := runSelectors{Only: []string{"role:worker"}}
	if err := checkSmokeReachable(selectorKindRole, "worker", onlyRole); err != nil {
		t.Errorf("worker is named by --only; want no error, got %v", err)
	}
	if err := checkSmokeReachable(selectorKindRole, "feedback", onlyRole); err == nil {
		t.Error("feedback is excluded by --only naming only worker; want a non-nil error")
	}

	if err := checkSmokeReachable(selectorKindRole, "worker", runSelectors{}); err != nil {
		t.Errorf("no active selectors; want no error, got %v", err)
	}
}

// applySelectors' combination rule (DEC-CLI-1): an empty --only leaves every
// configured participant a candidate.
func TestApplySelectors_emptyOnlyMeansEveryoneIsACandidate(t *testing.T) {
	cfg := selTestCfg()
	got, err := applySelectors(cfg, runSelectors{})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if !roleEnabled(got.Roles, "r1") || !roleEnabled(got.Roles, "r2") {
		t.Errorf("both roles should remain enabled with no selectors, got %+v", got.Roles)
	}
	if want := []string{"q1", "q2"}; !reflect.DeepEqual(queryNames(got.Queries), want) {
		t.Errorf("queries = %v, want %v", queryNames(got.Queries), want)
	}
}

// --disable alone excludes just the named participant, leaving the rest active.
func TestApplySelectors_disableAloneExcludesOnlyNamed(t *testing.T) {
	cfg := selTestCfg()
	got, err := applySelectors(cfg, runSelectors{Disable: []string{"role:r2", "query:q2"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if !roleEnabled(got.Roles, "r1") {
		t.Errorf("r1 should remain enabled")
	}
	if roleEnabled(got.Roles, "r2") {
		t.Errorf("r2 should be disabled")
	}
	if want := []string{"q1"}; !reflect.DeepEqual(queryNames(got.Queries), want) {
		t.Errorf("queries = %v, want %v", queryNames(got.Queries), want)
	}
}

// --only alone narrows to just the named participant.
func TestApplySelectors_onlyAloneNarrows(t *testing.T) {
	cfg := selTestCfg()
	got, err := applySelectors(cfg, runSelectors{Only: []string{"role:r1", "query:q1"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if !roleEnabled(got.Roles, "r1") {
		t.Errorf("r1 should remain enabled (named by --only)")
	}
	if roleEnabled(got.Roles, "r2") {
		t.Errorf("r2 should be excluded (not named by --only)")
	}
	if want := []string{"q1"}; !reflect.DeepEqual(queryNames(got.Queries), want) {
		t.Errorf("queries = %v, want %v", queryNames(got.Queries), want)
	}
}

// Both flags combined: --only narrows the candidate set first, THEN --disable
// removes from what's left (DEC-CLI-1's combination rule) — naming the SAME
// participant in both --only and --disable excludes it (disable wins on a
// conflict, since it is applied second).
func TestApplySelectors_bothFlagsNarrowThenExclude(t *testing.T) {
	cfg := config.Config{
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Binds: []string{"t1"}},
			{Name: "r2", Enabled: true, Binds: []string{"t2"}},
			{Name: "r3", Enabled: true, Binds: []string{"t3"}},
		},
	}
	// --only r1,r2 narrows the candidates to {r1, r2}; --disable r2 then removes
	// r2 from that narrowed set, leaving only r1 active. r3 was never a
	// candidate at all (excluded by --only, independent of --disable).
	got, err := applySelectors(cfg, runSelectors{
		Only:    []string{"role:r1", "role:r2"},
		Disable: []string{"role:r2"},
	})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if !roleEnabled(got.Roles, "r1") {
		t.Errorf("r1 should remain active")
	}
	if roleEnabled(got.Roles, "r2") {
		t.Errorf("r2 should be excluded (named by --only AND --disable)")
	}
	if roleEnabled(got.Roles, "r3") {
		t.Errorf("r3 should be excluded (not named by --only)")
	}
}

// A role already disabled in the loaded configuration stays disabled: a
// selector can only ever narrow further, never re-enable something the
// configuration itself turned off.
func TestApplySelectors_alreadyDisabledRoleStaysDisabled(t *testing.T) {
	cfg := config.Config{
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: false, Binds: []string{"t1"}},
		},
	}
	got, err := applySelectors(cfg, runSelectors{Only: []string{"role:r1"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if roleEnabled(got.Roles, "r1") {
		t.Errorf("an already-disabled role must stay disabled even when named by --only")
	}
}

// A selector naming a role/query the configuration never declares is a usage
// error — fail loud on a typo rather than silently building an allow-list
// that excludes everything.
func TestApplySelectors_unknownNameIsAnError(t *testing.T) {
	cfg := selTestCfg()
	cases := []runSelectors{
		{Only: []string{"role:nope"}},
		{Disable: []string{"role:nope"}},
		{Only: []string{"query:nope"}},
		{Disable: []string{"query:nope"}},
	}
	for _, sel := range cases {
		if _, err := applySelectors(cfg, sel); err == nil {
			t.Errorf("applySelectors(%+v) = nil error, want an error naming the unknown selector", sel)
		} else if !strings.Contains(err.Error(), "nope") {
			t.Errorf("applySelectors(%+v) error = %v, want it to name the unknown selector", sel, err)
		}
	}
}

// A malformed selector (bad grammar) is also an error, surfaced the same way
// as an unknown name.
func TestApplySelectors_malformedSelectorIsAnError(t *testing.T) {
	cfg := selTestCfg()
	if _, err := applySelectors(cfg, runSelectors{Only: []string{"bogus"}}); err == nil {
		t.Error("applySelectors with a malformed selector should error")
	}
}

// applySelectors must not mutate the caller's own Roles/Queries backing
// arrays — the config it was loaded from (and, trivially, the config FILE on
// disk) must be left exactly as loaded.
func TestApplySelectors_doesNotMutateCallersConfig(t *testing.T) {
	cfg := selTestCfg()
	originalRoles := cfg.Roles // same slice header / backing array
	originalQueries := cfg.Queries

	if _, err := applySelectors(cfg, runSelectors{Disable: []string{"role:r2", "query:q2"}}); err != nil {
		t.Fatalf("applySelectors: %v", err)
	}

	if !roleEnabled(originalRoles, "r2") {
		t.Error("applySelectors mutated the caller's own Roles slice in place")
	}
	if want := []string{"q1", "q2"}; !reflect.DeepEqual(queryNames(originalQueries), want) {
		t.Errorf("applySelectors mutated the caller's own Queries slice: %v", queryNames(originalQueries))
	}
}
