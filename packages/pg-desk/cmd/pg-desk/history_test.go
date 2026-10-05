package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // same driver internal/store registers

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// runHistoryCmd runs a fresh `history` command for entityType with args.
func runHistoryCmd(t *testing.T, entityType string, args ...string) (stdout string, err error) {
	t.Helper()
	c := newHistoryCmd(entityType)
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&bytes.Buffer{})
	c.SetArgs(args)
	c.SilenceUsage = true
	err = c.ExecuteContext(context.Background())
	return buf.String(), err
}

// seedHistory writes entity (o/r, entityType, id) with n change records and
// returns an open-fresh func for it. The store is cut over to the new schema.
func seedHistory(t *testing.T, entityType, id, facts string, n int) func() (*store.Store, error) {
	t.Helper()
	_, open := seedHistoryAt(t, entityType, id, facts, n)
	return open
}

// seedHistoryAt is seedHistory that also returns the store file's path.
func seedHistoryAt(t *testing.T, entityType, id, facts string, n int) (string, func() (*store.Store, error)) {
	t.Helper()
	path := storeAtVersion(t, "new")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var version int64
	for i := 1; i <= n; i++ {
		kinds := []string{"head_changed", "feedback_changed"}
		origin := "pg-connector"
		if i == 1 {
			kinds, origin = []string{"reconcile"}, "sweep"
		}
		version, err = s.WriteEntityWithLog(
			store.Entity{Repo: "o/r", EntityType: entityType, EntityID: id, Facts: facts, AsOf: "2026-09-29T00:00:00Z"},
			version, kinds, origin, fmt.Sprintf("2026-09-29T14:00:%02dZ", i),
		)
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	return path, func() (*store.Store, error) { return store.Open(path) }
}

func TestHistoryIsRegisteredUnderEveryTypeGroup(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		c, _, err := rootCmd.Find([]string{typ, "history"})
		if err != nil || c.Name() != "history" || c.Parent() != typeGroup(typ) {
			t.Errorf("%s history not registered: %v, %v", typ, c, err)
		}
	}
}

func TestHistoryNewestFirstTextForm(t *testing.T) {
	open := seedHistory(t, "pr", "o/r#5", `{"pr_show":{"title":"Add retry"}}`, 3)
	withOpenSeams(t, openTestConfig("o/r"), open)

	out, err := runHistoryCmd(t, "pr", "5")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := []string{
		"3  2026-09-29T14:00:03Z  head_changed, feedback_changed  origin=pg-connector",
		"2  2026-09-29T14:00:02Z  head_changed, feedback_changed  origin=pg-connector",
		"1  2026-09-29T14:00:01Z  reconcile  origin=sweep",
	}
	// seq values are store-assigned; compare everything after the seq.
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), out)
	}
	prev := int64(1 << 62)
	for i, line := range lines {
		var seq int64
		if _, err := fmt.Sscanf(line, "%d", &seq); err != nil {
			t.Fatalf("line %d: %v: %q", i, err, line)
		}
		if seq >= prev {
			t.Errorf("line %d: seq %d is not older than the previous %d (not newest first)", i, seq, prev)
		}
		prev = seq
		rest := strings.TrimPrefix(line, fmt.Sprintf("%d", seq))
		if wantRest := strings.TrimPrefix(want[i], fmt.Sprintf("%d", 3-i)); rest != wantRest {
			t.Errorf("line %d = %q, want suffix %q", i, line, wantRest)
		}
	}
}

