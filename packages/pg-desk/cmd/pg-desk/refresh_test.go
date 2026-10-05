package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// refreshFixture is a new-schema store, a config and a fake pg-connector on
// PATH. Per entity id (with "/" and "#" mapped to "_"), <dir>/show-<id>.json
// is the stdout of `<type> show <id>` and <dir>/show-<id>.exit its exit code
// (default 0); a missing json file exits 1.
type refreshFixture struct {
	t    *testing.T
	seed *store.Store
	dir  string
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	open, seed := seedLinkStore(t)
	withOpenSeams(t, openTestConfig("o/r"), open)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	installFakePGConnector(t, fmt.Sprintf(`D=%q
if [ "$2" = show ]; then
  id=$(echo "$3" | tr '/#' '__')
  echo "$1 show $3" >> "$D/calls.log"
  code=0
  if [ -f "$D/show-$id.exit" ]; then code=$(cat "$D/show-$id.exit"); fi
  if [ -f "$D/show-$id.json" ]; then cat "$D/show-$id.json"; exit "$code"; fi
  echo '{"error":{"code":"boom","message":"no fixture"}}'
  exit 1
fi
exit 99`, dir))
	return &refreshFixture{t: t, seed: seed, dir: dir}
}

func (f *refreshFixture) write(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *refreshFixture) showIssue(id, updatedAt string) {
	f.t.Helper()
	f.write("show-"+id+".json", fmt.Sprintf(
		`{"protocolVersion":1,"schemaVersion":4,"result":{"id":%q,"title":"title of %s","owner":"me","assignee":"x","issue_type":"task","state":"open","updated_at":%q,"as_of":"2026-09-30T00:00:00Z"}}`,
		id, id, updatedAt,
	))
}

func (f *refreshFixture) showThread(id string) {
	f.t.Helper()
	f.write("show-"+strings.NewReplacer("/", "_", "#", "_").Replace(id)+".json", fmt.Sprintf(
		`{"protocolVersion":1,"schemaVersion":4,"result":{"id":%q,"as_of":"2026-09-30T00:00:00Z","text":"hello"}}`, id,
	))
}

func (f *refreshFixture) entity(typ, id string) store.Entity {
	f.t.Helper()
	e, found, err := f.seed.GetEntity("o/r", typ, id)
	if err != nil || !found {
		f.t.Fatalf("GetEntity %s %s: found=%v err=%v", typ, id, found, err)
	}
	return e
}

func runRefreshCmd(t *testing.T, entityType string, args ...string) (stdout string, err error) {
	t.Helper()
	c := newRefreshCmd(entityType)
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err = c.ExecuteContext(context.Background())
	return out.String(), err
}

func TestRefreshIsRegisteredUnderEveryTypeGroup(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		c, _, err := rootCmd.Find([]string{typ, "refresh"})
		if err != nil || c.Name() != "refresh" || c.Parent() != typeGroup(typ) {
			t.Errorf("%s refresh not registered: %v, %v", typ, c, err)
		}
	}
}

func TestRefreshHappyPathHydratesOneEntityAndExitsZero(t *testing.T) {
	for _, tc := range []struct {
		typ, id string
		prep    func(f *refreshFixture)
	}{
		{"issue", "bd-1", func(f *refreshFixture) { f.showIssue("bd-1", "2026-09-30T00:00:00Z") }},
		{"thread", "C1/1700.5", func(f *refreshFixture) { f.showThread("C1/1700.5") }},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			f := newRefreshFixture(t)
			tc.prep(f)
			out, err := runRefreshCmd(t, tc.typ, tc.id)
			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if !strings.Contains(out, "refreshed "+tc.typ+" "+tc.id) {
				t.Errorf("stdout %q", out)
			}
			e := f.entity(tc.typ, tc.id)
			if e.Inactive || e.HydratedAt == "" || e.Version == 0 {
				t.Errorf("entity after refresh = %+v, want active and hydrated", e)
			}
			hist, herr := f.seed.ListEntityHistory("o/r", tc.typ, tc.id, 0)
			if herr != nil || len(hist) != 1 || hist[0].Origin != refreshOrigin {
				t.Errorf("history = %+v, %v; want one record at origin %q", hist, herr, refreshOrigin)
			}
			stats, serr := changes.ReadHydrationStats(f.seed, tc.typ)
			if serr != nil || stats.Hydrations != 1 || stats.Failures != 0 {
				t.Errorf("hydration stats = %+v, %v; want 1 hydration, 0 failures", stats, serr)
			}
			if _, err := os.Stat(filepath.Join(f.dir, "calls.log")); err != nil {
				t.Errorf("pg-connector was not called: %v", err)
			}
		})
	}
}

