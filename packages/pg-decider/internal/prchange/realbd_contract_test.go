//go:build contract

package prchange

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
)

// Real-tracker guard for pg-pr-change's not-found escalation (docket
// pg2-6mo45.3, packet P3b1b).
//
// WHY THIS FILE EXISTS (global rule V-3): the escalation is raised once, and a
// redelivered change MUST find it again even after it was closed. Whether
// `pg-connector search` returns a CLOSED bead, and how its per-source rows read,
// are properties of REAL tools (the connector's search fan-out over the beads
// backend over bd's own search), and a mock of those does not count. These tests
// run the real pg-pr-change, pg-desk and pg-decider against the real
// pg-connector and pg-connector-issue-beads (all built from this repo's source)
// and a real `bd` in a throwaway embedded store. The forge side is still the
// fixture-driven fake (see rig_test.go); only the tracker is real.
//
// Hermetic store: each test runs `bd init` in its own temp directory and asserts
// the resulting .beads/metadata.json says `"dolt_mode": "embedded"`, a private
// on-disk engine owned by that directory. The environment handed to bd and to the
// real connector is an allowlist (PATH, locale, a private HOME and state home,
// BEADS_DOLT_AUTO_START=0), so nothing that selects a git repository or a beads
// workspace (GIT_DIR from a commit hook, BEADS_*, ...) can reach it, and nothing
// can point at this machine's shared dolt server.
//
// Behind the `contract` build tag: it is not part of the unit tier and not part
// of the nix check (the sandbox has no bd). It is run by the
// `pg-pr-change-contract-test` hook (scripts/run-pg-pr-change-contract.sh) at
// commit and under `pg-hooks run pre-land`. A missing bd or go is a LOUD
// failure, never a skip. Run it directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run RealBD ./internal/prchange/

const contractDeadline = 3 * time.Minute

var contractBuild struct {
	once sync.Once
	dir  string
	bins binaries
	conn string // the real pg-connector
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if contractBuild.dir != "" {
		_ = os.RemoveAll(contractBuild.dir)
	}
	os.Exit(code)
}

// moduleDir resolves a sibling module of this repo from this package directory
// (packages/pg-decider/internal/prchange).
func moduleDir(t *testing.T, rel string) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func goBuild(t *testing.T, goBin, modDir, out, pkg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contractDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "build", "-o", out, pkg)
	cmd.Dir = modDir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s in %s: %v\n%s", pkg, modDir, err, b)
	}
}

// buildBinaries builds every binary the real-tracker tests run, once per test
// process, from this repo's current source.
func buildBinaries(t *testing.T) (binaries, string) {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go not on PATH, cannot build the real binaries: %v", err)
	}
	contractBuild.once.Do(func() {
		dir, err := os.MkdirTemp("", "pg-pr-change-contract-*")
		if err != nil {
			contractBuild.err = err
			return
		}
		contractBuild.dir = dir
		bins := filepath.Join(dir, "bin")
		conn := filepath.Join(dir, "connector")
		for _, d := range []string{bins, conn} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				contractBuild.err = err
				return
			}
		}
		goBuild(t, goBin, moduleDir(t, filepath.Join("..", "pg-desk")), filepath.Join(bins, "pg-desk"), "./cmd/pg-desk")
		goBuild(t, goBin, moduleDir(t, "."), filepath.Join(bins, "pg-decider"), "./cmd/pg-decider")
		goBuild(t, goBin, moduleDir(t, "."), filepath.Join(bins, "pg-pr-change"), "./cmd/pg-pr-change")
		for _, n := range []string{"pg-connector", "pg-connector-issue-beads"} {
			goBuild(t, goBin, moduleDir(t, filepath.Join("..", "pg-connector")), filepath.Join(conn, n), "./cmd/"+n)
		}
		contractBuild.bins = binaries{desk: filepath.Join(bins, "pg-desk"), decider: filepath.Join(bins, "pg-decider"), change: filepath.Join(bins, "pg-pr-change")}
		contractBuild.conn = conn
	})
	if contractBuild.err != nil {
		t.Fatal(contractBuild.err)
	}
	return contractBuild.bins, contractBuild.conn
}

// realBD is one throwaway beads workspace and the environment bd and the real
// connector run under.
type realBD struct {
	t      *testing.T
	bd     string
	dir    string
	env    []string
	prefix string
}

