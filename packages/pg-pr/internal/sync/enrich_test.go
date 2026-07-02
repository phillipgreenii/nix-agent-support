package sync

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

func TestEnrichAndStore_PersistsToRow(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pr := api.PR{
		Repo: "o/r", Number: 3, Title: "fix: boom", Body: "production incident",
		Branch: "fix/boom", Author: "me", State: "open", Additions: 20, Deletions: 5,
	}
	if _, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 3, Ownership: "mine", Author: "me", State: "open"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := &Engine{deps: Deps{Store: db, Now: func() time.Time { return time.Unix(0, 0).UTC() }}}
	enriched := &vcs.EnrichedPR{PR: pr, Files: []string{"a.go"}, Commits: []string{"fix: boom"}}
	rcfg := config.RepoConfig{Remote: "o/r"}
	if err := e.enrichAndStore(ctx, "o/r", pr, enriched, rcfg); err != nil {
		t.Fatalf("enrichAndStore: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 3)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.Kind != "bugfix" || got.Size != "S" || got.Urgency == "low" || len(got.Languages) == 0 {
		t.Fatalf("enrichment not persisted: %+v", got)
	}
}