func TestRefreshFailureKeepsSnapshotAndEntityStaysDue(t *testing.T) {
	f := newRefreshFixture(t)
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := runRefreshCmd(t, "issue", "bd-1"); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	before := f.entity("issue", "bd-1")

	// The detail read now fails with a hard connector error.
	f.showIssue("bd-1", "2026-10-01T00:00:00Z")
	f.write("show-bd-1.exit", "1")
	out, err := runRefreshCmd(t, "issue", "bd-1")
	if err == nil || exitCodeFor(err) != exitTotal {
		t.Fatalf("err = %v (exit %d), want exit %d", err, exitCodeFor(err), exitTotal)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty on failure", out)
	}
	after := f.entity("issue", "bd-1")
	if after.Version != before.Version || after.HydratedAt != before.HydratedAt || after.Facts != before.Facts || after.Inactive {
		t.Errorf("entity changed by a failed refresh:\nbefore %+v\nafter  %+v", before, after)
	}
	hist, herr := f.seed.ListEntityHistory("o/r", "issue", "bd-1", 0)
	if herr != nil || len(hist) != 1 {
		t.Errorf("history = %d records, %v; want the one from the first refresh", len(hist), herr)
	}
	stats, serr := changes.ReadHydrationStats(f.seed, "issue")
	if serr != nil || stats.Hydrations != 2 || stats.Failures != 1 || stats.Degraded["bd-1"].Count != 1 {
		t.Errorf("hydration stats = %+v, %v; want 2 hydrations, 1 failure, bd-1 degraded once", stats, serr)
	}
}

func TestRefreshNotFoundFailsLoudly(t *testing.T) {
	for _, tc := range []struct{ typ, id string }{
		{"issue", "bd-404"},
		{"thread", "C1/404"},
		{"pr", "o/r#404"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			f := newRefreshFixture(t)
			key := strings.NewReplacer("/", "_", "#", "_").Replace(tc.id)
			f.write("show-"+key+".json", `{"error":{"code":"not_found","message":"gone"}}`)
			f.write("show-"+key+".exit", "4")
			out, err := runRefreshCmd(t, tc.typ, tc.id)
			if err == nil || exitCodeFor(err) != exitTotal {
				t.Fatalf("err = %v (exit %d), want exit %d", err, exitCodeFor(err), exitTotal)
			}
			if !strings.Contains(err.Error(), tc.id) || !strings.Contains(err.Error(), "not found") {
				t.Errorf("error %q must name the entity %q and contain \"not found\"", err, tc.id)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if _, found, gerr := f.seed.GetEntity("o/r", tc.typ, tc.id); gerr != nil || found {
				t.Errorf("a not-found refresh created an entity row: found=%v err=%v", found, gerr)
			}
		})
	}
}

// fakeHydrator records the RunEntityChange call and returns a canned answer.
type fakeHydrator struct {
	change gather.ChangeKind
	opts   pipeline.EntityChangeOptions
	calls  []string
	res    pipeline.EntityChangeResult
	err    error
}

func (h *fakeHydrator) RunEntityChange(_ context.Context, entityType, entityID string, change gather.ChangeKind, opts pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	h.calls = append(h.calls, entityType+" "+entityID)
	h.change, h.opts = change, opts
	return h.res, h.err
}

func withFakeHydrator(t *testing.T, h *fakeHydrator) {
	t.Helper()
	orig := newRefreshHydrator
	t.Cleanup(func() { newRefreshHydrator = orig })
	newRefreshHydrator = func(*config.Config, *store.Store) changes.Hydrator { return h }
}