func newRealBD(t *testing.T) *realBD {
	t.Helper()
	bd, err := exec.LookPath("bd")
	if err != nil {
		t.Fatalf("bd not on PATH: the real-tracker tests need it (this is a failure, never a skip): %v", err)
	}
	f := &realBD{t: t, bd: bd, dir: t.TempDir(), prefix: "pc"}
	home := t.TempDir()
	f.env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_STATE_HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
		"BEADS_DOLT_AUTO_START=0",
		"BD_JSON_ENVELOPE=1",
	}
	for _, k := range []string{"LANG", "LC_ALL", "LC_CTYPE", "TERM"} {
		if v := os.Getenv(k); v != "" {
			f.env = append(f.env, k+"="+v)
		}
	}
	f.run("init", "--prefix", f.prefix, "--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	meta, err := os.ReadFile(filepath.Join(f.dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("read bd metadata: %v", err)
	}
	if !strings.Contains(string(meta), `"dolt_mode": "embedded"`) {
		t.Fatalf("bd init did not produce a private embedded store, refusing to continue:\n%s", meta)
	}
	return f
}

func (f *realBD) run(args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contractDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bd, args...)
	cmd.Dir, cmd.Env = f.dir, f.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("bd %v: %v\n%s%s", args, err, out.String(), errb.String())
	}
	return out.String()
}

// create files a bead and returns its id. The metadata is what links the anchor
// to its PR in pg-desk.
func (f *realBD) create(title, metadata string) string {
	f.t.Helper()
	args := []string{"create", title, "-t", "task", "--silent"}
	if metadata != "" {
		args = append(args, "--metadata", metadata)
	}
	lines := strings.Split(strings.TrimSpace(f.run(args...)), "\n")
	id := strings.TrimSpace(lines[len(lines)-1])
	if id == "" || strings.Contains(id, " ") {
		f.t.Fatalf("bd create %q: cannot read the new id from %q", title, lines)
	}
	return id
}

type bdHit struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// search is bd's own text search, every status, as JSON.
func (f *realBD) search(query string) []bdHit {
	f.t.Helper()
	var w struct {
		Data []bdHit `json:"data"`
	}
	if err := json.Unmarshal([]byte(f.run("search", query, "--json")), &w); err != nil {
		f.t.Fatalf("decode bd search: %v", err)
	}
	return w.Data
}

// exactCount is how many beads of any status carry exactly this title.
func (f *realBD) exactCount(title string) int {
	n := 0
	for _, h := range f.search(title) {
		if h.Title == title {
			n++
		}
	}
	return n
}

// realRig builds the rig whose tracker verbs reach the real connector over
// bd's workspace, with the anchor of PR 7 a real bead.
func realRig(t *testing.T, f *realBD, anchor string) *rig {
	t.Helper()
	bins, connDir := buildBinaries(t)
	registry := filepath.Join(t.TempDir(), "config.yaml")
	registryYAML := "connector:\n  issue:\n    - name: beads\n      command: [pg-connector-issue-beads]\nsearch:\n  sources: [pg-connector-issue-beads]\n"
	must(t, os.WriteFile(registry, []byte(registryYAML), 0o600))
	deciderCfg := filepath.Join(t.TempDir(), "decider.json")
	cfgJSON, err := json.Marshal(map[string]string{"agent_tracker_backend": "beads", "beads_dir": f.dir})
	must(t, err)
	must(t, os.WriteFile(deciderCfg, cfgJSON, 0o600))

	realEnv := []string{
		"PATH=" + connDir + string(os.PathListSeparator) + filepath.Dir(f.bd) + string(os.PathListSeparator) + "/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"XDG_STATE_HOME=" + t.TempDir(),
		"PG_PR_CONFIG=" + registry,
		"PG_CONNECTOR_ISSUE_BEADS_DIR=" + f.dir,
		"BEADS_DOLT_AUTO_START=0",
		"BD_JSON_ENVELOPE=1",
	}
	fx := fmt.Sprintf(`{
  "name": "real-anchor",
  "description": "An open PR of mine whose anchor is a real bead.",
  "entities": ["acme/api#7"],
  "prs": [{"number": 7, "title": "Add the export command", "author": "phillipgreenii"}],
  "beads": [{"id": %q, "title": "acme/api#7: Add the export command",
    "metadata": {"repo": "acme/api", "pr_number": "7", "state": "open", "branch": "feature-7", "base": "main", "author": "phillipgreenii", "draft": "false"}}]
}`, anchor)
	r := newRigWith(t, fx, rigOpts{
		cutover: true,
		bins:    &bins,
		real:    &realTracker{connector: filepath.Join(connDir, "pg-connector"), env: realEnv},
		env:     []string{"PG_DECIDER_CONFIG=" + deciderCfg},
	})
	r.hydrate()
	r.ctlWrite("fail.pr-show", "not_found")
	return r
}

const anchorMetadata = `{"repo":"acme/api","pr_number":"7"}`

var escalatedAs = regexp.MustCompile(`escalated as (\S+):`)

