//go:build contract

package contract

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/analyze"
	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

var (
	bdPath        = flag.String("bd", "", "absolute path of the bd binary under test (required)")
	update        = flag.Bool("update", false, "record testdata/bd fixtures and the VERSION pin")
	testdataFlag  = flag.String("testdata", "../../testdata/bd", "directory the -update flag writes fixtures to")
	childPathFlag = flag.String("child-path", "", "PATH of every bd child, as the real exporter's childPath (colon-separated absolute bash and coreutils bin dirs); empty means an empty directory, which only works for a bd that needs nothing on PATH")
	queuesFlag    = flag.String("queues", "../../../../claude-marketplace/pb/queues.json", "path of the committed queue definitions")
)

func TestMain(m *testing.M) {
	flag.Parse()
	if *bdPath == "" || !filepath.IsAbs(*bdPath) {
		fmt.Fprintln(os.Stderr, "contract: -bd must be the absolute path of a bd binary")
		os.Exit(2)
	}
	if err := validateChildPath(*childPathFlag); err != nil {
		fmt.Fprintf(os.Stderr, "contract: -child-path: %v\n", err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

// validateChildPath mirrors the real exporter's childPath rule (internal/config):
// every colon-separated entry MUST be an absolute directory path. Empty is valid
// and selects the empty-directory default.
func validateChildPath(p string) error {
	if p == "" {
		return nil
	}
	for _, entry := range strings.Split(p, ":") {
		if !filepath.IsAbs(entry) {
			return fmt.Errorf("entries must be absolute, got %q", entry)
		}
	}
	return nil
}

const fixtureActor = "fixture-actor"

// env is one throwaway embedded beads database.
type env struct {
	t        *testing.T
	root     string
	home     string
	ws       string
	beadsDir string
	prefix   string
	emptyBin string
}

// newEnv creates an isolated embedded database with a per-run unique prefix.
// It FAILS (never skips) when the database is not truly embedded, because a
// server-mode database would make the whole suite meaningless.
func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	e := &env{
		t:        t,
		root:     root,
		home:     filepath.Join(root, "h"),
		ws:       filepath.Join(root, "ws"),
		prefix:   "bx" + hex.EncodeToString(b[:]),
		emptyBin: filepath.Join(root, "emptybin"),
	}
	e.beadsDir = filepath.Join(e.ws, ".beads")
	for _, d := range []string{e.home, e.ws, e.emptyBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.mustBD("init", "--prefix", e.prefix)
	e.assertEmbedded()
	return e
}

func (e *env) assertEmbedded() {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.beadsDir, "metadata.json"))
	if err != nil {
		e.t.Fatalf("metadata.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		e.t.Fatal(err)
	}
	if meta["dolt_mode"] != "embedded" {
		e.t.Fatalf("temp database is not embedded (dolt_mode = %v): the suite must not run against a server", meta["dolt_mode"])
	}
	for k := range meta {
		if strings.HasPrefix(k, "dolt_server") {
			e.t.Fatalf("metadata.json carries %s: the temp database is not truly embedded", k)
		}
	}
	if !strings.HasPrefix(e.prefix, "bx") || len(e.prefix) != 10 {
		e.t.Fatalf("unexpected prefix %q", e.prefix)
	}
}

// writeEnv is the environment of setup (write) commands.
func (e *env) writeEnv() []string {
	return []string{
		"HOME=" + e.home,
		"PATH=" + os.Getenv("PATH"),
		"BEADS_DIR=" + e.beadsDir,
		"BEADS_ACTOR=" + fixtureActor,
		"BD_NON_INTERACTIVE=1",
		"BD_JSON_ENVELOPE=1",
		"BEADS_DOLT_AUTO_START=0",
		"BD_BACKUP_ENABLED=0",
	}
}

func (e *env) bdCmd(stdin string, args ...string) (stdout, stderr string, code int) {
	e.t.Helper()
	cmd := exec.Command(*bdPath, args...)
	cmd.Dir = e.ws
	cmd.Env = e.writeEnv()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out.String(), errb.String(), ee.ExitCode()
		}
		e.t.Fatalf("run bd %v: %v", args, err)
	}
	return out.String(), errb.String(), 0
}