func TestHistoryJSONForm(t *testing.T) {
	open := seedHistory(t, "pr", "o/r#5", `{"pr_show":{"title":"Add retry"}}`, 2)
	withOpenSeams(t, openTestConfig("o/r"), open)

	out, err := runHistoryCmd(t, "pr", "o/r#5", "--json")
	if err != nil {
		t.Fatalf("history --json: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(top) != 1 || top["records"] == nil {
		t.Fatalf("top-level keys must be exactly {records}, got %s", out)
	}
	var recs []struct {
		Seq     int64    `json:"seq"`
		Type    string   `json:"type"`
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Version int64    `json:"version"`
		Kinds   []string `json:"kinds"`
		Origin  string   `json:"origin"`
		At      string   `json:"at"`
	}
	if err := json.Unmarshal(top["records"], &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Seq <= recs[1].Seq {
		t.Fatalf("records not newest first: %+v", recs)
	}
	r := recs[0]
	if r.Type != "pr" || r.ID != "o/r#5" || r.Title != "Add retry" || r.Version != 2 ||
		strings.Join(r.Kinds, ",") != "head_changed,feedback_changed" || r.Origin != "pg-connector" || r.At != "2026-09-29T14:00:02Z" {
		t.Errorf("record = %+v", r)
	}
}

func TestHistoryJSONOutputEnvVar(t *testing.T) {
	open := seedHistory(t, "pr", "o/r#5", `{}`, 1)
	withOpenSeams(t, openTestConfig("o/r"), open)
	t.Setenv("PG_DESK_OUTPUT", "json")
	out, err := runHistoryCmd(t, "pr", "5")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("PG_DESK_OUTPUT=json did not select JSON: %v\n%s", err, out)
	}
}

func TestHistoryNoRecordsIsEmptyList(t *testing.T) {
	open := seedHistory(t, "pr", "o/r#5", `{}`, 1)
	withOpenSeams(t, openTestConfig("o/r"), open)
	out, err := runHistoryCmd(t, "pr", "99", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Records []any `json:"records"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Records == nil || len(got.Records) != 0 {
		t.Errorf("want {\"records\": []}, got %s (%v)", out, err)
	}
}

// TestHistoryLimitPaging covers section 12 "change log and cursors":
// --limit returns the newest N, 0 means all, and an over-large N is no cap.
func TestHistoryLimitPaging(t *testing.T) {
	open := seedHistory(t, "pr", "o/r#5", `{}`, 5)
	withOpenSeams(t, openTestConfig("o/r"), open)

	count := func(args ...string) []string {
		out, err := runHistoryCmd(t, "pr", append([]string{"5"}, args...)...)
		if err != nil {
			t.Fatalf("history %v: %v", args, err)
		}
		if out == "" {
			return nil
		}
		return strings.Split(strings.TrimRight(out, "\n"), "\n")
	}
	all := count()
	if len(all) != 5 {
		t.Fatalf("no --limit returned %d records, want 5", len(all))
	}
	for _, n := range []int{1, 2, 4} {
		got := count("--limit", fmt.Sprint(n))
		if len(got) != n {
			t.Errorf("--limit %d returned %d records", n, len(got))
			continue
		}
		for i := range got {
			if got[i] != all[i] {
				t.Errorf("--limit %d line %d = %q, want the newest-first prefix %q", n, i, got[i], all[i])
			}
		}
	}
	if got := count("--limit", "50"); len(got) != 5 {
		t.Errorf("--limit 50 returned %d records, want all 5", len(got))
	}
	if _, err := runHistoryCmd(t, "pr", "5", "--limit", "-1"); err == nil {
		t.Errorf("negative --limit accepted")
	}
}

func TestHistoryOtherTypesUseVerbatimIDAndTitle(t *testing.T) {
	open := seedHistory(t, "thread", "C1/1700000000.000100", `{"thread_show":{"text":"\nfirst line\nmore"}}`, 1)
	withOpenSeams(t, openTestConfig("o/r"), open)
	out, err := runHistoryCmd(t, "thread", "C1/1700000000.000100", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"title": "first line"`) || !strings.Contains(out, `"id": "C1/1700000000.000100"`) {
		t.Errorf("thread history output:\n%s", out)
	}
}

func TestHistoryTitleEmptyWhenEntityRowUnavailable(t *testing.T) {
	path, open := seedHistoryAt(t, "pr", "o/r#5", `{"pr_show":{"title":"x"}}`, 1)
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM entity WHERE entity_id = 'o/r#5'`); err != nil {
		t.Fatalf("delete entity row: %v", err)
	}
	_ = raw.Close()
	withOpenSeams(t, openTestConfig("o/r"), open)

	out, err := runHistoryCmd(t, "pr", "5", "--json")
	if err != nil {
		t.Fatalf("history with the entity row gone: %v", err)
	}
	if !strings.Contains(out, `"title": ""`) || !strings.Contains(out, `"id": "o/r#5"`) {
		t.Errorf("want a record with an empty title, got:\n%s", out)
	}
}

func TestHistoryRefusesOldSchemaStore(t *testing.T) {
	path := storeAtVersion(t, "old")
	withOpenSeams(t, openTestConfig("o/r"), func() (*store.Store, error) { return store.Open(path) })
	out, err := runHistoryCmd(t, "pr", "1")
	if !errors.Is(err, store.ErrOldSchema) {
		t.Fatalf("err = %v, want store.ErrOldSchema", err)
	}
	if !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
		t.Errorf("refusal text %q lacks pg-desk migrate --cutover", err)
	}
	if out != "" {
		t.Errorf("refusal printed to stdout: %q", out)
	}
	if exitCodeFor(err) != 1 {
		t.Errorf("old-schema refusal exit code = %d, want 1", exitCodeFor(err))
	}
}

func TestHistoryRequiresExactlyOneID(t *testing.T) {
	if _, err := runHistoryCmd(t, "pr"); err == nil {
		t.Errorf("history with no id accepted")
	}
}
