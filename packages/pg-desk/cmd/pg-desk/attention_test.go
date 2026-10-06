package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const attnRepo = "acme/api"

// attnPR is one stored PR for the verb tests.
type attnPR struct {
	n         int
	ownership string
	panel     string
	facts     string
}

func (p attnPR) id() string { return fmt.Sprintf("%s#%d", attnRepo, p.n) }

func attnFixture(t *testing.T, cutover bool, prs ...attnPR) string {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "attention.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prs {
		facts := p.facts
		if facts == "" {
			facts = fmt.Sprintf(`{"pr_show":{"number":%d,"state":"open","author":"me","head_sha":"h1"}}`, p.n)
		}
		if err := st.UpsertEntity(store.Entity{Repo: attnRepo, EntityType: "pr", EntityID: p.id(), Facts: facts, AsOf: "2026-10-06T11:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		if p.panel == "none" {
			continue
		}
		if err := st.UpsertInterpretation(store.Interpretation{
			Repo: attnRepo, EntityType: "pr", EntityID: p.id(), Ownership: p.ownership, Panel: p.panel,
			Approvals: "{}", MatchReasons: "[]", Enrichment: "{}", Urgency: "{}", Dispositions: "[]", AsOf: "2026-10-06T11:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if cutover {
		if err := st.Cutover(); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func withAttentionSeams(t *testing.T, cfg *config.Config, open func() (*store.Store, error)) {
	t.Helper()
	origCfg, origOpen, origNow := deskConfigLoad, deskStoreOpenReadOnly, attentionNow
	t.Cleanup(func() { deskConfigLoad, deskStoreOpenReadOnly, attentionNow = origCfg, origOpen, origNow })
	deskConfigLoad = func(context.Context) (*config.Config, error) { return cfg, nil }
	deskStoreOpenReadOnly = open
	attentionNow = interpret.FixedClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
}

func attnConfig() *config.Config {
	return &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: attnRepo}}}
}

func openAttn(path string) func() (*store.Store, error) {
	return func() (*store.Store, error) { return store.OpenReadOnly(path) }
}

// runAttention runs `pg-desk attention <sub> [args]`, optionally with --json.
func runAttention(t *testing.T, sub string, jsonFlag bool, args ...string) (string, error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"attention", sub})
	if ferr != nil || c.Name() != sub {
		t.Fatalf("rootCmd has no attention %s subcommand: %v", sub, ferr)
	}
	_ = c.Flags().Set("json", fmt.Sprint(jsonFlag))
	t.Cleanup(func() { _ = c.Flags().Set("json", "false") })
	var out bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&out)
	err := c.RunE(c, args)
	return out.String(), err
}

func decodeAttnList(t *testing.T, out string) attention.ListDocument {
	t.Helper()
	var d attention.ListDocument
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("not the list document: %v\n%s", err, out)
	}
	return d
}

func TestAttentionListJSON(t *testing.T) {
	failing := `{"pr_show":{"number":3,"state":"open","author":"me","head_sha":"h1"},"ci":{"runs":[{"id":"1","name":"build","status":"completed","conclusion":"failure","head_sha":"h1","attempt":1}]}}`
	path := attnFixture(
		t, true,
		attnPR{n: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe},
		attnPR{n: 2, ownership: "team", panel: interpret.PanelTeamAwaitingTeam},
		attnPR{n: 3, ownership: "mine", panel: interpret.PanelMineAwaitingMe, facts: failing},
	)
	withAttentionSeams(t, attnConfig(), openAttn(path))

	out, err := runAttention(t, "list", true)
	if err != nil {
		t.Fatal(err)
	}
	d := decodeAttnList(t, out)
	if d.SchemaVersion != 1 || d.Now != "2026-10-06T12:00:00Z" || d.Degraded {
		t.Errorf("document header = %+v, want schemaVersion 1, the fixed now, not degraded", d)
	}
	if len(d.Items) != 2 {
		t.Fatalf("items = %+v, want the two PRs that need the operator", d.Items)
	}
	if d.Items[0].ID != "acme/api#3" || d.Items[0].Severity != "high" || d.Items[0].Rule != "pr.own-ci-failing" || d.Items[0].Type != "pr" {
		t.Errorf("first item = %+v, want the failing own PR first (canonical order)", d.Items[0])
	}
	if d.Items[1].ID != "acme/api#1" || d.Items[1].Rule != "pr.review-requested" {
		t.Errorf("second item = %+v", d.Items[1])
	}
	if len(d.Groups) != 2 || d.Groups[0].Key != d.Items[0].Group || len(d.Groups[0].Items) != 1 {
		t.Errorf("groups = %+v, want one group per item in canonical order", d.Groups)
	}
	if strings.Contains(out, `"degraded"`) {
		t.Error("degraded is optional and must be absent on a migrated store")
	}

	again, err := runAttention(t, "list", true)
	if err != nil || again != out {
		t.Errorf("a fixed store and clock must give byte-identical output (INV-ATTNEVAL-1): err=%v", err)
	}
}