func (e *env) mustBD(args ...string) string {
	e.t.Helper()
	out, errs, code := e.bdCmd("", args...)
	if code != 0 {
		e.t.Fatalf("bd %v exited %d:\n%s\n%s", args, code, out, errs)
	}
	return out
}

func (e *env) mustImport(lines ...map[string]any) {
	e.t.Helper()
	var sb strings.Builder
	for _, l := range lines {
		raw, err := json.Marshal(l)
		if err != nil {
			e.t.Fatal(err)
		}
		sb.Write(raw)
		sb.WriteByte('\n')
	}
	out, errs, code := e.bdCmd(sb.String(), "import", "-")
	if code != 0 {
		e.t.Fatalf("bd import exited %d:\n%s\n%s", code, out, errs)
	}
}

func (e *env) id(suffix string) string { return e.prefix + "-" + suffix }

// created returns the id of a freshly created bead.
func (e *env) created(args ...string) string {
	e.t.Helper()
	out := e.mustBD(append([]string{"create", "--json"}, args...)...)
	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Data.ID == "" {
		e.t.Fatalf("create %v: no id in %s (%v)", args, out, err)
	}
	return env.Data.ID
}

// childPath is the PATH of every bd child. By default it is an empty directory:
// bd must run without git, bash or coreutils. A bd that is a shell wrapper (the
// machine's wrapped bd) needs bash and coreutils, so -child-path supplies the
// same bash+coreutils PATH the real exporter's childPath carries; git stays off.
func (e *env) childPath() string {
	if *childPathFlag != "" {
		return *childPathFlag
	}
	return e.emptyBin
}

// client is the production adapter pointed at the temp database, running bd
// with childPath() as its PATH.
func (e *env) client() *bd.Client {
	return bd.NewClient(bd.ClientConfig{
		BDPath:    *bdPath,
		BeadsDir:  e.beadsDir,
		Home:      e.home,
		ChildPath: e.childPath(),
		Timeout:   2 * time.Minute,
	})
}

// raw runs argv exactly as the production adapter would and returns stdout.
func (e *env) raw(argv []string) (string, int) {
	e.t.Helper()
	res, err := bd.ExecRunner{}.Run(context.Background(), bd.Cmd{
		Path: *bdPath, Args: argv, Env: bd.ChildEnv(e.home, e.childPath(), e.beadsDir),
	})
	if err != nil {
		e.t.Fatalf("run %v: %v", argv, err)
	}
	return string(res.Stdout), res.ExitCode
}

func ids(beads []bd.Bead) []string {
	out := make([]string, 0, len(beads))
	for _, b := range beads {
		out = append(out, b.ID)
	}
	sort.Strings(out)
	return out
}

