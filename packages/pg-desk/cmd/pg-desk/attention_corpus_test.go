package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/corpus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// corpusAttentionStore seeds a cut-over store with every corpus pull request,
// interpreted by the real pipeline over its stored facts, plus a legacy-shaped
// row (an approvals blob without waiting_on), and returns its path and the
// entity ids in fixture order.
func corpusAttentionStore(t *testing.T) (string, []string) {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "corpus.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := pipeline.New(corpus.Config(), st, pipeline.WithStamp("corpus/stamp"), pipeline.WithClock(corpus.Clock()), pipeline.WithLogWriter(io.Discard))
	fxs, err := corpus.Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, fx := range fxs {
		if fx.Type != "pr" {
			continue
		}
		if err := st.UpsertEntityForTest(store.Entity{Repo: corpus.Repo, EntityType: "pr", EntityID: fx.ID, Facts: string(fx.Payload), AsOf: "2026-09-16T00:00:00Z", ContentHash: "seed"}); err != nil {
			t.Fatal(err)
		}
		if err := p.RunInterpretOnly(context.Background(), "pr", fx.ID, gather.ChangeChanged); err != nil {
			t.Fatalf("%s: %v", fx.Name, err)
		}
		ids = append(ids, fx.ID)
	}
	legacy := corpus.Repo + "#990"
	if err := st.UpsertEntityForTest(store.Entity{Repo: corpus.Repo, EntityType: "pr", EntityID: legacy, Facts: `{"pr_show":{"number":990,"state":"open","author":"teammate","head_sha":"h1"}}`, AsOf: "2026-09-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: corpus.Repo, EntityType: "pr", EntityID: legacy, Ownership: "team",
		Enrichment: `{"kind":"other","size":"S","title":"Legacy row","url":"https://example.invalid/acme/widgets/pull/990","author":"teammate"}`,
		Urgency:    `{"level":"low","score":0}`, Dispositions: "null",
		Approvals:    `{"human_approvers":0,"human_approved":false,"self_approved":false,"human_changes_requested":false,"bot_verdict":"no-decision","waiting_on_me":false}`,
		MatchReasons: `["team-authored"]`, Panel: "team_awaiting_me", AsOf: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	ids = append(ids, legacy)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path, ids
}

// `attention list` (text and --json) and `attention explain` over the corpus
// rows, byte-exact, including the legacy approvals blob without waiting_on.
func TestAttentionGoldensOverCorpus(t *testing.T) {
	path, ids := corpusAttentionStore(t)
	withAttentionSeams(t, corpus.Config(), openAttn(path))

	text, err := runAttention(t, "list", false)
	if err != nil {
		t.Fatal(err)
	}
	corpus.AssertBytes(t, filepath.Join("testdata", "golden", "attention_corpus.list.txt"), []byte(text))
	js, err := runAttention(t, "list", true)
	if err != nil {
		t.Fatal(err)
	}
	corpus.AssertBytes(t, filepath.Join("testdata", "golden", "attention_corpus.list.json.txt"), []byte(js))

	var b strings.Builder
	for _, id := range ids {
		out, err := runAttention(t, "explain", false, "pr:"+id)
		if err != nil {
			t.Fatalf("explain %s: %v", id, err)
		}
		b.WriteString("== pr:" + id + "\n" + out)
	}
	corpus.AssertBytes(t, filepath.Join("testdata", "golden", "attention_corpus.explain.txt"), []byte(b.String()))
}
