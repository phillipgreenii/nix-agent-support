package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func runConsumer(t *testing.T, entityType string, args ...string) (string, error) {
	t.Helper()
	return runGroupCmd(t, newConsumerCmd, entityType, args...)
}

var consumerTestNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func withConsumerClock(t *testing.T) {
	t.Helper()
	orig := consumerNow
	t.Cleanup(func() { consumerNow = orig })
	consumerNow = func() time.Time { return consumerTestNow }
}

func TestConsumerVerbsRegisteredUnderEveryTypeGroup(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		for _, verb := range []string{"list", "forget"} {
			c, _, err := rootCmd.Find([]string{typ, "consumer", verb})
			if err != nil || c.Name() != verb || c.Parent().Parent() != typeGroup(typ) {
				t.Errorf("%s consumer %s not registered: %v, %v", typ, verb, c, err)
			}
		}
	}
}

func TestConsumerListShowsCursorSeenAtAndStaleness(t *testing.T) {
	withConsumerClock(t)
	open, seed := seedLinkStore(t)
	withOpenSeams(t, linkTestConfig(), open)
	if err := seed.RegisterConsumer("fresh", "pr", consumerTestNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := seed.RegisterConsumer("old", "pr", consumerTestNow.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := seed.RegisterConsumer("other-type", "issue", consumerTestNow); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ReadChanges("fresh", "pr", 0, consumerTestNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	out, err := runConsumer(t, "pr", "list")
	if err != nil {
		t.Fatal(err)
	}
	want := "fresh  cursor=0  seen_at=2026-10-01T11:00:00Z  fresh\n" +
		"old  cursor=0  seen_at=2026-09-23T12:00:00Z  stale\n"
	if out != want {
		t.Errorf("list =\n%s\nwant\n%s", out, want)
	}
}

func TestConsumerListUsesConfiguredStaleAfter(t *testing.T) {
	withConsumerClock(t)
	open, seed := seedLinkStore(t)
	cfg := linkTestConfig()
	cfg.ConsumerStaleAfterRaw = "1h"
	withOpenSeams(t, cfg, open)
	if err := seed.RegisterConsumer("c", "pr", consumerTestNow.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	out, err := runConsumer(t, "pr", "list")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(out), "stale") {
		t.Errorf("a consumer unseen for 2h with consumer_stale_after 1h must be stale: %q, %v", out, err)
	}
}

func TestConsumerListJSONAndEmpty(t *testing.T) {
	withConsumerClock(t)
	open, seed := seedLinkStore(t)
	withOpenSeams(t, linkTestConfig(), open)

	out, err := runConsumer(t, "pr", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var empty struct {
		Consumers []any `json:"consumers"`
	}
	if err := json.Unmarshal([]byte(out), &empty); err != nil || empty.Consumers == nil || len(empty.Consumers) != 0 {
		t.Errorf("want {\"consumers\": []}, got %s (%v)", out, err)
	}
	if text, err := runConsumer(t, "pr", "list"); err != nil || text != "" {
		t.Errorf("empty text list = %q, %v", text, err)
	}

	if err := seed.RegisterConsumer("a", "pr", consumerTestNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	out, err = runConsumer(t, "pr", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Consumers []consumerView `json:"consumers"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Consumers) != 1 {
		t.Fatalf("json = %s (%v)", out, err)
	}
	if c := got.Consumers[0]; c.Name != "a" || c.Type != "pr" || c.Cursor != 0 || c.SeenAt != "2026-10-01T11:00:00Z" || c.Stale {
		t.Errorf("consumer = %+v", c)
	}
}

func TestConsumerForgetDeletesOnlyThatNameAndType(t *testing.T) {
	open, seed := seedLinkStore(t)
	withOpenSeams(t, linkTestConfig(), open)
	for _, c := range [][2]string{{"a", "pr"}, {"b", "pr"}, {"a", "issue"}} {
		if err := seed.RegisterConsumer(c[0], c[1], consumerTestNow); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runConsumer(t, "pr", "forget", "a")
	if err != nil || !strings.Contains(out, "forgot pr consumer a") {
		t.Fatalf("forget = %q, %v", out, err)
	}
	var left []string
	rows, _ := seed.ListConsumers()
	for _, c := range rows {
		left = append(left, c.Type+"/"+c.Name)
	}
	if !equalStrings(left, []string{"issue/a", "pr/b"}) {
		t.Errorf("consumers left = %v", left)
	}
}

func TestConsumerForgetAbsentIsNotAnError(t *testing.T) {
	open, _ := seedLinkStore(t)
	withOpenSeams(t, linkTestConfig(), open)
	out, err := runConsumer(t, "pr", "forget", "ghost")
	if err != nil || !strings.Contains(out, "nothing to forget") {
		t.Errorf("forget absent = %q, %v", out, err)
	}
}

// TestConsumerForgetStopsPruneWaiting: a registered consumer that has not
// read holds back change_log pruning; forgetting it releases the rows.
func TestConsumerForgetStopsPruneWaiting(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}) // one old "created" record
	withOpenSeams(t, linkTestConfig(), open)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) // far past the 14d retention
	if err := seed.RegisterConsumer("slow", "pr", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	if n, err := seed.PruneChangeLog(now, 0, 0); err != nil || n != 0 {
		t.Fatalf("prune with a waiting consumer = %d, %v; want 0 (it holds the horizon)", n, err)
	}
	if _, err := runConsumer(t, "pr", "forget", "slow"); err != nil {
		t.Fatal(err)
	}
	if n, err := seed.PruneChangeLog(now, 0, 0); err != nil || n != 1 {
		t.Fatalf("prune after forget = %d, %v; want 1 (nothing waits any more)", n, err)
	}
}

func TestConsumerVerbsRefuseOldSchemaStore(t *testing.T) {
	path := storeAtVersion(t, "old")
	withOpenSeams(t, linkTestConfig(), func() (*store.Store, error) { return store.Open(path) })
	for _, args := range [][]string{{"list"}, {"forget", "x"}} {
		t.Run(args[0], func(t *testing.T) {
			out, err := runConsumer(t, "pr", args...)
			if !errors.Is(err, store.ErrOldSchema) {
				t.Fatalf("err = %v, want store.ErrOldSchema", err)
			}
			if !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
				t.Errorf("refusal text %q lacks pg-desk migrate --cutover", err)
			}
			if out != "" {
				t.Errorf("refusal printed to stdout: %q", out)
			}
		})
	}
}

func TestConsumerForgetRequiresName(t *testing.T) {
	if _, err := runConsumer(t, "pr", "forget"); err == nil {
		t.Errorf("forget with no name accepted")
	}
}
