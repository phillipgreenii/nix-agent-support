package sync

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

func fp(repo string, n int, oid string) vcs.PRFingerprint {
	return vcs.PRFingerprint{Repo: repo, Number: n, Author: "me", State: "open", HeadOID: oid}
}

func TestDiffRoster_AddedChangedDisappeared(t *testing.T) {
	prev := map[prKey]string{
		{Repo: "o/r", Number: 1}: fingerprintHash(fp("o/r", 1, "a")),
		{Repo: "o/r", Number: 2}: fingerprintHash(fp("o/r", 2, "b")),
	}
	roster := []vcs.PRFingerprint{
		fp("o/r", 1, "a"),  // unchanged → skip
		fp("o/r", 3, "c"),  // new, no bead → added
		fp("o/r", 2, "b2"), // changed (oid) → changed
	}
	openBeads := map[prKey]bool{
		{Repo: "o/r", Number: 1}: true,
		{Repo: "o/r", Number: 2}: true,
		{Repo: "o/r", Number: 9}: true, // bead, not in roster → disappeared
	}
	d := diffRoster(prev, roster, openBeads, true /*complete*/)
	if !d.enqueued[prKey{Repo: "o/r", Number: 3}] || d.reasons[prKey{Repo: "o/r", Number: 3}] != "added" {
		t.Errorf("PR 3 should be added: %+v", d)
	}
	if !d.enqueued[prKey{Repo: "o/r", Number: 2}] || d.reasons[prKey{Repo: "o/r", Number: 2}] != "changed" {
		t.Errorf("PR 2 should be changed: %+v", d)
	}
	if !d.enqueued[prKey{Repo: "o/r", Number: 9}] || d.reasons[prKey{Repo: "o/r", Number: 9}] != "disappeared" {
		t.Errorf("bead 9 should be disappeared: %+v", d)
	}
	if d.enqueued[prKey{Repo: "o/r", Number: 1}] {
		t.Errorf("PR 1 unchanged should be skipped")
	}
}

func TestDiffRoster_TruncatedSkipsDisappeared(t *testing.T) {
	openBeads := map[prKey]bool{{Repo: "o/r", Number: 9}: true}
	d := diffRoster(map[prKey]string{}, nil, openBeads, false /*incomplete*/)
	if d.enqueued[prKey{Repo: "o/r", Number: 9}] {
		t.Error("incomplete roster must NOT enqueue disappeared (mass-close guard)")
	}
}

func TestBuildQueries(t *testing.T) {
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}, {Remote: "o/r2"}}}
	mine := buildMineQuery(cfg)
	if mine != "is:pr is:open author:me repo:o/r repo:o/r2" {
		t.Errorf("mine query = %q", mine)
	}
	team := buildTeamQuery(config.RepoConfig{Remote: "o/r", TeamMembers: []string{"a", "b"}})
	if team != "is:pr is:open repo:o/r author:a author:b" {
		t.Errorf("team query = %q", team)
	}
	if buildTeamQuery(config.RepoConfig{Remote: "o/r"}) != "" {
		t.Error("team query with no members should be empty")
	}
}
