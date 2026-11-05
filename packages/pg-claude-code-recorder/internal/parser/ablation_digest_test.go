//go:build ablation

package parser_test

// Opt-in corpus output-equivalence digest (build tag ablation, never part of
// go test ./...). It runs the harness's baseline rebuild over the corpus and
// writes one digest line per table (see digest_test.go) to
// $PG_CCREC_ABLATION_OUT/digest.$PG_CCREC_ABLATION_LABEL.txt. Run it once on
// the code before a change and once after, then diff the two files.
//
//	go test -c -tags ablation -o ablation.test ./internal/parser/
//	PG_CCREC_ABLATION_LIST=corpus.txt PG_CCREC_ABLATION_WORK=/tmp/x \
//	  PG_CCREC_ABLATION_OUT=out PG_CCREC_ABLATION_LABEL=before \
//	  ./ablation.test -test.run TestAblationDigest -test.v

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func TestAblationDigest(t *testing.T) {
	list := os.Getenv("PG_CCREC_ABLATION_LIST")
	if list == "" {
		t.Skip("PG_CCREC_ABLATION_LIST not set")
	}
	work, out, label := os.Getenv("PG_CCREC_ABLATION_WORK"), os.Getenv("PG_CCREC_ABLATION_OUT"), os.Getenv("PG_CCREC_ABLATION_LABEL")
	if work == "" || out == "" || label == "" {
		t.Fatal("PG_CCREC_ABLATION_WORK, _OUT and _LABEL are required")
	}
	files := abLoadList(t, list)
	dir := filepath.Join(work, "digest."+label)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	abBuildState(t, dir, files)

	o := obs.New(nil)
	st, err := store.Open(dir, store.Options{V3Layout: true, Obs: o, Rebuild: true, BulkRebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	p := parser.New(parser.Config{Store: st, Sources: sources.All(), ClaudeDir: "/claude", Obs: o})
	if err := p.ParseAll(); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRebuild(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "index.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got := strings.Join(tableDigests(t, db), "\n") + "\n"
	if err := os.WriteFile(filepath.Join(out, "digest."+label+".txt"), []byte(got), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", got)
}
