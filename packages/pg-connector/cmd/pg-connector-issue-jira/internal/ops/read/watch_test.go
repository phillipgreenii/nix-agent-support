package read

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestShowWithWatchOnAKnownIssueAppendsOneAddedRowOnceAndASecondShowAppendsNone(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	if r.isWatched(issueKey) || len(r.rows(issueKey)) != 0 {
		t.Fatal("the issue starts watched or with rows")
	}

	r.mustShow(`{"id":"ABC-1","watch":true}`)
	if !r.isWatched(issueKey) {
		t.Fatal("the issue is not watched after show with watch")
	}
	rows := r.rows(issueKey)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Kinds, []string{classify.KindAdded}) || !rows[0].Watched {
		t.Fatalf("rows = %+v, want one added row of a watched issue", rows)
	}
	ent, _, _ := r.st.GetEntity(t.Context(), "issue", issueKey)
	if ent.WatchedVia != registry.ViaDirect {
		t.Errorf("watched via %q, want %q", ent.WatchedVia, registry.ViaDirect)
	}

	r.mustShow(`{"id":"ABC-1","watch":true}`)
	if got := len(r.rows(issueKey)); got != 1 {
		t.Errorf("rows after a second watching show = %d, want still 1", got)
	}
}

func TestShowWithWatchAppliesTheWatchBeforeTheReadOfAKnownIssue(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	r.clk.Advance(2 * summaryTTL) // the summary is stale, so the read fetches it

	r.mustShow(`{"id":"ABC-1","watch":true}`)
	r.origin.mu.Lock()
	watched, fetched := r.origin.watchedAtSummaryFetch[issueKey]
	r.origin.mu.Unlock()
	if !fetched || !watched {
		t.Errorf("watched at the summary fetch = %v (fetched %v), want the watch applied before the read", watched, fetched)
	}
}

func TestShowWithWatchOnAnUnknownIssueSetsTheFlagAfterTheReadAndAppendsNoRow(t *testing.T) {
	r := newRig(t)
	r.origin.set(issueOf("XYZ-7", "new one"), "its description")

	r.mustShow(`{"id":"xyz-7","watch":true}`)
	if !r.isWatched("XYZ-7") {
		t.Fatal("the issue is not watched after show with watch")
	}
	if rows := r.rows("XYZ-7"); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the first fetch of a watched issue is a baseline", rows)
	}
	r.origin.mu.Lock()
	watched, fetched := r.origin.watchedAtSummaryFetch["XYZ-7"]
	r.origin.mu.Unlock()
	if !fetched || watched {
		t.Errorf("watched at the summary fetch = %v (fetched %v), want the watch applied after the read", watched, fetched)
	}

	// The issue is now known: a later watching show of it adds nothing, since the
	// flag is already set.
	r.mustShow(`{"id":"XYZ-7","watch":true}`)
	if rows := r.rows("XYZ-7"); len(rows) != 0 {
		t.Errorf("rows after a second watching show = %+v, want none", rows)
	}
}

func TestShowWithoutWatchNeverWatchesNorClearsAWatch(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	r.mustShow(`{"id":"ABC-1"}`)
	r.mustShow(`{"id":"ABC-1","watch":false}`)
	if r.isWatched(issueKey) || len(r.rows(issueKey)) != 0 {
		t.Error("a read without the option watched the issue")
	}
	r.mustShow(`{"id":"ABC-1","watch":true}`)
	r.mustShow(`{"id":"ABC-1"}`)
	r.mustShow(`{"id":"ABC-1","watch":false}`)
	if !r.isWatched(issueKey) {
		t.Error("a read without the option cleared the watch")
	}
}

func TestShowWithWatchOfAnIdThatCannotBeReadWatchesNothing(t *testing.T) {
	r := newRig(t)
	if _, err := r.show(`{"id":"owner/repo#1","watch":true}`); !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("show of a non-Jira id with watch = %v, want not_found", err)
	}
	if _, err := r.show(`{"id":"ZZZ-9","watch":true}`); !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("show of an issue the origin does not know with watch = %v, want not_found", err)
	}
	if r.isWatched("ZZZ-9") {
		t.Error("an issue that was not found is watched")
	}
}

func TestShowWithWatchOnAnUnreadableUnknownIssueWatchesNothing(t *testing.T) {
	r := newRig(t)
	r.origin.setErr(errors.New("origin down"))
	if _, err := r.show(`{"id":"NEW-5","watch":true}`); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("show = %v, want unavailable", err)
	}
	if r.isWatched("NEW-5") {
		t.Error("an issue that could not be read is watched")
	}
}

func TestShowWithWatchOfAKnownIssueKeepsTheWatchWhenTheReadFails(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	r.origin.setErr(errors.New("origin down"))
	r.clk.Advance(2 * time.Hour * 24 * 7) // past the expiry: nothing is servable
	if _, err := r.show(`{"id":"ABC-1","watch":true}`); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("show = %v, want unavailable", err)
	}
	if !r.isWatched(issueKey) {
		t.Error("the one-off watch of a known issue is applied before the read, so it stays when the read fails")
	}
}

func TestShowWithWatchIsUnavailableWithoutAChangeEngine(t *testing.T) {
	r := newRig(t)
	r.seed(issueKey, "t", "d")
	r.ops = Handlers(Deps{Summary: r.summary, Detail: r.detail, Store: r.st, Clock: r.clk})
	if _, err := r.show(`{"id":"ABC-1","watch":true}`); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("show with watch and no engine = %v, want unavailable", err)
	}
	if _, err := r.show(`{"id":"ABC-1"}`); err != nil {
		t.Errorf("show without watch and no engine = %v", err)
	}
}