func TestPRChangeRealBD_EscalationDedupSurvivesAClosedFirstEscalation(t *testing.T) {
	f := newRealBD(t)
	anchor := f.create("acme/api#7: Add the export command", anchorMetadata)
	r := realRig(t, f, anchor)
	title := EscalationTitle(id7)

	// First delivery: one escalation, created by the real tracker.
	res := r.change("apply", id7)
	if res.code != ExitHydrateNotFound {
		t.Fatalf("first delivery: exit %d:\n%s", res.code, res.stderr)
	}
	m := escalatedAs.FindStringSubmatch(res.stderr)
	if m == nil {
		t.Fatalf("first delivery raised no escalation:\n%s", res.stderr)
	}
	esc := m[1]
	if n := f.exactCount(title); n != 1 {
		t.Fatalf("after the first delivery the tracker holds %d escalations titled %q, want 1", n, title)
	}
	hits := f.search(title)
	if len(hits) != 1 || hits[0].ID != esc || !contains2(hits[0].Labels, "human") {
		t.Fatalf("the escalation is not the one reported, or lacks the human label: %+v (reported %s)", hits, esc)
	}
	if !strings.HasPrefix(esc, anchor+".") {
		t.Errorf("the escalation %s is not parented under the anchor %s", esc, anchor)
	}

	// A redelivered change finds it open.
	res = r.change("apply", id7+"@0123456789ab")
	if res.code != ExitHydrateNotFound {
		t.Fatalf("redelivery: exit %d:\n%s", res.code, res.stderr)
	}
	contains(t, "redelivery line", res.stderr, "already escalated as "+esc)
	if n := f.exactCount(title); n != 1 {
		t.Fatalf("a redelivery raised a second escalation: %d titled %q", n, title)
	}

	// And finds it after it was CLOSED: the lookup spans every status.
	f.run("close", esc, "--reason", "handled by hand")
	if got := f.search(title); len(got) != 1 || got[0].Status != "closed" {
		t.Fatalf("setup: the escalation is not closed: %+v", got)
	}
	res = r.change("apply", id7)
	if res.code != ExitHydrateNotFound {
		t.Fatalf("redelivery after close: exit %d:\n%s", res.code, res.stderr)
	}
	contains(t, "redelivery line", res.stderr, "already escalated as "+esc)
	if n := f.exactCount(title); n != 1 {
		t.Fatalf("a redelivery after the first escalation was closed raised a second one: %d titled %q", n, title)
	}
}

func TestPRChangeRealBD_AnEscalationOfAnotherPRWithASimilarTitleDoesNotCount(t *testing.T) {
	f := newRealBD(t)
	anchor := f.create("acme/api#7: Add the export command", anchorMetadata)
	// PR 70's escalation contains PR 7's whole title as a substring, so the real
	// search returns it for PR 7's query; only the exact-title match tells them apart.
	other := f.create(EscalationTitle("acme/api#70"), "")
	r := realRig(t, f, anchor)
	title := EscalationTitle(id7)

	if got := f.search(title); len(got) != 1 || got[0].ID != other {
		t.Fatalf("setup: the real search of %q does not return the similar title: %+v", title, got)
	}
	res := r.change("apply", id7)
	if res.code != ExitHydrateNotFound {
		t.Fatalf("exit %d:\n%s", res.code, res.stderr)
	}
	if !escalatedAs.MatchString(res.stderr) {
		t.Fatalf("PR 7 was not escalated because PR 70's escalation was taken for its own:\n%s", res.stderr)
	}
	if n := f.exactCount(title); n != 1 {
		t.Fatalf("the tracker holds %d escalations titled %q, want 1", n, title)
	}
}

func TestPRChangeRealBD_ATrackerThatCannotBeReadIsTransientAndWritesNothing(t *testing.T) {
	f := newRealBD(t)
	anchor := f.create("acme/api#7: Add the export command", anchorMetadata)
	r := realRig(t, f, anchor)
	// Break the tracker after setup: the open-anchor probe and the search of the
	// real connector then fail, and neither may be read as "no escalation yet".
	must(t, os.RemoveAll(filepath.Join(f.dir, ".beads")))

	res := r.change("apply", id7)
	if res.code != ExitHydrateTransient {
		t.Fatalf("exit %d, want %d:\n%s", res.code, ExitHydrateTransient, res.stderr)
	}
	contains(t, "line", res.stderr, "class=transient", "unavailable")
	if len(r.deciderRuns()) != 0 {
		t.Error("the decider ran")
	}
}

