package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/corpus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Serve payload golden: the dashboard JSON the handler serves for every corpus
// pull request (interpreted by the real pipeline over its stored facts) plus a
// row in the legacy shape (an approvals blob without waiting_on). The whole
// Approvals JSON is merged into each served row, so a change to any served field
// shows here. Compared as exact bytes.
func TestServePayloadGoldenOverCorpus(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	cfg := corpus.Config()
	p := pipeline.New(cfg, s, pipeline.WithClock(corpus.Clock()), pipeline.WithLogWriter(io.Discard))
	fxs, err := corpus.Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	seeded := 0
	for _, fx := range fxs {
		if fx.Type != "pr" {
			continue
		}
		if err := s.UpsertEntity(store.Entity{Repo: corpus.Repo, EntityType: "pr", EntityID: fx.ID, Facts: string(fx.Payload), AsOf: "2026-09-16T00:00:00Z", ContentHash: "seed"}); err != nil {
			t.Fatal(err)
		}
		if err := p.RunInterpretOnly(context.Background(), "pr", fx.ID, gather.ChangeChanged); err != nil {
			t.Fatalf("%s: %v", fx.Name, err)
		}
		seeded++
	}
	// A row written by an older binary: the decision pair is absent.
	if err := s.UpsertInterpretation(store.Interpretation{
		Repo: corpus.Repo, EntityType: "pr", EntityID: corpus.Repo + "#990", Ownership: "team",
		Enrichment:   `{"kind":"other","size":"S","title":"Legacy row","url":"https://example.invalid/acme/widgets/pull/990","author":"teammate"}`,
		Urgency:      `{"level":"low","score":0}`,
		Dispositions: "null",
		Approvals:    `{"human_approvers":0,"human_approved":false,"self_approved":false,"human_changes_requested":false,"bot_verdict":"no-decision","waiting_on_me":false}`,
		MatchReasons: `["team-authored"]`, Panel: PanelTeamAwaitingMe, AsOf: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if seeded < 30 {
		t.Fatalf("seeded %d pr fixtures", seeded)
	}
	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-09-16T12:00:00Z")
	setClock(t, time.Date(2026, 9, 16, 12, 0, 30, 0, time.UTC))

	h, err := NewHandler(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, bytes.TrimSpace(rr.Body.Bytes()), "", " "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	corpus.AssertBytes(t, filepath.Join("testdata", "golden", "serve_corpus.payload.txt"), pretty.Bytes())
}
