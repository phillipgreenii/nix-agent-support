package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/attention"
)

// pgConnectorEnvVar names the real umbrella binary the end-to-end test drives.
// The pg-connector umbrella is package main in another module and cannot be
// imported or built from this one (its third-party dependencies are not in
// pg-desk's module graph), so the nix gate (flake.nix's pg-desk-go-tests)
// supplies the built binary here. Unset, the end-to-end tests skip with that
// reason; every other test in this package runs unconditionally.
const pgConnectorEnvVar = "PG_DESK_E2E_PG_CONNECTOR"

func umbrellaBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv(pgConnectorEnvVar)
	if bin == "" {
		t.Skipf("%s is not set; the nix go-tests check supplies the real pg-connector binary", pgConnectorEnvVar)
	}
	return bin
}

// umbrellaOutcome is the part of `pg-connector attention list`'s JSON this
// test reads.
type umbrellaOutcome struct {
	Sources []struct {
		Source string `json:"source"`
		Status string `json:"status"`
		Count  int    `json:"count"`
		Reason string `json:"reason"`
	} `json:"sources"`
	Items []struct {
		schema.AttentionItem
		Via []string `json:"via"`
	} `json:"items"`
}

// runUmbrellaAttentionList registers the plugin process (see buildPlugin) by bare name in
// a temporary pg-connector config and runs `pg-connector attention list`
// against it, with the plugin's own config and store pointed at the fixture.
func runUmbrellaAttentionList(t *testing.T, umbrella, storePath string) (umbrellaOutcome, int) {
	t.Helper()
	binDir := t.TempDir()
	buildPlugin(t, binDir)

	cfgDir := t.TempDir()
	connectorCfg := filepath.Join(cfgDir, "pg-connector.yaml")
	if err := os.WriteFile(connectorCfg, []byte("attention:\n  sources:\n    - pg-desk-attention\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, umbrella, "attention", "list")
	// The umbrella resolves the source by bare name on PATH, exactly as in
	// production, so the plugin's directory leads PATH.
	cmd.Env = append([]string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PG_PR_CONFIG=" + connectorCfg,
	}, writePluginEnv(t, storePath)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run pg-connector: %v", err)
		}
		code = ee.ExitCode()
	}
	var out umbrellaOutcome
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("pg-connector attention list printed non-JSON: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	return out, code
}

// TestE2E_PgConnectorAttentionList drives the plugin through the real
// umbrella over a fixture store: the source is queried and succeeds, one item
// per entity arrives with the group intact (attention schema version 3
// passes it through unread), and the merged feed order round-trips to the
// evaluator's order.
func TestE2E_PgConnectorAttentionList(t *testing.T) {
	umbrella := umbrellaBinary(t)
	path := standardFixture(t)
	out, code := runUmbrellaAttentionList(t, umbrella, path)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (all sources healthy); outcome=%+v", code, out)
	}
	want := evaluate(t, path)

	if len(out.Sources) != 1 || out.Sources[0].Source != "pg-desk-attention" || out.Sources[0].Status != "succeeded" || out.Sources[0].Count != len(want.Items) {
		t.Fatalf("sources = %+v, want pg-desk-attention succeeded with %d items", out.Sources, len(want.Items))
	}
	if len(out.Items) != len(want.Items) {
		t.Fatalf("items = %d, want %d (one per entity)", len(out.Items), len(want.Items))
	}
	for _, it := range out.Items {
		if it.Type != "pr" || len(it.Via) != 1 || it.Via[0] != "pg-desk-attention" {
			t.Errorf("item %+v: want type pr, via [pg-desk-attention]", it)
		}
		if it.Group == nil || it.Group.Key == "" || it.Group.Label == "" {
			t.Errorf("item %+v lost its group on the way through the umbrella", it)
		}
	}

	feed := make([]schema.AttentionItem, len(out.Items))
	for i, it := range out.Items {
		feed[i] = it.AttentionItem
	}
	assertFeedRoundTripsToEvaluatorOrder(t, feed, want.Groups)
}

