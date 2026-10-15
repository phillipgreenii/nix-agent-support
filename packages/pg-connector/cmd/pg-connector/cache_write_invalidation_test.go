package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Invalidate on write (INV-CACHE-10, operator ruling on pg2-tseur): a write
// verb that the backend accepted drops the written entity's cached copy, so
// the next show reads the backend instead of serving the pre-write entity
// until read_ttl expires. These tests drive the real CLI against the scripted
// backend (cache_policy_test.go) and the real on-disk cache.

// showTitle runs `<entityType> show <id>` through the CLI and returns the
// entity's title and served_from.
func showTitle(t *testing.T, entityType, id string) (title, servedFrom string) {
	t.Helper()
	stdout, stderr, code := executePr(t, []string{entityType, "show", id})
	if code != 0 {
		t.Fatalf("%s show %s exit = %d; stdout=%s stderr=%s", entityType, id, code, stdout, stderr)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode show output: %v (%s)", err, stdout)
	}
	var e struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(resp.Result, &e); err != nil {
		t.Fatalf("decode entity: %v", err)
	}
	from, _, _ := servedFields(t, &resp)
	return e.Title, from
}

type writeCase struct {
	name       string
	entityType string
	args       []string
	stdin      string
}

// writeCases is every pr and issue write verb, with the CLI args that
// perform it on id.
func writeCases(id string) []writeCase {
	return []writeCase{
		{"issue comment", "issue", []string{"issue", "comment", id, "--body", "hi"}, ""},
		{"issue transition", "issue", []string{"issue", "transition", id, "--state", "done"}, ""},
		{"issue update", "issue", []string{"issue", "update", id, "--title", "after"}, ""},
		{"issue close", "issue", []string{"issue", "close", id, "--reason", "done"}, ""},
		{"pr review submit", "pr", []string{"pr", "review", "submit", id}, `{"head_sha":"abc","body":"b","comments":[]}`},
	}
}

// writeEnv points the umbrella at a scripted backend registered for both pr
// and issue, whose entity id reads "before" until a successful write turns it
// into "after".
func writeEnv(t *testing.T, name, id string) *scriptedBackend {
	t.Helper()
	b := newScriptedBackend(t, name)
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - "+name+"\n  issue:\n    - "+name+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv(envIssueBeadsDir, "")
	b.setEntity(t, id, "before", time.Now())
	b.set(t, "after-"+id+".json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"id":%q,"title":"after","state":"open","as_of":%q,"stale":false}}`, id, time.Now().UTC().Format(time.RFC3339)))
	return b
}

func TestWriteVerbs_InvalidateTheCachedEntity(t *testing.T) {
	for _, tc := range writeCases("W-1") {
		t.Run(tc.name, func(t *testing.T) {
			b := writeEnv(t, "backend-inv-"+strings.ReplaceAll(tc.name, " ", "-"), "W-1")

			// Prime the cache, then prove the pre-write entity is served from it.
			if title, from := showTitle(t, tc.entityType, "W-1"); title != "before" || from != "origin" {
				t.Fatalf("first show = %q from %q, want before from origin", title, from)
			}
			if title, from := showTitle(t, tc.entityType, "W-1"); title != "before" || from != "cache" {
				t.Fatalf("second show = %q from %q, want before from cache", title, from)
			}

			stdout, stderr, code := executePrWithStdin(t, tc.stdin, tc.args)
			if code != 0 {
				t.Fatalf("%s exit = %d; stdout=%s stderr=%s", tc.name, code, stdout, stderr)
			}

			// Read-after-write: no --fresh, still inside read_ttl.
			if title, from := showTitle(t, tc.entityType, "W-1"); title != "after" || from != "origin" {
				t.Fatalf("show after %s = %q from %q, want after from origin", tc.name, title, from)
			}
			if n := b.calls(t, "show"); n != 2 {
				t.Fatalf("backend show calls = %d, want 2 (prime + the post-write read)", n)
			}
			// The post-write read re-primed the cache with the new entity.
			if title, from := showTitle(t, tc.entityType, "W-1"); title != "after" || from != "cache" {
				t.Fatalf("show after the re-prime = %q from %q, want after from cache", title, from)
			}
		})
	}
}

func TestWriteVerbs_FailedWriteLeavesTheCacheAlone(t *testing.T) {
	for _, tc := range writeCases("W-2") {
		t.Run(tc.name, func(t *testing.T) {
			b := writeEnv(t, "backend-fail-"+strings.ReplaceAll(tc.name, " ", "-"), "W-2")
			b.set(t, "write-error", "invalid_argument")

			if _, from := showTitle(t, tc.entityType, "W-2"); from != "origin" {
				t.Fatalf("first show from %q, want origin", from)
			}
			if _, _, code := executePrWithStdin(t, tc.stdin, tc.args); code == 0 {
				t.Fatalf("%s exit = 0, want the scripted failure to surface", tc.name)
			}
			// The entry survived: the next show is a cache hit on the pre-write entity.
			if title, from := showTitle(t, tc.entityType, "W-2"); title != "before" || from != "cache" {
				t.Fatalf("show after a failed %s = %q from %q, want before from cache", tc.name, title, from)
			}
			if n := b.calls(t, "show"); n != 1 {
				t.Fatalf("backend show calls = %d, want 1 (a failed write must not force a re-read)", n)
			}
		})
	}
}

// A write to one entity drops only that entity, and drops it whatever level
// it is held at: the summary a list wrote and the detail a show wrote share
// one entry per id.
func TestInvalidateEntity_DropsOnlyThatIdAtAnyLevel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	reg := registryFor(t, "backend-inv-unit", "")
	key := CacheKey{Type: "issue", Backend: "backend-inv-unit"}
	if err := ensureCacheDirExists(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c := newEmptyCache()
	c.PutLevel("A", json.RawMessage(`{"id":"A"}`), now, now, CacheLevelDetail)
	c.PutLevel("B", json.RawMessage(`{"id":"B"}`), now, now, CacheLevelSummary)
	c.PutLevel("C", json.RawMessage(`{"id":"C"}`), now, now, CacheLevelDetail)
	if err := saveCache(key, c); err != nil {
		t.Fatal(err)
	}

	invalidateEntity(reg, "issue", "A", "")
	invalidateEntity(reg, "issue", "B", "")
	invalidateEntity(reg, "issue", "never-cached", "")

	got, err := loadCache(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Entries["A"]; ok {
		t.Errorf("detail entry A survived invalidation")
	}
	if _, ok := got.Entries["B"]; ok {
		t.Errorf("summary entry B survived invalidation")
	}
	if _, ok := got.Entries["C"]; !ok {
		t.Errorf("untouched entry C was dropped")
	}
	for id, e := range got.Entries {
		if e.RemovedAt != nil {
			t.Errorf("entry %s is a tombstone; invalidation must delete, not tombstone", id)
		}
	}
}

// An invalidation with nothing cached must not write a cache file.
func TestInvalidateEntity_NothingCachedWritesNothing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	reg := registryFor(t, "backend-inv-empty", "")
	before := cacheSaves.Load()
	invalidateEntity(reg, "pr", "pr-9", "")
	if got := cacheSaves.Load(); got != before {
		t.Fatalf("saveCache calls = %d, want %d (no entry was held)", got, before)
	}
}