func TestRefreshDegradedHydrationExitsTwoAndKeepsSnapshot(t *testing.T) {
	f := newRefreshFixture(t)
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := runRefreshCmd(t, "issue", "bd-1"); err != nil {
		t.Fatal(err)
	}
	before := f.entity("issue", "bd-1")

	withFakeHydrator(t, &fakeHydrator{res: pipeline.EntityChangeResult{Degraded: "ci list"}})
	out, err := runRefreshCmd(t, "issue", "bd-1")
	if err == nil || exitCodeFor(err) != exitPartial {
		t.Fatalf("err = %v (exit %d), want exit %d", err, exitCodeFor(err), exitPartial)
	}
	if !strings.Contains(err.Error(), "ci list") || !strings.Contains(err.Error(), "bd-1") {
		t.Errorf("error %q must name the entity and the degraded note", err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	if after := f.entity("issue", "bd-1"); after.Version != before.Version || after.HydratedAt != before.HydratedAt {
		t.Errorf("a degraded refresh changed the entity: %+v -> %+v", before, after)
	}
	stats, serr := changes.ReadHydrationStats(f.seed, "issue")
	if serr != nil || stats.Failures != 1 || stats.Degraded["bd-1"].Count != 1 {
		t.Errorf("hydration stats = %+v, %v; want the degraded call recorded", stats, serr)
	}
}

func TestRefreshChangeKindIsNeitherRemovedNorSweep(t *testing.T) {
	if refreshChangeKind == gather.ChangeRemoved || refreshChangeKind == gather.ChangeSweep {
		t.Fatalf("refreshChangeKind = %q; removed would exempt the not-found failure and sweep skips an unchanged PR head", refreshChangeKind)
	}
	if refreshChangeKind != gather.ChangeChanged {
		t.Errorf("refreshChangeKind = %q, want %q (the choice recorded in refresh.md)", refreshChangeKind, gather.ChangeChanged)
	}

	f := newRefreshFixture(t)
	h := &fakeHydrator{res: pipeline.EntityChangeResult{Written: true, Version: 1}}
	withFakeHydrator(t, h)
	if _, err := runRefreshCmd(t, "pr", "o/r#7"); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0] != "pr o/r#7" {
		t.Errorf("RunEntityChange calls = %v, want exactly one for pr o/r#7", h.calls)
	}
	if h.change != refreshChangeKind || h.opts.Origin != refreshOrigin {
		t.Errorf("RunEntityChange got change %q origin %q, want %q / %q", h.change, h.opts.Origin, refreshChangeKind, refreshOrigin)
	}
	if h.opts.ThreadActiveWindow != config.DefaultThreadActiveWindow {
		t.Errorf("ThreadActiveWindow = %v, want the configured window", h.opts.ThreadActiveWindow)
	}
	stats, serr := changes.ReadHydrationStats(f.seed, "pr")
	if serr != nil || stats.Hydrations != 1 {
		t.Errorf("hydration stats = %+v, %v; want the call recorded", stats, serr)
	}
}

func TestRefreshEntityRecordsAHardErrorAndReturnsIt(t *testing.T) {
	f := newRefreshFixture(t)
	boom := errors.New("boom")
	withFakeHydrator(t, &fakeHydrator{err: boom})
	cfg := openTestConfig("o/r")
	if _, err := refreshEntity(context.Background(), cfg, f.seed, "thread", "C1/1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	stats, serr := changes.ReadHydrationStats(f.seed, "thread")
	if serr != nil || stats.Hydrations != 1 || stats.Failures != 1 {
		t.Errorf("hydration stats = %+v, %v", stats, serr)
	}
}

func TestRefreshRefusesOldSchemaStore(t *testing.T) {
	path := storeAtVersion(t, "old")
	withOpenSeams(t, openTestConfig("o/r"), func() (*store.Store, error) { return store.Open(path) })
	h := &fakeHydrator{}
	withFakeHydrator(t, h)
	out, err := runRefreshCmd(t, "issue", "bd-1")
	if !errors.Is(err, store.ErrOldSchema) || !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
		t.Fatalf("err = %v, want the old-schema refusal naming pg-desk migrate --cutover", err)
	}
	if out != "" || exitCodeFor(err) != 1 || len(h.calls) != 0 {
		t.Errorf("stdout %q, exit %d, hydrator calls %v", out, exitCodeFor(err), h.calls)
	}
}

func TestRefreshRequiresExactlyOneID(t *testing.T) {
	newRefreshFixture(t)
	if _, err := runRefreshCmd(t, "issue"); err == nil {
		t.Error("refresh without an id succeeded")
	}
	if _, err := runRefreshCmd(t, "issue", "a", "b"); err == nil {
		t.Error("refresh with two ids succeeded")
	}
}
