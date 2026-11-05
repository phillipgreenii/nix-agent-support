package settle

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// gaugeFixture is lastSeenFixture with every Conversation also committed as
// settled, so the removal gauges consider it.
func gaugeFixture(e *csEnv, n int) map[string]*conv {
	groups := lastSeenFixture(e, n)
	for session := range groups {
		if err := e.st.Commit(&store.Rec{T: store.TStage, At: 1, Session: session, State: StageSettled}); err != nil {
			e.t.Fatal(err)
		}
	}
	return groups
}

// The facts the gauges use equal, per file, the last-seen time and byte count
// the exact per-file readers give (newest differs only by the original's mtime).
func TestBacklogFactsMatchPerFileReaders(t *testing.T) {
	e := newCSEnv(t)
	groups := lastSeenFixture(e, 20)
	var cs []*conv
	for _, c := range groups {
		cs = append(cs, c)
	}
	newest, bytes := e.s.backlogFacts(cs)
	for _, c := range cs {
		f := c.files[0]
		if want := perFileLastSeen(e.st, f); !newest[c.session].Equal(want) {
			t.Errorf("%s: newest %d, want %d", c.session, newest[c.session].UnixNano(), want.UnixNano())
		}
		if want := e.s.fileBytes(f); bytes[c.session] != want {
			t.Errorf("%s: bytes %d, want %d", c.session, bytes[c.session], want)
		}
	}
}

// A refresh of the removal gauges issues the same number of statements whether
// there are 6 or 60 settled Conversations, and never stats an original file
// (this Settler has no Mtime or Copier, so a stat would panic).
func TestRemovalGaugeRefreshStatementCountIsConstant(t *testing.T) {
	count := func(n int) int64 {
		e := newCSEnv(t)
		e.s.walkQuarantine = func() []string { return nil }
		groups := gaugeFixture(e, n)
		e.s.cfg.RemoveAfter = time.Nanosecond
		e.s.cfg.Obs = obs.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
		e.cnt.Reset()
		e.s.refreshRemovalGauges(groups, map[string]bool{}, time.Unix(0, 1<<40))
		if got, _ := e.s.cfg.Obs.Reg.Gauge(obs.MRemovalBacklogConvs, nil); got != float64(n) {
			t.Fatalf("backlog convs = %v, want %d", got, n)
		}
		return e.cnt.Queries()
	}
	small, large := count(6), count(60)
	t.Logf("statements per refresh: 6 conversations %d, 60 conversations %d", small, large)
	if small != large || large > 5 {
		t.Fatalf("statements grow with the data or are too many: %d vs %d", small, large)
	}
}

// The gauge is approximate: it counts a Conversation as ready from the time the
// recorder last saw it change, while Remove stays exact and refuses it when its
// original was modified recently.
func TestRemoveRefusesWhatTheApproximateGaugeCountsAsReady(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	e.clk.Advance(removeAge + time.Hour)
	// The original changes after the recorder last saw it (not yet ingested).
	p := e.path(e.transcript())
	if err := os.Chtimes(p, e.clk.Now().Add(-time.Hour), e.clk.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	groups, err := e.sett.groups()
	if err != nil {
		t.Fatal(err)
	}
	e.sett.refreshRemovalGauges(groups, map[string]bool{}, e.clk.Now())
	if v, _ := e.obs.Reg.Gauge(obs.MRemovalBacklogConvs, nil); v != 1 {
		t.Fatalf("gauge counts %v ready, want 1 (approximate by design)", v)
	}
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockQuietAge) {
		t.Fatalf("Remove did not refuse a recently modified original: %+v", rep)
	}
	if _, err := os.Stat(filepath.Clean(p)); err != nil {
		t.Fatalf("the original is gone: %v", err)
	}
	_ = fmt.Sprint
}
