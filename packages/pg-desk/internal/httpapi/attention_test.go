package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const attentionRepo = "acme/api"

// attentionConfig is a config whose first repository matches the rows
// seedAttentionPR writes.
func attentionConfig() *config.Config {
	return &config.Config{
		HeartbeatPeriod: "60s",
		SelfLogin:       "me",
		Repos:           []config.RepoConfig{{Remote: attentionRepo}},
	}
}

// seedAttentionPR stores one team PR awaiting the operator, which
// pr.review-requested raises as a medium item.
func seedAttentionPR(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.UpsertEntity(store.Entity{
		Repo: attentionRepo, EntityType: "pr", EntityID: id,
		Facts: `{"pr_show":{"number":1,"state":"open","author":"me","head_sha":"h1"}}`, AsOf: "2026-10-06T11:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: attentionRepo, EntityType: "pr", EntityID: id, Ownership: "team", Panel: PanelTeamAwaitingMe,
		Approvals: "{}", MatchReasons: "[]", Enrichment: "{}", Urgency: "{}", Dispositions: "[]",
		AsOf: "2026-10-06T11:00:00Z",
	})
}

func attentionNow() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }

// TestPayloadCarriesTheEvaluatorsGroups pins the additive `attention` field:
// it is exactly the groups of the shared evaluator, and the panels beside it
// are unchanged.
func TestPayloadCarriesTheEvaluatorsGroups(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	seedAttentionPR(t, s, attentionRepo+"#1")
	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-10-06T11:59:30Z")

	p, err := BuildPayload(s, attentionConfig(), attentionNow())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if p.AttentionError != "" {
		t.Fatalf("AttentionError = %q, want none", p.AttentionError)
	}
	if len(p.Attention) != 1 || len(p.Attention[0].Items) != 1 {
		t.Fatalf("Attention = %+v, want one group of one item", p.Attention)
	}
	it := p.Attention[0].Items[0]
	if it.Type != "pr" || it.ID != attentionRepo+"#1" || it.Rule != "pr.review-requested" || it.Group != p.Attention[0].Key {
		t.Errorf("item = %+v, want the review-requested PR in its own group", it)
	}
	if len(p.TeamAwaitingMe) != 1 {
		t.Errorf("TeamAwaitingMe = %+v, want the panel unchanged by the new field", p.TeamAwaitingMe)
	}
}

// TestDashboardJSONAttentionIsAnArrayWhenNothingNeedsTheOperator: an empty
// evaluation is [] on the wire, never null and never absent, and there is no
// attention_error key.
func TestDashboardJSONAttentionIsAnArrayWhenNothingNeedsTheOperator(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: attentionRepo, EntityType: "pr", EntityID: attentionRepo + "#2", Ownership: "team", Panel: PanelTeamAwaitingTeam,
		Approvals: "{}", MatchReasons: "[]", Enrichment: "{}", Urgency: "{}", Dispositions: "[]", AsOf: "2026-10-06T11:00:00Z",
	})
	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-10-06T11:59:30Z")
	setClock(t, attentionNow())

	handler, err := NewHandler(s, attentionConfig())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw["attention"])); got != "[]" {
		t.Errorf("attention = %s, want []", got)
	}
	if _, ok := raw["attention_error"]; ok {
		t.Error("attention_error present on a successful evaluation")
	}
}

// TestFailedEvaluationIsNeverAllClear: an evaluation that cannot run leaves
// `attention` null with the reason in `attention_error` (INV-ATTNEVAL-6) and
// does not take the panels down.
func TestFailedEvaluationIsNeverAllClear(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func() *config.Config
		want string
	}{
		{"no repository configured", func() *config.Config { return testConfig() }, "no repository configured"},
		{"unknown rule kind", func() *config.Config {
			c := attentionConfig()
			c.Attention = config.AttentionConfig{Rules: map[string]config.AttentionRuleConfig{"no.such-rule": {}}}
			return c
		}, "unknown rule kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := store.OpenNewSchemaForTest(t)
			seedAttentionPR(t, s, attentionRepo+"#1")
			mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-10-06T11:59:30Z")
			setClock(t, attentionNow())

			handler, err := NewHandler(s, tc.cfg())
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (the panels still serve); body: %s", rr.Code, rr.Body.String())
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(raw["attention"])); got != "null" {
				t.Errorf("attention = %s, want null (never [] for a failed evaluation)", got)
			}
			var msg string
			if err := json.Unmarshal(raw["attention_error"], &msg); err != nil || !strings.Contains(msg, tc.want) {
				t.Errorf("attention_error = %s, want it to contain %q", raw["attention_error"], tc.want)
			}
			var p Payload
			if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if len(p.TeamAwaitingMe) != 1 {
				t.Errorf("TeamAwaitingMe = %+v, want the panel intact", p.TeamAwaitingMe)
			}
		})
	}
}