func contains2(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// createTyped files a bead of an issue type and returns its id.
func (f *realBD) createTyped(title, issueType, metadata string) string {
	f.t.Helper()
	args := []string{"create", title, "-t", issueType, "--silent"}
	if metadata != "" {
		args = append(args, "--metadata", metadata)
	}
	lines := strings.Split(strings.TrimSpace(f.run(args...)), "\n")
	id := strings.TrimSpace(lines[len(lines)-1])
	if id == "" || strings.Contains(id, " ") {
		f.t.Fatalf("bd create %q: cannot read the new id from %q", title, lines)
	}
	return id
}

// The backfill driver's `enumerate` against the real tracker (docket pg2-6mo45.3,
// packet P4b). Whether the named query lists the right beads, and what the real
// connector's listing looks like to the driver (the entity fields, the sources, the
// truncation flag), are properties of REAL tools: the real pg-connector over the
// real pg-connector-issue-beads over a real bd in a throwaway embedded store. Only
// the `pr list` of the old backend is a double (there is no forge here): a script in
// front of the real connector answers it and hands everything else on.
func TestPRChangeRealBD_BackfillEnumerateListsTheRealTrackersOpenAnchors(t *testing.T) {
	f := newRealBD(t)
	f.run("config", "set", "types.custom", "merge-request")
	open := f.createTyped("acme/api#10: Add the export command", "merge-request", `{"state":"open"}`)
	merged := f.createTyped("acme/api#11: Tidy the readme", "merge-request", `{"state":"merged"}`)
	f.createTyped("review-pr: acme/api#10", "task", "")
	weird := f.createTyped("an anchor whose title names no PR", "merge-request", "")
	closed := f.createTyped("acme/api#12: Closed long ago", "merge-request", "")
	f.run("close", closed, "--reason", "merged")

	_, connDir := buildBinaries(t)
	registry := filepath.Join(t.TempDir(), "config.yaml")
	registryYAML := `connector:
  issue:
    - name: beads
      command: [pg-connector-issue-beads]
backends:
  beads:
    queries:
      work-beads:
        - "list --type merge-request --status open,in_progress,blocked"
        - "list --type task --title-contains review-pr: --status open,in_progress,blocked"
`
	must(t, os.WriteFile(registry, []byte(registryYAML), 0o600))
	shim := filepath.Join(t.TempDir(), "pg-connector")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n\"pr list\") printf '%s\\n' '{\"entities\":[],\"present_ids\":[\"acme/api#10\",\"acme/api#20\"],\"sources\":[{\"source\":\"pr\",\"status\":\"succeeded\",\"count\":2}],\"truncated\":false}';;\n*) exec " + shq(filepath.Join(connDir, "pg-connector")) + " \"$@\";;\nesac\n"
	must(t, os.WriteFile(shim, []byte(script), 0o755))
	queries := filepath.Join(t.TempDir(), "queries")
	must(t, os.WriteFile(queries, []byte("mine\n"), 0o600))

	set, err := backfill.Enumerate(context.Background(), backfill.EnumerateOptions{
		QueriesFile: queries, AnchorsQuery: "work-beads", Backend: "beads", BeadsDir: f.dir, Connector: shim,
		Env: []string{
			"PATH=" + connDir + string(os.PathListSeparator) + filepath.Dir(f.bd) + string(os.PathListSeparator) + "/usr/bin:/bin",
			"HOME=" + t.TempDir(), "XDG_STATE_HOME=" + t.TempDir(), "PG_PR_CONFIG=" + registry,
			"BEADS_DOLT_AUTO_START=0", "BD_JSON_ENVELOPE=1",
		},
	})
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	byPR := map[string]backfill.Entry{}
	for _, e := range set.Entries {
		byPR[e.PR] = e
	}
	if len(byPR) != 3 {
		t.Fatalf("entries %v, want #10, #11 and #20", set.IDs())
	}
	if e := byPR["acme/api#10"]; !e.Has(backfill.FromAnchor) || !e.Has(backfill.FromQuery) || e.State != backfill.StateOpen || len(e.AnchorIDs) != 1 || e.AnchorIDs[0] != open {
		t.Errorf("#10: %+v (anchor %s)", e, open)
	}
	if e := byPR["acme/api#11"]; !e.Has(backfill.FromAnchor) || e.Has(backfill.FromQuery) || e.State != backfill.StateMerged || e.AnchorIDs[0] != merged {
		t.Errorf("#11, an anchor whose PR is merged: %+v (anchor %s)", e, merged)
	}
	if e := byPR["acme/api#20"]; e.Has(backfill.FromAnchor) || !e.Has(backfill.FromQuery) {
		t.Errorf("#20: %+v", e)
	}
	// The anchor whose title maps to no PR is reported, the closed one and the child are not anchors.
	if len(set.Unparseable) != 1 || set.Unparseable[0].ID != weird || set.Unparseable[0].From != backfill.FromAnchor {
		t.Errorf("unparseable %+v", set.Unparseable)
	}
	if set.OpenAnchorCount != 3 || set.OpenAnchorPRCount != 2 || set.QueryPRCount != 2 {
		t.Errorf("open anchors %d, anchor PRs %d, query PRs %d", set.OpenAnchorCount, set.OpenAnchorPRCount, set.QueryPRCount)
	}
}
