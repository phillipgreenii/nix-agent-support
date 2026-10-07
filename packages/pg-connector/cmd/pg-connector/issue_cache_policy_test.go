package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The cache policy for the issue type (INV-CACHE-2..8). The policy is
// type-generic over pr and issue, so a backend such as the Jira one adopts it
// by configuration alone: these tests drive the umbrella with a scripted
// issue backend (Jira-style keys) and prove each part of the contract holds
// for type "issue", not only "pr".

// issueEnv points the umbrella at a scripted backend registered under
// connector.issue only, isolating the config, ledger and cache.
func issueEnv(t *testing.T, name, state string) *scriptedBackend {
	t.Helper()
	b := newScriptedBackend(t, name)
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	yaml := "connector:\n  issue:\n    - " + name + "\n"
	if state != "" {
		yaml += "state:\n" + state
	}
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
	// The issue ledger key carries the beads-tracker discriminator; keep it
	// out of this test's key space.
	t.Setenv(envIssueBeadsDir, "")
	return b
}

func runIssueChanges(t *testing.T, args ...string) changesWire {
	t.Helper()
	full := append([]string{"issue", "changes", "--query", "mine", "--consumer", "c1"}, args...)
	stdout, stderr, code := executePr(t, full)
	if code != 0 {
		t.Fatalf("issue changes exit = %d; stdout=%s stderr=%s", code, stdout, stderr)
	}
	return decodeChangesWire(t, stdout)
}

func TestIssueShow_ReadThroughServesWithinTTLAndFreshAsksTheOrigin(t *testing.T) {
	b := issueEnv(t, "backend-issue-rt", "")
	b.setEntity(t, "PROJ-1", "t", time.Now())

	for i, args := range [][]string{{"issue", "show", "PROJ-1"}, {"issue", "show", "PROJ-1"}, {"issue", "show", "PROJ-1", "--fresh"}} {
		stdout, _, code := executePr(t, args)
		if code != 0 {
			t.Fatalf("call %d exit = %d; stdout=%s", i, code, stdout)
		}
		var resp scriptout.Response
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		from, _, _ := servedFields(t, &resp)
		if want := []string{"origin", "cache", "origin"}[i]; from != want {
			t.Errorf("call %d served_from = %q, want %q", i, from, want)
		}
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("backend show calls = %d, want 2 (the middle read is a cache hit)", n)
	}
	c, err := loadCache(CacheKey{Type: "issue", Backend: b.name})
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if e := c.Entries["PROJ-1"]; e.level() != CacheLevelDetail {
		t.Fatalf("issue cache entry level = %q, want detail", e.Level)
	}
}

func TestIssueShow_UnavailableFallsBackToTheStaleDetailEntry(t *testing.T) {
	b := issueEnv(t, "backend-issue-stale", "  cache_read_ttl: 0\n")
	b.setEntity(t, "PROJ-1", "t", time.Now().Add(-30*time.Minute))
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	ctx := context.Background()
	if _, err := dispatchShow(ctx, reg, "issue", "PROJ-1", "", false); err != nil {
		t.Fatalf("first show: %v", err)
	}
	b.set(t, "show-error", "unavailable")
	resp, err := dispatchShow(ctx, reg, "issue", "PROJ-1", "", true)
	if err != nil {
		t.Fatalf("fallback show: %v", err)
	}
	if from, _, stale := servedFields(t, resp); from != "cache" || !stale {
		t.Fatalf("fallback served_from=%q stale=%t, want cache/true", from, stale)
	}
}

func TestIssueShow_TypeOptOutKeepsTheOriginOnly(t *testing.T) {
	b := issueEnv(t, "backend-issue-optout", "  cache_disabled_types: issue\n")
	b.setEntity(t, "PROJ-1", "t", time.Now())
	for i := 0; i < 2; i++ {
		if _, _, code := executePr(t, []string{"issue", "show", "PROJ-1"}); code != 0 {
			t.Fatalf("call %d exit = %d", i, code)
		}
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("backend show calls = %d, want 2 (an opted-out type is never read through)", n)
	}
	if dir, err := cacheDir(); err == nil {
		if _, statErr := os.Stat(filepath.Join(dir, "issue__"+b.name+".json")); statErr == nil {
			t.Fatal("an opted-out issue type was written to the cache")
		}
	}
}

func TestIssueChangesRefresher_MembershipThenOnlyNewOrAgedThenConfirmedRemoval(t *testing.T) {
	b := issueEnv(t, "backend-issue-rf", "  cache_refresh_after: 2m\n")
	now := time.Now()
	for _, id := range []string{"PROJ-1", "PROJ-2"} {
		b.setEntity(t, id, "t", now)
	}
	b.setMembership(t, "PROJ-1", "PROJ-2")

	w := runIssueChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 2 {
		t.Fatalf("first pass kinds = %v, want 2 added: %+v", kinds, w.Changes)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("first pass shows = %d, want 2", n)
	}
	c, err := loadCache(CacheKey{Type: "issue", Backend: b.name})
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	for _, id := range []string{"PROJ-1", "PROJ-2"} {
		if e := c.Entries[id]; e.level() != CacheLevelDetail {
			t.Fatalf("cache entry %s level = %q, want detail", id, e.Level)
		}
	}

	// A second pass over a stable, young membership asks the origin for the
	// membership only: no show, no change.
	w = runIssueChanges(t)
	if len(w.Changes) != 0 || b.calls(t, "show") != 2 {
		t.Fatalf("second pass changes=%+v shows=%d, want none and still 2", w.Changes, b.calls(t, "show"))
	}

	// PROJ-2 leaves the query (a transition out of the JQL set): the removal is
	// confirmed by one read, and the removed row carries what that read returned.
	b.setMembership(t, "PROJ-1")
	b.set(t, "show-PROJ-2.json", `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"PROJ-2","title":"t","state":"Done","as_of":"`+now.UTC().Format(time.RFC3339)+`","stale":false}}`)
	w = runIssueChanges(t)
	if len(w.Changes) != 1 || w.Changes[0].Change != ChangeRemoved {
		t.Fatalf("third pass changes = %+v, want one removed", w.Changes)
	}
	var e struct{ State string }
	if err := json.Unmarshal(w.Changes[0].Entity, &e); err != nil || e.State != "Done" {
		t.Fatalf("removed entity = %s, want the confirmed Done content", w.Changes[0].Entity)
	}
	if n := b.calls(t, "show"); n != 3 {
		t.Fatalf("shows = %d, want 3 (one confirming read)", n)
	}
}