// TestE2E_UnreadableStoreDegradesTheSource proves the failure path through the
// real umbrella: a store that cannot be read makes the source degraded, never
// a successful empty list.
func TestE2E_UnreadableStoreDegradesTheSource(t *testing.T) {
	umbrella := umbrellaBinary(t)
	out, code := runUmbrellaAttentionList(t, umbrella, filepath.Join(t.TempDir(), "never-created", "pg-desk", "store.db"))
	if len(out.Sources) != 1 || out.Sources[0].Status != "degraded" || out.Sources[0].Count != 0 {
		t.Fatalf("sources = %+v, want pg-desk-attention degraded", out.Sources)
	}
	if code == 0 {
		t.Error("exit = 0, want non-zero when the only source is degraded")
	}
	if len(out.Items) != 0 {
		t.Errorf("items = %+v, want none", out.Items)
	}
}

// feedGroup is one group as a consumer reconstructs it from the merged feed.
type feedGroup struct {
	Key string
	IDs []string
}

// regroupFeed is the menu bar's rule (docs/behavior/pg-desk/attention.md,
// "Grouping and order"): group by group.key in order of first appearance in
// the feed, and keep feed order inside each group. Items with no group are
// singletons keyed by their own entity.
func regroupFeed(feed []schema.AttentionItem) []feedGroup {
	var groups []feedGroup
	index := map[string]int{}
	for _, it := range feed {
		key := it.Type + ":" + it.ID
		if it.Group != nil {
			key = it.Group.Key
		}
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, feedGroup{Key: key})
		}
		groups[i].IDs = append(groups[i].IDs, it.ID)
	}
	return groups
}

// assertFeedRoundTripsToEvaluatorOrder asserts the consumer-side regrouping of
// the merged feed reproduces the evaluator's own groups: the same groups, in
// the same order, with the same items in the same order inside each.
func assertFeedRoundTripsToEvaluatorOrder(t *testing.T, feed []schema.AttentionItem, groups []attention.Group) {
	t.Helper()
	want := make([]feedGroup, 0, len(groups))
	for _, g := range groups {
		fg := feedGroup{Key: g.Key}
		for _, it := range g.Items {
			fg.IDs = append(fg.IDs, it.ID)
		}
		want = append(want, fg)
	}
	if got := regroupFeed(feed); !reflect.DeepEqual(got, want) {
		t.Errorf("regrouped merged feed = %+v, want the evaluator's groups %+v", got, want)
	}
}

// TestRegroupFeed_StableSeverityResortKeepsGroupOrder documents why the
// round-trip holds even though the umbrella re-sorts the merged feed by
// severity: with the evaluator's order [A:high, A:low, B:high] the stable sort
// yields [A:high, B:high, A:low], and regrouping by first appearance still
// gives the groups A then B with A's items in evaluator order.
func TestRegroupFeed_StableSeverityResortKeepsGroupOrder(t *testing.T) {
	a := &schema.AttentionGroup{Key: "issue:ABC-1", Label: "ABC-1"}
	b := &schema.AttentionGroup{Key: "issue:ABC-2", Label: "ABC-2"}
	resorted := []schema.AttentionItem{
		{Type: "pr", ID: "a1", Group: a, Severity: schema.SeverityHigh},
		{Type: "pr", ID: "b1", Group: b, Severity: schema.SeverityHigh},
		{Type: "pr", ID: "a2", Group: a, Severity: schema.SeverityLow},
		{Type: "issue", ID: "other"}, // another source: no group, a singleton
	}
	want := []feedGroup{
		{Key: "issue:ABC-1", IDs: []string{"a1", "a2"}},
		{Key: "issue:ABC-2", IDs: []string{"b1"}},
		{Key: "issue:other", IDs: []string{"other"}},
	}
	if got := regroupFeed(resorted); !reflect.DeepEqual(got, want) {
		t.Errorf("regroupFeed = %+v, want %+v", got, want)
	}
}