func TestAttentionListNothingNeedsTheOperatorExitsZero(t *testing.T) {
	path := attnFixture(t, true, attnPR{n: 1, ownership: "team", panel: interpret.PanelTeamAwaitingTeam})
	withAttentionSeams(t, attnConfig(), openAttn(path))

	out, err := runAttention(t, "list", true)
	if err != nil {
		t.Fatalf("exit must be 0 when nothing needs attention: %v", err)
	}
	if d := decodeAttnList(t, out); len(d.Items) != 0 || !strings.Contains(out, `"items": []`) || !strings.Contains(out, `"groups": []`) {
		t.Errorf("items and groups must be empty arrays, never null:\n%s", out)
	}
	text, err := runAttention(t, "list", false)
	if err != nil || strings.TrimSpace(text) != "nothing needs attention" {
		t.Errorf("text = %q err=%v", text, err)
	}
}

func TestAttentionListTextAndEnvJSON(t *testing.T) {
	path := attnFixture(t, true, attnPR{n: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	withAttentionSeams(t, attnConfig(), openAttn(path))

	text, err := runAttention(t, "list", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "medium pr:acme/api#1  review requested of me  [acme/api#1]\n"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}

	t.Setenv(outputEnvVar, "json")
	out, err := runAttention(t, "list", false)
	if err != nil {
		t.Fatal(err)
	}
	if d := decodeAttnList(t, out); len(d.Items) != 1 {
		t.Errorf("PG_DESK_OUTPUT=json must select JSON, got %s", out)
	}
}

func TestAttentionListDegradedOnUnmigratedStore(t *testing.T) {
	path := attnFixture(t, false, attnPR{n: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	withAttentionSeams(t, attnConfig(), openAttn(path))

	out, err := runAttention(t, "list", true)
	if err != nil {
		t.Fatal(err)
	}
	d := decodeAttnList(t, out)
	if !d.Degraded || len(d.Items) != 1 {
		t.Errorf("an unmigrated store must still evaluate and say degraded (INV-ATTNEVAL-5): %s", out)
	}
	text, err := runAttention(t, "list", false)
	if err != nil || !strings.Contains(text, "degraded") {
		t.Errorf("text output must mention degraded: %q err=%v", text, err)
	}
}

func TestAttentionVerbsErrors(t *testing.T) {
	path := attnFixture(t, true, attnPR{n: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})

	t.Run("unknown rule kind in config", func(t *testing.T) {
		cfg := attnConfig()
		cfg.Attention.Rules = map[string]config.AttentionRuleConfig{"pr.typo": {}}
		withAttentionSeams(t, cfg, openAttn(path))
		for _, sub := range []string{"list"} {
			if _, err := runAttention(t, sub, true); err == nil || !strings.Contains(err.Error(), "pr.typo") || exitCodeFor(err) != 1 {
				t.Errorf("%s: err = %v, want exit 1 naming the unknown kind", sub, err)
			}
		}
		if _, err := runAttention(t, "explain", true, "pr:acme/api#1"); err == nil {
			t.Error("explain must also fail on an unknown rule kind")
		}
	})
	t.Run("config cannot be loaded", func(t *testing.T) {
		withAttentionSeams(t, attnConfig(), openAttn(path))
		deskConfigLoad = func(context.Context) (*config.Config, error) { return nil, errors.New("bad config") }
		if _, err := runAttention(t, "list", true); err == nil || !strings.Contains(err.Error(), "bad config") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("store cannot be read", func(t *testing.T) {
		withAttentionSeams(t, attnConfig(), func() (*store.Store, error) { return nil, errors.New("no store") })
		if _, err := runAttention(t, "list", true); err == nil || !strings.Contains(err.Error(), "no store") {
			t.Errorf("err = %v, want an error, never an empty all-clear", err)
		}
	})
	t.Run("explain argument shape", func(t *testing.T) {
		withAttentionSeams(t, attnConfig(), openAttn(path))
		for _, bad := range []string{"pr", ":x", "pr:"} {
			if _, err := runAttention(t, "explain", true, bad); err == nil || !strings.Contains(err.Error(), "<type>:<id>") {
				t.Errorf("explain %q: err = %v, want a shape error", bad, err)
			}
		}
	})
}

func TestAttentionExplain(t *testing.T) {
	failing := `{"pr_show":{"number":3,"state":"open","author":"me","head_sha":"h1","mergeable":"CONFLICTING"},"ci":{"runs":[{"id":"1","name":"build","status":"completed","conclusion":"failure","head_sha":"h1","attempt":1}]}}`
	path := attnFixture(
		t, true,
		attnPR{n: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe},
		attnPR{n: 2, ownership: "team", panel: interpret.PanelTeamAwaitingTeam},
		attnPR{n: 3, ownership: "mine", panel: interpret.PanelMineAwaitingMe, facts: failing},
		attnPR{n: 4, ownership: "team", panel: "none"},
	)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAnnotation(store.KVAnnotation{Repo: attnRepo, EntityType: "pr", EntityID: "acme/api#3", Key: store.KeySuppress("pr.own-needs-action"), Value: "true", Origin: "test", SetBy: "test", SetAt: "2026-10-06T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	withAttentionSeams(t, attnConfig(), openAttn(path))

	type doc = explainDocument
	run := func(ref string) doc {
		t.Helper()
		out, err := runAttention(t, "explain", true, ref)
		if err != nil {
			t.Fatalf("explain %s: %v", ref, err)
		}
		var d doc
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatalf("not the explain document: %v\n%s", err, out)
		}
		return d
	}

	d := run("pr:acme/api#3")
	if !d.Listed || !d.Held || d.Group != "pr:acme/api#3" || len(d.Candidates) != 2 {
		t.Fatalf("doc = %+v", d)
	}
	bySurv := map[string]explainCandidate{}
	for _, c := range d.Candidates {
		bySurv[c.Rule] = c
	}
	if c := bySurv["pr.own-ci-failing"]; !c.Survived || c.SuppressedBy != "" {
		t.Errorf("own-ci-failing = %+v, want survived", c)
	}
	if c := bySurv["pr.own-needs-action"]; c.Survived || c.SuppressedBy != "suppress.pr.own-needs-action" {
		t.Errorf("own-needs-action = %+v, want suppressed by the annotation (INV-ATTNEVAL-4)", c)
	}

	d = run("pr:acme/api#2")
	if d.Listed || !d.Held || len(d.Candidates) != 0 || len(d.NotRaised) != 4 {
		t.Errorf("an unlisted entity must say which rules did not apply: %+v", d)
	}
	for _, n := range d.NotRaised {
		if n.Why == "" {
			t.Errorf("rule %s has no reason", n.Rule)
		}
	}

	d = run("pr:acme/api#4")
	if !d.Held || d.Evaluated || d.Listed {
		t.Errorf("held without an interpretation row: %+v", d)
	}

	d = run("pr:acme/api#99")
	if d.Held || d.Listed {
		t.Errorf("an unknown ref is not an error: %+v", d)
	}
	d = run("thread:nope")
	if d.Held {
		t.Errorf("an unknown type is not an error: %+v", d)
	}

	text, err := runAttention(t, "explain", false, "pr:acme/api#3")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"listed", "raised, survived:   pr.own-ci-failing", "suppressed by suppress.pr.own-needs-action", "not raised:         pr.review-requested"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	text, _ = runAttention(t, "explain", false, "pr:acme/api#99")
	if !strings.Contains(text, "not held") {
		t.Errorf("text = %q", text)
	}
}

func TestAttentionExplainDegradedOnUnmigratedStore(t *testing.T) {
	path := attnFixture(t, false, attnPR{n: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
	withAttentionSeams(t, attnConfig(), openAttn(path))

	out, err := runAttention(t, "explain", true, "pr:acme/api#1")
	if err != nil || !strings.Contains(out, `"degraded": true`) {
		t.Errorf("explain must report degraded on an unmigrated store (INV-ATTNEVAL-5): err=%v\n%s", err, out)
	}
	text, _ := runAttention(t, "explain", false, "pr:acme/api#1")
	if !strings.Contains(text, "degraded") {
		t.Errorf("text must report degraded: %q", text)
	}
}
