package pipeline

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/corpus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// hydrateWith hydrates fx once on a fresh schema-2 store under cfg and the
// given stamp options, and returns the stored row.
func hydrateWith(t *testing.T, fx corpus.Fixture, cfg *config.Config, opts ...Option) store.Interpretation {
	t.Helper()
	st := store.OpenNewSchemaForTest(t)
	all := append([]Option{WithClock(corpus.Clock()), WithLogWriter(io.Discard)}, opts...)
	p := New(cfg, st, all...)
	g := corpusGatherer{fx}
	p.entityGatherers = map[string]gather.EntityGatherer{"pr": g, "issue": g, "thread": g}
	if _, err := p.RunEntityChange(context.Background(), fx.Type, fx.ID, gather.ChangeAdded, EntityChangeOptions{}); err != nil {
		t.Fatalf("RunEntityChange: %v", err)
	}
	row, found, err := st.GetInterpretation(corpus.Repo, fx.Type, fx.ID)
	if err != nil || !found {
		t.Fatalf("row found=%v err=%v", found, err)
	}
	return row
}

// INV-REINTERPRET-4: a pipeline given WithStamp writes rows carrying it; one
// without it writes NULL; an empty stamp leaves the handle's stamp alone.
func TestWithStamp_RowsCarryTheHandleStamp(t *testing.T) {
	fx := mustFixtures(t)[0]
	cfg := corpus.Config()
	stamp := interpret.Stamp("0.0.0-deadbeef", cfg)

	if got := hydrateWith(t, fx, cfg, WithStamp(stamp)).InterpVersion; got != stamp {
		t.Fatalf("InterpVersion = %q, want %q", got, stamp)
	}
	if got := hydrateWith(t, fx, cfg).InterpVersion; got != "" {
		t.Fatalf("a pipeline without a stamp wrote %q, want NULL", got)
	}

	st := store.OpenNewSchemaForTest(t)
	st.SetInterpStamp("preset")
	New(cfg, st, WithStamp(""))
	if got := st.InterpStamp(); got != "preset" {
		t.Fatalf("WithStamp(\"\") changed the handle stamp to %q", got)
	}
	New(cfg, st, WithStamp("next"))
	if got := st.InterpStamp(); got != "next" {
		t.Fatalf("handle stamp = %q, want next", got)
	}
}

// Config changes show in rows, not only in the stamp: a change to a value
// interpret reads changes at least one corpus row AND the stamp; a change to
// one it does not read changes neither.
func TestConfigChange_ShowsInRowsAndStamp(t *testing.T) {
	base := corpus.Config()
	read := corpus.Config()
	read.SelfLogin = "someone-else" // ownership depends on it
	unread := corpus.Config()
	unread.Path = "/another/place/config.yaml"
	unread.Serve.Addr = "127.0.0.1:9"

	if interpret.Stamp("v", base) == interpret.Stamp("v", read) {
		t.Fatal("a changed self_login did not change the stamp")
	}
	if interpret.Stamp("v", base) != interpret.Stamp("v", unread) {
		t.Fatal("a config edit interpret does not read changed the stamp")
	}

	rowsDiffer := 0
	for _, fx := range mustFixtures(t) {
		b := corpus.RenderRow(hydrateWith(t, fx, base))
		if !bytes.Equal(b, corpus.RenderRow(hydrateWith(t, fx, unread))) {
			t.Fatalf("%s: a config edit interpret does not read changed the row", fx.Name)
		}
		if !bytes.Equal(b, corpus.RenderRow(hydrateWith(t, fx, read))) {
			rowsDiffer++
		}
	}
	if rowsDiffer == 0 {
		t.Fatal("changing self_login changed no corpus row: a config regression would not show in a row")
	}
}
