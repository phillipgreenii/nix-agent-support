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
	"time"

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
	// The fixture's connector answers exit 1 with an error envelope whose code is
	// "boom": a failure that is neither absent nor transient.
	if out != "class=hydrate_error\n" {
		t.Errorf("stdout = %q, want only the class line on failure", out)
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
			if out != "class=not_found\n" {
				t.Errorf("stdout = %q, want only the line class=not_found", out)
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
	if out != "class=degraded\n" {
		t.Errorf("stdout = %q, want only the line class=degraded", out)
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

// TestRefreshClassTellsATransientFailureFromNotFound is the point of the class
// line: a source that answers with a transient failure (the connector's
// "unavailable" code, which carries a message that may even say "not found")
// is never reported as not_found, and each code lands in its class.
func TestRefreshClassTellsATransientFailureFromNotFound(t *testing.T) {
	for _, tc := range []struct {
		name, exit, body, wantClass string
	}{
		{"transient", "1", `{"error":{"code":"unavailable","message":"upstream 502: repository not found behind the proxy"}}`, "transient"},
		{"unauthenticated", "1", `{"error":{"code":"unauthenticated","message":"bad credentials"}}`, "unauthenticated"},
		{"not found", "4", `{"error":{"code":"not_found","message":"gone"}}`, "not_found"},
		{"other wire code", "1", `{"error":{"code":"version_mismatch","message":"skew"}}`, "hydrate_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefreshFixture(t)
			f.write("show-o_r_1.json", tc.body)
			f.write("show-o_r_1.exit", tc.exit)
			out, err := runRefreshCmd(t, "pr", "o/r#1")
			if err == nil || exitCodeFor(err) != exitTotal {
				t.Fatalf("err = %v (exit %d), want exit %d", err, exitCodeFor(err), exitTotal)
			}
			if want := "class=" + tc.wantClass + "\n"; out != want {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		})
	}
}

func TestRefreshSuccessPrintsNoClassLine(t *testing.T) {
	f := newRefreshFixture(t)
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	out, err := runRefreshCmd(t, "issue", "bd-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "class=") {
		t.Errorf("stdout %q carries a class line on success", out)
	}
}

// backendFixture is a new-schema store, a config with one OWNING issue
// backend (jira-a) and two non-owning ones, and a fake pg-connector that logs
// every call to <dir>/calls.log. `issue show <id> --backend jira-a` answers
// only for an id with a held-<id> file (exit 4 otherwise); any other call
// answers show-<id>.json (show-<id>.watch.json for a --watch call); a file
// named down makes every call answer the wire error unavailable.
type backendFixture struct {
	*refreshFixture
	cfg *config.Config
}

func backendTestConfig() *config.Config {
	cfg := openTestConfig("o/r")
	cfg.TicketPatterns = []string{`[A-Z]+-\d+`}
	cfg.AgentTrackerBackend = "beads-a"
	cfg.Watch.Issue.Queries = []string{"jira-q", "beads-q", "beads-q2"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"jira-q":   {Instance: "jira-a", OwnsChanges: true},
		"beads-q":  {Instance: "beads-a"},
		"beads-q2": {Instance: "beads-b"},
	}
	return cfg
}

const backendFakeScript = `D=%q
echo "$*" >> "$D/calls.log"
id="$3"
backend=""; watch=0; prev=""
for a in "$@"; do
  if [ "$prev" = "--backend" ]; then backend="$a"; fi
  if [ "$a" = "--watch" ]; then watch=1; fi
  prev="$a"
done
if [ -f "$D/down" ]; then echo '{"error":{"code":"unavailable","message":"daemon down"}}'; exit 1; fi
if [ "$backend" = "jira-a" ] && [ ! -f "$D/held-$id" ]; then echo '{"error":{"code":"not_found","message":"no"}}'; exit 4; fi
if [ "$watch" = 1 ] && [ -f "$D/show-$id.watch.json" ]; then cat "$D/show-$id.watch.json"; exit 0; fi
if [ -f "$D/show-$id.json" ]; then cat "$D/show-$id.json"; exit 0; fi
echo '{"error":{"code":"not_found","message":"no"}}'; exit 4`

func newBackendFixture(t *testing.T, seedIDs ...string) *backendFixture {
	t.Helper()
	ents := make([][2]string, 0, len(seedIDs))
	for _, id := range seedIDs {
		ents = append(ents, [2]string{"issue", id})
	}
	open, seed := seedLinkStore(t, ents...)
	cfg := backendTestConfig()
	withOpenSeams(t, cfg, open)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	installFakePGConnector(t, fmt.Sprintf(backendFakeScript, dir))
	return &backendFixture{refreshFixture: &refreshFixture{t: t, seed: seed, dir: dir}, cfg: cfg}
}

// showJira writes the answer for a Jira issue; watched is the stated
// entity_meta.watched ("" states nothing).
func (f *backendFixture) showJira(id, title, watched string) {
	f.t.Helper()
	meta := ""
	if watched != "" {
		meta = fmt.Sprintf(`,"entity_meta":{"watched":%s}`, watched)
	}
	body := fmt.Sprintf(
		`{"protocolVersion":1,"schemaVersion":4,"result":{"id":%q,"title":%q,"owner":"me","assignee":"x","issue_type":"task","state":"open","updated_at":%q,"as_of":"2026-09-30T00:00:00Z"%s}}`,
		id, title, "2026-09-30T00:00:00Z", meta,
	)
	f.write("show-"+id+".json", body)
	f.write("held-"+id, "")
}

func (f *backendFixture) calls() []string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (f *backendFixture) setBackend(id, backend string) {
	f.t.Helper()
	if err := f.seed.SetEntityColumns("o/r", "issue", id, &backend, nil); err != nil {
		f.t.Fatal(err)
	}
}

func (f *backendFixture) history(id string) []store.ChangeRecord {
	f.t.Helper()
	h, err := f.seed.ListEntityHistory("o/r", "issue", id, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

// T-8: refresh of an owning-backend entity reads the column and pins it; the
// unwatched entity is hydrated and appends no row (the gate).
func TestRefreshOwningBackendColumnPinsTheReadAndAppendsNoRowWhenUnwatched(t *testing.T) {
	f := newBackendFixture(t, "PROJ-1")
	f.setBackend("PROJ-1", "jira-a")
	f.showJira("PROJ-1", "first", "")
	before := f.entity("issue", "PROJ-1")

	if _, err := runRefreshCmd(t, "issue", "PROJ-1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	calls := f.calls()
	if len(calls) != 1 || calls[0] != "issue show PROJ-1 --fresh --backend jira-a" {
		t.Fatalf("calls = %q, want exactly the pinned show", calls)
	}
	after := f.entity("issue", "PROJ-1")
	if after.Version <= before.Version || after.Backend != "jira-a" {
		t.Errorf("entity = %+v (was version %d), want a new version with backend jira-a", after, before.Version)
	}
	if h := f.history("PROJ-1"); len(h) != 1 {
		t.Errorf("history = %d records, want only the seed record: an unwatched owning entity appends no row", len(h))
	}
}

func TestRefreshNullColumnResolvesTheBackendFromTheConfiguredQueries(t *testing.T) {
	f := newBackendFixture(t, "bd-5", "PROJ-2")
	if err := f.seed.SetMeta("change_flow.watchset.issue.beads-q", `["bd-5"]`); err != nil {
		t.Fatal(err)
	}
	f.write("show-bd-5.json", `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"bd-5","title":"t","updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`)
	if _, err := runRefreshCmd(t, "issue", "bd-5"); err != nil {
		t.Fatalf("refresh bd-5: %v", err)
	}
	if calls := f.calls(); len(calls) != 1 || calls[0] != "issue show bd-5 --fresh --backend beads-a" {
		t.Errorf("calls = %q, want the show pinned to the watch set's backend", calls)
	}
	if e := f.entity("issue", "bd-5"); e.Backend != "beads-a" {
		t.Errorf("backend = %q, want beads-a", e.Backend)
	}

	// A ticket-shaped id in no watch set resolves to the single owning
	// instance with no probe: the one call is the pinned read.
	f.showJira("PROJ-2", "t", "")
	if _, err := runRefreshCmd(t, "issue", "PROJ-2"); err != nil {
		t.Fatalf("refresh PROJ-2: %v", err)
	}
	calls := f.calls()
	if len(calls) != 2 || calls[1] != "issue show PROJ-2 --fresh --backend jira-a" {
		t.Errorf("calls = %q, want one pinned read for PROJ-2 and no probe", calls)
	}
	if e := f.entity("issue", "PROJ-2"); e.Backend != "jira-a" {
		t.Errorf("backend = %q, want jira-a", e.Backend)
	}
}

func TestRefreshAnIDServedByTwoBackendsFailsWithErrAmbiguousBackend(t *testing.T) {
	f := newBackendFixture(t, "bd-9")
	if err := f.seed.SetMeta("change_flow.watchset.issue.beads-q", `["bd-9"]`); err != nil {
		t.Fatal(err)
	}
	if err := f.seed.SetMeta("change_flow.watchset.issue.beads-q2", `["bd-9"]`); err != nil {
		t.Fatal(err)
	}
	out, err := runRefreshCmd(t, "issue", "bd-9")
	if !errors.Is(err, changes.ErrAmbiguousBackend) || !strings.Contains(err.Error(), "bd-9") {
		t.Fatalf("err = %v, want ErrAmbiguousBackend naming the id", err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want none", out)
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Errorf("calls = %q, want no read of an ambiguous id", calls)
	}
}

// T-20: refresh --watch forwards --watch, sets watched in the same write, and
// the first-observation row is appended and readable by ReadChanges; a repeat
// appends no second added; a later content change on the now-watched entity
// appends the classifier's kinds.
func TestRefreshWatchForwardsTheOptionAndDeliversTheFirstRow(t *testing.T) {
	f := newBackendFixture(t, "PROJ-1")
	f.setBackend("PROJ-1", "jira-a")
	f.showJira("PROJ-1", "first", "")
	f.write("show-PROJ-1.watch.json", strings.Replace(readFile(t, filepath.Join(f.dir, "show-PROJ-1.json")), `"as_of"`, `"entity_meta":{"watched":true},"as_of"`, 1))
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if _, err := f.seed.ReadChanges("c1", "issue", 10, now); err != nil {
		t.Fatal(err)
	}
	base := len(f.history("PROJ-1"))

	if _, err := runRefreshCmd(t, "issue", "PROJ-1", "--watch"); err != nil {
		t.Fatalf("refresh --watch: %v", err)
	}
	if calls := f.calls(); len(calls) != 1 || calls[0] != "issue show PROJ-1 --fresh --backend jira-a --watch" {
		t.Fatalf("calls = %q, want the pinned show carrying --watch", calls)
	}
	if e := f.entity("issue", "PROJ-1"); e.Watched == nil || !*e.Watched {
		t.Errorf("watched = %v, want true from the same write", e.Watched)
	}
	h := f.history("PROJ-1")
	if len(h) != base+1 || strings.Join(h[0].Kinds, ",") != "added" || h[0].Origin != refreshOrigin {
		t.Fatalf("history = %+v, want one new added row at origin %q", h, refreshOrigin)
	}
	recs, err := f.seed.ReadChanges("c1", "issue", 10, now)
	if err != nil || len(recs) != base+1 || strings.Join(recs[len(recs)-1].Kinds, ",") != "added" {
		t.Fatalf("ReadChanges = %+v, %v; want the seed record then the added row", recs, err)
	}

	// A repeated --watch on an already-watched entity appends no second added.
	if _, err := runRefreshCmd(t, "issue", "PROJ-1", "--watch"); err != nil {
		t.Fatalf("repeat refresh --watch: %v", err)
	}
	if h2 := f.history("PROJ-1"); len(h2) != len(h) {
		t.Errorf("history grew from %d to %d on a repeated --watch", len(h), len(h2))
	}

	// A watched entity whose content changed gets one row from the classifier.
	f.showJira("PROJ-1", "second", "true")
	if _, err := runRefreshCmd(t, "issue", "PROJ-1"); err != nil {
		t.Fatalf("refresh after a change: %v", err)
	}
	h3 := f.history("PROJ-1")
	if len(h3) != len(h)+1 || len(h3[0].Kinds) == 0 || h3[0].Kinds[0] == "added" {
		t.Fatalf("history = %+v, want one new classified row after the content change", h3)
	}
}

func TestRefreshWatchFlagExistsForIssueOnly(t *testing.T) {
	if newRefreshCmd("issue").Flags().Lookup("watch") == nil {
		t.Error("issue refresh has no --watch")
	}
	for _, typ := range []string{"pr", "thread"} {
		if newRefreshCmd(typ).Flags().Lookup("watch") != nil {
			t.Errorf("%s refresh has a --watch", typ)
		}
	}
}

func TestRefreshOwningEntityUnavailableIsATransientFailureAndWritesNothing(t *testing.T) {
	f := newBackendFixture(t, "PROJ-1")
	f.setBackend("PROJ-1", "jira-a")
	f.showJira("PROJ-1", "first", "")
	f.write("down", "")
	before := f.entity("issue", "PROJ-1")
	baseHist := len(f.history("PROJ-1"))

	out, err := runRefreshCmd(t, "issue", "PROJ-1", "--watch")
	if err == nil || exitCodeFor(err) != exitTotal || !errors.Is(err, gather.ErrUnavailable) {
		t.Fatalf("err = %v (exit %d), want exit %d wrapping ErrUnavailable", err, exitCodeFor(err), exitTotal)
	}
	if out != "class=transient\n" || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("stdout %q, err %q; want class=transient and an error naming unavailable", out, err)
	}
	after := f.entity("issue", "PROJ-1")
	if after.Version != before.Version || after.Watched != nil || len(f.history("PROJ-1")) != baseHist {
		t.Errorf("a failed refresh wrote: before %+v after %+v", before, after)
	}

	// The next run retries once the daemon is back.
	if err := os.Remove(filepath.Join(f.dir, "down")); err != nil {
		t.Fatal(err)
	}
	if _, err := runRefreshCmd(t, "issue", "PROJ-1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestRefreshWatchProbesOwningInstancesWhenNothingNamesTheBackend(t *testing.T) {
	f := newBackendFixture(t, "bd-7")
	f.write("show-bd-7.json", `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"bd-7","title":"t","updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`)
	// Not ticket-shaped, in no watch set, held by no owning instance: no
	// backend is asked to watch it (no --watch on the read), the read stays
	// unpinned, and the hydration write alone marks it watched.
	if _, err := runRefreshCmd(t, "issue", "bd-7", "--watch"); err != nil {
		t.Fatalf("refresh --watch: %v", err)
	}
	calls := f.calls()
	if len(calls) != 2 || calls[0] != "issue show bd-7 --fresh --backend jira-a" || calls[1] != "issue show bd-7 --fresh" {
		t.Errorf("calls = %q, want one probe and the unpinned read without --watch", calls)
	}
	if e := f.entity("issue", "bd-7"); e.Watched == nil || !*e.Watched || e.Backend != "" {
		t.Errorf("watched = %v backend = %q, want watched true and no pinned backend", e.Watched, e.Backend)
	}
}

// pg-desk issue refresh <beads id> --watch sets the flag in the same write as
// the hydration and appends exactly one added row; a repeat appends no second
// added; without --watch an unwatched beads issue is hydrated and appends no
// row.
func TestRefreshWatchOfABeadsIssueMarksItWatchedAndAppendsOneAdded(t *testing.T) {
	f := newBackendFixture(t, "bd-5", "bd-6")
	for _, id := range []string{"bd-5", "bd-6"} {
		f.write("show-"+id+".json", `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"`+id+`","title":"t","updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`)
	}
	addedRows := func(id string) int {
		n := 0
		for _, r := range f.history(id) {
			for _, k := range r.Kinds {
				if k == "added" {
					n++
				}
			}
		}
		return n
	}

	// Without --watch the unwatched issue is hydrated and stays unwatched.
	if _, err := runRefreshCmd(t, "issue", "bd-6"); err != nil {
		t.Fatal(err)
	}
	if e := f.entity("issue", "bd-6"); e.Watched != nil {
		t.Errorf("a plain refresh set watched = %v", *e.Watched)
	}

	base := addedRows("bd-5")
	if _, err := runRefreshCmd(t, "issue", "bd-5", "--watch"); err != nil {
		t.Fatalf("refresh --watch: %v", err)
	}
	if e := f.entity("issue", "bd-5"); e.Watched == nil || !*e.Watched {
		t.Errorf("watched = %v, want true from the hydration write", e.Watched)
	}
	if got := addedRows("bd-5"); got != base+1 {
		t.Fatalf("added rows = %d, want %d (exactly one new)", got, base+1)
	}
	if _, err := runRefreshCmd(t, "issue", "bd-5", "--watch"); err != nil {
		t.Fatalf("repeat refresh --watch: %v", err)
	}
	if got := addedRows("bd-5"); got != base+1 {
		t.Errorf("added rows = %d after a repeat, want %d", got, base+1)
	}
	for _, c := range f.calls() {
		if strings.Contains(c, "--watch") {
			t.Errorf("call %q asked a backend to watch a beads issue", c)
		}
	}
}