func issue(id, typ, status string, labels []string, extra map[string]any) map[string]any {
	m := map[string]any{
		"id": id, "title": "title " + id, "status": status, "priority": 2, "issue_type": typ,
		"created_at": "2026-01-02T03:04:05Z", "updated_at": "2026-01-02T03:04:05Z",
	}
	if len(labels) > 0 {
		m["labels"] = labels
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestClientSideQueuesMatchBDReady(t *testing.T) {
	e := newEnv(t)
	labelSets := [][]string{{"a"}, {"b"}, {"a", "b"}, {"c"}, {"human"}, {"a", "human"}, {"b", "c"}, nil}
	types := []string{"task", "bug", "epic", "chore"}
	var lines []map[string]any
	for i := 0; i < 130; i++ {
		lines = append(lines, issue(e.id(fmt.Sprintf("r%03d", i)), types[i%len(types)], "open", labelSets[i%len(labelSets)], nil))
	}
	tmpl := e.id("tpl")
	lines = append(lines, issue(tmpl, "task", "open", []string{"a"}, map[string]any{"is_template": true}))
	e.mustImport(lines...)

	c := e.client()
	ctx := context.Background()
	all, err := c.Ready(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) <= 100 {
		t.Fatalf("bd ready -n 0 returned %d beads; the >100 case needs more than the default limit", len(all))
	}
	// The reason templates are dropped client-side: bd ready returns them.
	var sawTemplate bool
	for _, b := range all {
		if b.ID == tmpl {
			sawTemplate = b.IsTemplate
		}
	}
	if !sawTemplate {
		t.Fatalf("bd ready no longer returns templates with is_template set; the client-side drop is now questionable")
	}

	cases := map[string][]string{
		"no filter":              nil,
		"label":                  {"--label", "a"},
		"label comma is AND":     {"--label", "a,b"},
		"label repeated is AND":  {"--label", "a", "--label", "b"},
		"label equals form":      {"--label=a"},
		"exclude-label":          {"--exclude-label", "human"},
		"exclude-label comma":    {"--exclude-label", "c,human"},
		"exclude-label repeated": {"--exclude-label", "c", "--exclude-label", "human"},
		"exclude-type":           {"--exclude-type", "epic"},
		"exclude-type comma":     {"--exclude-type", "epic,bug"},
		"combined":               {"--label", "a", "--exclude-label", "human", "--exclude-type", "epic"},
	}
	// The committed queue definitions are cases too.
	raw, err := os.ReadFile(*queuesFlag)
	if err != nil {
		t.Fatalf("read committed queues: %v", err)
	}
	var specs []struct {
		Name string   `json:"name"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &specs); err != nil {
		t.Fatal(err)
	}
	for _, s := range specs {
		cases["committed queue "+s.Name] = s.Args
	}

	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			q, err := queue.New("q", args)
			if err != nil {
				t.Fatal(err)
			}
			if q.Class != queue.ClientSide {
				t.Fatalf("case %v is not client-side", args)
			}
			got := ids(q.Filter().Apply(all))
			serverSide, err := c.Ready(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			want := ids(queue.DropTemplates(serverSide))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("client-side %v = %d beads, bd ready %v = %d beads\nonly client: %v\nonly bd: %v",
					args, len(got), args, len(want), diff(got, want), diff(want, got))
			}
		})
	}
}

func diff(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	return out
}

func TestClosedAfterReturnsJustClosedAndExcludesGates(t *testing.T) {
	e := newEnv(t)
	oldClosed := e.id("old")
	e.mustImport(issue(oldClosed, "task", "closed", nil, map[string]any{
		"closed_at": "2020-01-01T00:00:00Z", "created_at": "2020-01-01T00:00:00Z",
	}))
	justClosed := e.created("just closed", "-t", "task")
	e.mustBD("close", justClosed)
	gate := e.created("a gate", "-t", "gate")
	e.mustBD("close", gate)
	stillOpen := e.created("still open", "-t", "task")

	c := e.client()
	ctx := context.Background()
	since := time.Now().Add(-time.Hour)

	closed, err := c.List(ctx, bd.ListOpts{All: true, ClosedAfter: since})
	if err != nil {
		t.Fatal(err)
	}
	got := ids(closed)
	if !reflect.DeepEqual(got, []string{justClosed}) {
		t.Fatalf("--all --closed-after = %v, want exactly [%s] (not the 2020 bead %s, the closed gate %s, or the open %s)",
			got, justClosed, oldClosed, gate, stillOpen)
	}

	created, err := c.List(ctx, bd.ListOpts{All: true, CreatedAfter: since})
	if err != nil {
		t.Fatal(err)
	}
	gotCreated := ids(created)
	for _, want := range []string{justClosed, stillOpen} {
		found := false
		for _, id := range gotCreated {
			found = found || id == want
		}
		if !found {
			t.Fatalf("--all --created-after = %v, missing %s", gotCreated, want)
		}
	}
	for _, id := range gotCreated {
		if id == oldClosed || id == gate {
			t.Fatalf("--all --created-after returned %s", id)
		}
	}

	// Without --all the closed bead is invisible: the reason --all is required.
	def, err := c.List(ctx, bd.ListOpts{ClosedAfter: since})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range def {
		if b.ID == justClosed {
			t.Fatal("closed bead visible without --all")
		}
	}

	// count --by-status includes the closed gate, which is why the exporter says so.
	counts, err := c.CountByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["closed"] != 3 {
		t.Fatalf("closed count = %d, want 3 (old, just closed, closed gate): %v", counts["closed"], counts)
	}
}

func TestDefaultListExcludesGatesWispsAndTemplates(t *testing.T) {
	e := newEnv(t)
	plain := e.created("plain", "-t", "task")
	gate := e.created("gate", "-t", "gate")
	wisp := e.created("wisp", "--ephemeral")
	tmpl := e.id("tpl")
	e.mustImport(issue(tmpl, "task", "open", nil, map[string]any{"is_template": true}))

	got, err := e.client().List(context.Background(), bd.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, id := range ids(got) {
		have[id] = true
	}
	if !have[plain] {
		t.Fatalf("plain bead missing from the default list: %v", ids(got))
	}
	for name, id := range map[string]string{"gate": gate, "wisp": wisp, "template": tmpl} {
		if have[id] {
			t.Fatalf("default list includes the %s %s", name, id)
		}
	}
	// The statuses call reports built-in statuses, and a configured custom one.
	e.mustBD("config", "set", "status.custom", "review:active,archived:done")
	names, err := e.client().Statuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"open", "in_progress", "blocked", "deferred", "closed", "review", "archived"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Fatalf("statuses = %v, missing %s", names, want)
		}
	}
}

// TestReadyExcludedTypesAreExactlyThePinnedSet creates one open, unblocked bead
// of every creatable type and checks which types the default list shows but bd
// ready never returns. That set drives the tracking state.
func TestReadyExcludedTypesAreExactlyThePinnedSet(t *testing.T) {
	e := newEnv(t)
	e.mustBD("config", "set", "types.custom", "merge-request,molecule,gate")
	typeNames := []string{"bug", "feature", "task", "epic", "chore", "decision", "spike", "story", "milestone", "merge-request", "molecule", "event"}
	byType := map[string]string{}
	for _, ty := range typeNames {
		byType[ty] = e.created("type "+ty, "-t", ty)
	}
	c := e.client()
	list, err := c.List(context.Background(), bd.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := c.Ready(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inReady := map[string]bool{}
	for _, b := range ready {
		inReady[b.ID] = true
	}
	var excluded []string
	for _, b := range list {
		if !inReady[b.ID] {
			excluded = append(excluded, b.IssueType)
		}
	}
	sort.Strings(excluded)
	want := append([]string{}, analyze.ReadyExcludedTypes()...)
	sort.Strings(want)
	if !reflect.DeepEqual(excluded, want) {
		t.Fatalf("types listed but never ready = %v, analyze.ReadyExcludedTypes = %v; update the pinned set before trusting the tracking state", excluded, want)
	}
	for _, ty := range want {
		if byType[ty] == "" {
			t.Fatalf("pinned type %s was not exercised by the suite", ty)
		}
	}
}

func TestLimitFlagIsAcceptedByListAndReadyAndRejectedElsewhere(t *testing.T) {
	e := newEnv(t)
	e.created("one", "-t", "task")
	accepted := [][]string{
		{"list", "-n", "0", "--json", "--readonly", "--sandbox"},
		{"ready", "-n", "0", "--json", "--readonly", "--sandbox"},
	}
	for _, argv := range accepted {
		if out, code := e.raw(argv); code != 0 {
			t.Fatalf("bd %v exited %d: %s", argv, code, out)
		}
	}
	rejected := [][]string{
		{"blocked", "-n", "0", "--json", "--readonly", "--sandbox"},
		{"count", "--by-status", "-n", "0", "--json", "--readonly", "--sandbox"},
		{"statuses", "-n", "0", "--json", "--readonly", "--sandbox"},
	}
	for _, argv := range rejected {
		if out, code := e.raw(argv); code == 0 {
			t.Fatalf("bd %v unexpectedly accepted -n: %s", argv, out)
		}
	}
	// The production argv builders never pass -n to the rejecting commands.
	for _, argv := range [][]string{bd.BlockedArgv(), bd.CountByStatusArgv(), bd.StatusesArgv()} {
		if out, code := e.raw(argv); code != 0 {
			t.Fatalf("bd %v exited %d: %s", argv, code, out)
		}
	}
}

func TestEnvelopeShapes(t *testing.T) {
	e := newEnv(t)
	e.created("one", "-t", "task")
	for _, argv := range [][]string{bd.ListArgv(bd.ListOpts{}), bd.ReadyArgv(nil), bd.BlockedArgv(), bd.CountByStatusArgv(), bd.StatusesArgv()} {
		out, code := e.raw(argv)
		if code != 0 {
			t.Fatalf("bd %v exited %d", argv, code)
		}
		var env struct {
			Data          json.RawMessage `json:"data"`
			SchemaVersion *int            `json:"schema_version"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("bd %v: not an envelope: %v\n%s", argv, err, out)
		}
		if env.SchemaVersion == nil || *env.SchemaVersion != 1 || len(env.Data) == 0 {
			t.Fatalf("bd %v: envelope = %s", argv, out)
		}
	}
}

// TestRecordFixtures records the testdata/bd fixtures from a deterministic
// database. It only runs with -update.
func TestRecordFixtures(t *testing.T) {
	if !*update {
		t.Skip("run with -update to record fixtures")
	}
	e := newEnv(t)
	e.mustBD("config", "set", "types.custom", "merge-request,molecule,gate")
	e.mustBD("config", "set", "status.custom", "review:active,archived:done")
	e.mustImport(
		issue(e.id("r1"), "task", "open", []string{"area-a", "human"}, map[string]any{"priority": 1, "assignee": ""}),
		issue(e.id("r2"), "bug", "open", []string{"area-a"}, map[string]any{"priority": 0}),
		issue(e.id("r3"), "epic", "open", nil, nil),
		issue(e.id("mr"), "merge-request", "open", nil, nil),
		issue(e.id("rv"), "task", "review", nil, nil),
		issue(e.id("tpl"), "task", "open", nil, map[string]any{"is_template": true}),
		issue(e.id("dep"), "task", "open", nil, nil),
		issue(e.id("blk"), "task", "open", nil, nil),
		issue(e.id("old"), "task", "closed", nil, map[string]any{"closed_at": "2026-01-03T00:00:00Z"}),
	)
	e.mustBD("update", e.id("r2"), "--claim")
	e.mustBD("defer", e.id("dep"), "--until", "2099-01-01")
	e.mustBD("dep", "add", e.id("blk"), e.id("r1"))

	dir := *testdataFlag
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := func(name string, argv []string) {
		out, code := e.raw(argv)
		if code != 0 {
			t.Fatalf("bd %v exited %d: %s", argv, code, out)
		}
		out = strings.ReplaceAll(out, e.prefix+"-", "alpha-")
		if strings.Contains(out, e.home) || strings.Contains(out, e.root) {
			t.Fatalf("fixture %s leaks a local path", name)
		}
		if u := os.Getenv("USER"); u != "" && strings.Contains(out, u) {
			t.Fatalf("fixture %s leaks the local user name", name)
		}
		var pretty json.RawMessage
		if err := json.Unmarshal([]byte(out), &pretty); err != nil {
			t.Fatalf("fixture %s is not JSON: %v", name, err)
		}
		indented, err := json.MarshalIndent(pretty, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), append(indented, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	record("list.json", bd.ListArgv(bd.ListOpts{}))
	record("list-all.json", bd.ListArgv(bd.ListOpts{All: true, CreatedAfter: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}))
	record("ready.json", bd.ReadyArgv(nil))
	record("blocked.json", bd.BlockedArgv())
	record("count.json", bd.CountByStatusArgv())
	record("statuses.json", bd.StatusesArgv())

	version, _, code := e.bdCmd("", "version")
	if code != 0 {
		t.Fatal("bd version failed")
	}
	m := regexp.MustCompile(`^bd version (\S+)`).FindStringSubmatch(version)
	if m == nil {
		t.Fatalf("cannot parse bd version output %q", version)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(m[1]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateChildPath(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
	}{
		{"", false},
		{"/a/bin", false},
		{"/a/bin:/b/bin", false},
		{"/a/bin:bin", true},
		{"/a/bin::/b/bin", true},
		{"bin", true},
	} {
		if err := validateChildPath(tc.in); (err != nil) != tc.wantErr {
			t.Errorf("validateChildPath(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
	}
}
