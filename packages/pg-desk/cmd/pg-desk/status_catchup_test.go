package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of the interpretation catch-up lines of `pg-desk status` and of the
// persisted last-run counts (docs/behavior/pg-desk/reinterpret.md,
// "Observability"; INV-REINTERPRET-11 and -13), over a real on-disk store.

func statusOut(t *testing.T) string {
	t.Helper()
	out, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return out
}

func TestStatusReportsBehindFailedAndNeverRun(t *testing.T) {
	rs := newRIStore(t)
	seedOpen(t, rs, 3)
	if err := rs.seed.MarkInterpretationFailed(store.InterpretationKey{Repo: riRepo, EntityType: "pr", EntityID: riID(3)}, riStamp()); err != nil {
		t.Fatal(err)
	}
	out := statusOut(t)
	for _, want := range []string{
		"interpretations behind: 2 (stamp differs from " + riStamp() + ")\n",
		"interpretations failed: 1\n",
		"last catch-up: never\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestStatusShowsLastRunOfReinterpretOpen(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 1, riFacts("open", false, "me"))
	rs.pr(t, 2, riFacts("closed", false, "me"))
	orig := changesNow
	t.Cleanup(func() { changesNow = orig })
	changesNow = func() time.Time { return time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC) }
	if _, _, err := riRun(t, "--open"); err != nil {
		t.Fatal(err)
	}
	out := statusOut(t)
	for _, want := range []string{
		"interpretations behind: 0 (stamp differs from " + riStamp() + ")\n",
		"last catch-up: 2026-11-10T09:30:00Z via pr reinterpret --open: selected 2, changed 1, unchanged 0, stamped only 1, skipped 0, failed 0, remaining 0\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

// A dry run, an idle pass and the named-PR form record nothing.
func TestReinterpretWritesLastRunOnlyForAWritingOpenPassThatSelectedRows(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 1, riFacts("open", false, "me"))
	lastRun := func() bool {
		_, found, err := rs.seed.GetMeta(store.MetaKeyReinterpretLastRun)
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	if _, _, err := riRun(t, "--open", "--dry-run"); err != nil || lastRun() {
		t.Fatalf("dry run: err=%v recorded=%v", err, lastRun())
	}
	if _, _, err := riRun(t, "--open"); err != nil || !lastRun() {
		t.Fatalf("open pass: err=%v recorded=%v", err, lastRun())
	}
	if err := rs.seed.DeleteMeta(store.MetaKeyReinterpretLastRun); err != nil {
		t.Fatal(err)
	}
	if _, _, err := riRun(t, riID(1)); err != nil || lastRun() {
		t.Fatalf("named PR: err=%v recorded=%v", err, lastRun())
	}
	if _, _, err := riRun(t, "--open"); err != nil || lastRun() {
		t.Fatalf("idle pass: err=%v recorded=%v", err, lastRun())
	}
}

// The heartbeat catch-up records its counts, including the remaining count
// when it stops at the row cap, so status shows the backlog draining; an idle
// heartbeat writes no row.
func TestStatusShowsHeartbeatBacklogDraining(t *testing.T) {
	withReleaseVersion(t)
	rs := newRIStore(t)
	withHeartbeatLedger(t, "[]", nil)
	if _, err := runHeartbeat(t); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := rs.seed.GetMeta(store.MetaKeyReinterpretLastRun); found {
		t.Fatal("an idle heartbeat wrote last-run counts")
	}
	seedOpen(t, rs, CatchUpMaxRows+3)
	if _, err := runHeartbeat(t); err != nil {
		t.Fatal(err)
	}
	out := statusOut(t)
	for _, want := range []string{
		"interpretations behind: 3 (stamp differs from ",
		" via heartbeat: selected 25, changed 25, unchanged 0, stamped only 0, skipped 0, failed 0, remaining 3\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if _, err := runHeartbeat(t); err != nil {
		t.Fatal(err)
	}
	out = statusOut(t)
	if !strings.Contains(out, "interpretations behind: 0 ") || !strings.Contains(out, "selected 3, changed 3, unchanged 0, stamped only 0, skipped 0, failed 0, remaining 0\n") {
		t.Errorf("status after the second pass:\n%s", out)
	}
}

// A reader that does not migrate: the column is absent, so the count is
// unknown, never 0 (INV-REINTERPRET-13).
func TestStatusBehindIsUnknownWithoutTheStampColumn(t *testing.T) {
	rs := newRIStore(t)
	seedOpen(t, rs, 1)
	rs.exec(t, `ALTER TABLE interpretation DROP COLUMN interp_version`)
	deskStoreOpenRaw = func() (*store.Store, error) { return store.OpenRaw(rs.path) }
	out := statusOut(t)
	if !strings.Contains(out, "interpretations behind: unknown (column not yet added)\n") {
		t.Errorf("status:\n%s", out)
	}
	if strings.Contains(out, "interpretations behind: 0") || strings.Contains(out, "interpretations failed") {
		t.Errorf("status reports a count it cannot know:\n%s", out)
	}
	if !strings.Contains(out, "last catch-up: never\n") {
		t.Errorf("status:\n%s", out)
	}
}

func TestStatusBehindIsUnknownWithNoSchemaAndWithoutConfig(t *testing.T) {
	path := storeAtVersion(t, "empty")
	withOpenSeams(t, openTestConfig(riRepo), func() (*store.Store, error) { return store.OpenRaw(path) })
	out := statusOut(t)
	if !strings.Contains(out, "interpretations behind: unknown (store not initialized)\n") || strings.Contains(out, "last catch-up") {
		t.Errorf("no schema:\n%s", out)
	}

	rs := newRIStore(t)
	seedOpen(t, rs, 1)
	for name, load := range map[string]func(context.Context) (*config.Config, error){
		"load error": func(context.Context) (*config.Config, error) { return nil, errors.New("no config") },
		"no repos":   func(context.Context) (*config.Config, error) { return &config.Config{}, nil },
	} {
		deskConfigLoad = load
		out := statusOut(t)
		if !strings.Contains(out, "interpretations behind: unknown (configuration unavailable)\n") || !strings.Contains(out, "last catch-up: never\n") {
			t.Errorf("%s:\n%s", name, out)
		}
	}
}

func TestStatusReportsACorruptLastRunAsAnError(t *testing.T) {
	rs := newRIStore(t)
	if err := rs.seed.SetMeta(store.MetaKeyReinterpretLastRun, "garbage"); err != nil {
		t.Fatal(err)
	}
	if _, err := runStatusCmd(t); err == nil || !strings.Contains(err.Error(), "last catch-up") {
		t.Fatalf("err = %v", err)
	}
}

// A failed write of the counts is one stderr line, never a failed pass.
func TestRecordLastRunFailureIsOneLine(t *testing.T) {
	rs := newRIStore(t)
	closed, err := store.Open(rs.path)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	var buf strings.Builder
	recordLastRun(&buf, closed, "heartbeat", time.Now(), reinterpretCounts{Selected: 1}, 0)
	if !strings.HasPrefix(buf.String(), "reinterpret: record last run: ") || strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("stderr = %q", buf.String())
	}
}
