package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/httpapi"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TestPluginAndDashboardPayloadReturnTheSameGroups is INV-ATTNEVAL-2 made
// executable: for one store, the groups of GET /api/v1/dashboard's `attention`
// field and the groups a consumer reconstructs from the plugin's
// `list_attention` feed are the same, in the same order, with the same items
// and labels, because both call attention.Evaluate.
func TestPluginAndDashboardPayloadReturnTheSameGroups(t *testing.T) {
	path := standardFixture(t)

	feed, err := testProvider(path).ListAttention(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: fixtureRepo}}}
	payload, err := httpapi.BuildPayload(st, cfg, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if payload.AttentionError != "" {
		t.Fatalf("attention_error = %q, want none", payload.AttentionError)
	}
	if len(payload.Attention) == 0 {
		t.Fatal("fixture should raise attention groups")
	}

	// Compare what the consumer sees on the wire, so a JSON tag drift is
	// caught as well as a logic drift.
	var wire struct {
		Attention []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
			Items []struct {
				Type     string `json:"type"`
				ID       string `json:"id"`
				Summary  string `json:"summary"`
				Severity string `json:"severity"`
			} `json:"items"`
		} `json:"attention"`
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}

	type sig struct {
		Key, Label, Type, ID, Summary, Severity string
	}
	var fromPayload []sig
	for _, g := range wire.Attention {
		for _, it := range g.Items {
			fromPayload = append(fromPayload, sig{g.Key, g.Label, it.Type, it.ID, it.Summary, it.Severity})
		}
	}
	var fromPlugin []sig
	for _, it := range feed {
		if it.Group == nil {
			t.Fatalf("plugin item %+v has no group", it)
		}
		fromPlugin = append(fromPlugin, sig{it.Group.Key, it.Group.Label, it.Type, it.ID, it.Summary, string(it.Severity)})
	}
	if !reflect.DeepEqual(fromPayload, fromPlugin) {
		t.Errorf("dashboard groups = %+v\nplugin feed      = %+v\nwant the same", fromPayload, fromPlugin)
	}

	// And the consumer-side regrouping of the plugin feed reproduces the
	// payload's groups, key for key and id for id.
	assertFeedRoundTripsToEvaluatorOrder(t, feed, payload.Attention)
}
