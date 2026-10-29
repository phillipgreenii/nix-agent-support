package prchange

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	_ "github.com/phillipgreenii/pg-decider/internal/rules" // registers the decider's rules
)

// The world of the PR lane's end-to-end scenarios (docket pg2-6mo45.3, packets
// P4c-1 and P4c-2; bead pg2-s4xeju split the runs). One set of scenarios
// (lane_scenarios_test.go, lane_review_test.go, lane_race_test.go,
// lane_rollback_test.go) runs in two ways:
//
//   - TestLaneFake (lane_fake_test.go, untagged): over the fake bd of
//     internal/fakebd, every independent scenario in parallel. It takes seconds,
//     and its nix check pg-decider-lane-fake is the automatic run.
//   - TestLaneE2E (lane_e2e_test.go, build tag laneE2E): over a real bd in a
//     throwaway embedded store, one scenario after another. It is the soak:
//     every bd call is a cold start of an embedded dolt engine, so it takes tens
//     of minutes (84 on a loaded host), and it runs only by hand
//     (scripts/run-pg-decider-lane-e2e.sh, or `nix run .#pg-decider-lane-e2e-soak`).
//
// What is real in both: pg-desk (a schema-2 store), pg-decider, pg-pr-change,
// pg-pr-backfill, the umbrella pg-connector and the beads backend
// pg-connector-issue-beads. Every `issue ...` and `search` call of every process
// reaches the tracker through them, so the ground truth of each scenario is the
// tracker's own listing (`bd list`), not a log of what a double was asked. What
// is not real is the forge: the pg-connector `pr ...` reads are answered by the
// fixture-driven fake the other real-tool tests share (rig_test.go).
//
// The binaries come from the environment (the nix check exports the ones nix
// built; the script builds them from source and exports the same names):
//
//	PG_PR_CHANGE_PG_DESK_BIN, PG_PR_CHANGE_PG_DECIDER_BIN, PG_PR_CHANGE_BIN
//	PG_PR_CHANGE_SQLITE3_BIN                          (optional; PATH otherwise)
//	PG_LANE_E2E_PG_PR_BACKFILL_BIN                    (the rollback drill's driver)
//	PG_LANE_E2E_PG_CONNECTOR_BIN, PG_LANE_E2E_PG_CONNECTOR_ISSUE_BEADS_BIN
//	PG_PR_CHANGE_REQUIRE_BINARIES                     (set: a missing tool FAILS)
//
// A missing tool is a LOUD failure whenever PG_PR_CHANGE_REQUIRE_BINARIES is set
// (the checks and the script set it), never a skip; without it the test skips,
// like the other real-tool tests, so a plain `go test ./...` stays hermetic.
const (
	envLaneConnectorBin      = "PG_LANE_E2E_PG_CONNECTOR_BIN"
	envLaneConnectorBeadsBin = "PG_LANE_E2E_PG_CONNECTOR_ISSUE_BEADS_BIN"
	envLaneBackfillBin       = "PG_LANE_E2E_PG_PR_BACKFILL_BIN"
)

// e2eSeeded is N, the number of PRs whose keyless anchors the adoption
// scenarios seed.
const e2eSeeded = 3

type e2eTools struct {
	bins      binaries
	connector string // the real pg-connector
	backfill  string // the real pg-pr-backfill (the rollback drill)
	beads     string // the real pg-connector-issue-beads
	bd        string // the real bd, or the fake one
	git       string // the real git ("" with the fake bd, which needs none)
	fake      bool   // bd is internal/fakebd
}

func e2eNeed(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if v := os.Getenv(envRequireBins); v != "" && v != "0" {
		t.Fatalf("%s: %v (this is a failure, never a skip, because %s is set)", what, err, envRequireBins)
	}
	t.Skipf("%s: %v: skipping the end-to-end lane scenarios", what, err)
}

// laneTools is every tool of the lane but the tracker: the real pg-desk,
// pg-decider, pg-pr-change, pg-pr-backfill and connector binaries.
func laneTools(t *testing.T) e2eTools {
	t.Helper()
	tools := e2eTools{bins: realBinaries(t)}
	for _, e := range []struct {
		env string
		dst *string
	}{{envLaneConnectorBin, &tools.connector}, {envLaneConnectorBeadsBin, &tools.beads}, {envLaneBackfillBin, &tools.backfill}} {
		var err error
		p := os.Getenv(e.env)
		switch {
		case p == "":
			err = fmt.Errorf("%s is not set", e.env)
		case !filepath.IsAbs(p):
			err = fmt.Errorf("%s is not an absolute path", e.env)
		case !isExec(p):
			err = fmt.Errorf("%s (%s) is not an executable file", e.env, p)
		}
		e2eNeed(t, "the real connector", err)
		*e.dst = p
	}
	return tools
}

// connectorDir is a directory holding the two connector binaries under the
// names the umbrella looks its backend up by.
func (tools e2eTools) connectorDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must(t, os.Symlink(tools.connector, filepath.Join(dir, "pg-connector")))
	must(t, os.Symlink(tools.beads, filepath.Join(dir, "pg-connector-issue-beads")))
	return dir
}

// toolDirs is the PATH entries of bd and git ("" entries left out), then the
// system directories.
func (tools e2eTools) toolDirs() []string {
	var out []string
	for _, p := range []string{tools.bd, tools.git} {
		if p != "" {
			out = append(out, filepath.Dir(p))
		}
	}
	return append(out, "/usr/bin", "/bin")
}

// ---- the tracker ------------------------------------------------------------

// tracker is one throwaway beads workspace (bd's embedded mode, or the fake's
// JSON store, in a private directory) and the environment bd and the real
// connector run under.
type tracker struct {
	t   *testing.T
	bd  string
	dir string
	env []string
}

func e2eEnv(tools e2eTools, extra ...string) []string {
	env := []string{
		"PATH=" + strings.Join(tools.toolDirs(), string(os.PathListSeparator)),
		"BEADS_DOLT_AUTO_START=0",
		"BD_JSON_ENVELOPE=1",
		"GIT_AUTHOR_NAME=lane-e2e", "GIT_AUTHOR_EMAIL=lane-e2e@example.invalid",
		"GIT_COMMITTER_NAME=lane-e2e", "GIT_COMMITTER_EMAIL=lane-e2e@example.invalid",
	}
	for _, k := range []string{"LANG", "LC_ALL", "LC_CTYPE", "TERM"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

func newTracker(t *testing.T, tools e2eTools) *tracker {
	t.Helper()
	tr := &tracker{t: t, bd: tools.bd, dir: t.TempDir()}
	tr.env = e2eEnv(tools, "HOME="+t.TempDir(), "XDG_STATE_HOME="+t.TempDir(), "TMPDIR="+t.TempDir())
	tr.run("init", "--prefix", "pc", "--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	if tools.fake {
		if _, err := os.Stat(filepath.Join(tr.dir, ".beads", "fake-bd.json")); err != nil {
			t.Fatalf("the fake bd's init made no store, refusing to continue: %v", err)
		}
	} else {
		meta, err := os.ReadFile(filepath.Join(tr.dir, ".beads", "metadata.json"))
		if err != nil {
			t.Fatalf("read bd metadata: %v", err)
		}
		if !strings.Contains(string(meta), `"dolt_mode": "embedded"`) {
			t.Fatalf("bd init did not produce a private embedded store, refusing to continue:\n%s", meta)
		}
	}
	tr.run("config", "set", "types.custom", "merge-request")
	return tr
}

func (tr *tracker) run(args ...string) string {
	tr.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contractTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tr.bd, args...)
	cmd.Dir, cmd.Env = tr.dir, tr.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		tr.t.Fatalf("bd %v: %v\n%s%s", args, err, out.String(), errb.String())
	}
	return out.String()
}

// contractTimeout bounds one tracker command.
const contractTimeout = 3 * time.Minute

// create files a bead and returns its id.
func (tr *tracker) create(title, typ, parent string, md map[string]string) string {
	tr.t.Helper()
	args := []string{"create", title, "-t", typ, "--silent"}
	if parent != "" {
		args = append(args, "--parent", parent)
	}
	if len(md) > 0 {
		b, err := json.Marshal(md)
		must(tr.t, err)
		args = append(args, "--metadata", string(b))
	}
	lines := strings.Split(strings.TrimSpace(tr.run(args...)), "\n")
	id := strings.TrimSpace(lines[len(lines)-1])
	if id == "" || strings.Contains(id, " ") {
		tr.t.Fatalf("bd create %q: cannot read the new id from %q", title, lines)
	}
	return id
}

// items is every bead of the tracker in every status, as bd lists them.
func (tr *tracker) items() []map[string]any {
	tr.t.Helper()
	return decodeItems(tr.t, tr.run("list", "--all", "--limit", "0", "--json"))
}

func decodeItems(t *testing.T, out string) []map[string]any {
	t.Helper()
	var w struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &w); err != nil {
		t.Fatalf("decode bd list: %v\n%s", err, out)
	}
	return w.Data
}

// snapshot is every bead as canonical JSON by id: it differs after any write
// (a field, a label, a comment count, an updated_at).
func (tr *tracker) snapshot() map[string]string {
	tr.t.Helper()
	out := map[string]string{}
	for _, it := range tr.items() {
		b, err := json.Marshal(it)
		must(tr.t, err)
		out[it["id"].(string)] = string(b)
	}
	return out
}

func sameSnapshot(a, b map[string]string) string {
	var diff []string
	for id, v := range b {
		if old, ok := a[id]; !ok {
			diff = append(diff, "created "+id)
		} else if old != v {
			diff = append(diff, "changed "+id)
		}
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			diff = append(diff, "removed "+id)
		}
	}
	sort.Strings(diff)
	return strings.Join(diff, ", ")
}

func metaOf(it map[string]any) map[string]any {
	m, _ := it["metadata"].(map[string]any)
	return m
}

func itemKind(it map[string]any) string {
	title, _ := it["title"].(string)
	typ, _ := it["issue_type"].(string)
	switch {
	case typ == "merge-request":
		return "anchor"
	case strings.HasPrefix(title, "review-pr:"):
		return "review-pr"
	case strings.HasPrefix(title, "process-feedback:"):
		return "process-feedback"
	case strings.HasPrefix(title, "fix-ci:"):
		return "fix-ci"
	case strings.HasPrefix(title, "resolve-conflict:"):
		return "resolve-conflict"
	}
	return "other"
}

// ---- the world ---------------------------------------------------------------

// e2eRegistry is the connector registry of the tracker: the beads backend and
// its two named queries, in the shape the deployment declares them (the
// all-status listing names every status and no limit, since the backend appends
// its own).
//
// timeouts.list lifts the beads backend's "list" deadline to the largest value
// the registry accepts (scriptout.MaxConfiguredTimeout, 300s; the umbrella kills
// at 305s) [bead pg2-fhvt4d]. The backend runs a named query's bd expressions
// serially under ONE list deadline (40s by default, 45s for the umbrella's kill):
// work-items-all is six `bd list` runs, work-beads five, and each is a cold
// start of bd's embedded dolt engine. On a host loaded far past its cores
// (load 65 to 74 on 11 CPUs) those runs overran the default, the backend
// answered "scriptout: unavailable", the connector reported the beads source
// degraded and eight scenarios failed on lookup-unavailable or hydration
// degraded, though the lane's logic was fine. The deadline is a hang guard
// here, not a performance assertion, so the fixture gives it the headroom.
const e2eRegistry = `connector:
  issue:
    - name: beads
      command: [pg-connector-issue-beads]
search:
  sources: [pg-connector-issue-beads]
backends:
  beads:
    timeouts:
      list: 300s
    queries:
      work-beads:
        - "list --type merge-request --status open,in_progress,blocked"
        - "list --type task --title-contains review-pr: --status open,in_progress,blocked"
        - "list --type task --title-contains process-feedback: --status open,in_progress,blocked"
        - "list --type task --title-contains fix-ci: --status open,in_progress,blocked"
        - "list --type task --title-contains resolve-conflict: --status open,in_progress,blocked"
      work-items-all:
        - "list --type merge-request --status open,in_progress,blocked,deferred,closed"
        - "list --type task --title-contains process-feedback: --status open,in_progress,blocked,deferred,closed"
        - "list --type task --title-contains review-pr: --status open,in_progress,blocked,deferred,closed"
        - "list --type task --title-contains fix-ci: --status open,in_progress,blocked,deferred,closed"
        - "list --type task --title-contains resolve-conflict: --status open,in_progress,blocked,deferred,closed"
        - "list --label focus-item --status open,in_progress,blocked,deferred,closed"
`

// laneRegistry is e2eRegistry for these tools. Over the fake bd the beads
// backend's per-minute bd budget is lifted: it is a guard for the real bd's dolt
// engine (92 starts a minute by default), and the fake answers in milliseconds,
// so one scenario starts more bd processes than that in a few seconds and the
// budget would deny the rest as unavailable.
func laneRegistry(tools e2eTools) string {
	if !tools.fake {
		return e2eRegistry
	}
	return strings.Replace(e2eRegistry, "command: [pg-connector-issue-beads]", `command: [pg-connector-issue-beads, --bd-max-per-minute, "100000"]`, 1)
}

// TestLaneE2ERegistryGivesTheListOpHeadroom pins the registry's timeouts.list
// [bead pg2-fhvt4d]: without it the beads backend answers a named query's bd runs
// (up to six, serially) inside the default 40s list deadline, which a host loaded
// far past its cores overruns. It needs no tool, so it runs in the plain unit run.
func TestLaneE2ERegistryGivesTheListOpHeadroom(t *testing.T) {
	m := regexp.MustCompile(`(?m)^  beads:\n    timeouts:\n      list: (\S+)\n`).FindStringSubmatch(e2eRegistry)
	if m == nil {
		t.Fatal("e2eRegistry has no backends.beads.timeouts.list")
	}
	d, err := time.ParseDuration(m[1])
	if err != nil {
		t.Fatalf("timeouts.list %q is not a Go duration: %v", m[1], err)
	}
	// Six bd runs at the 40s default budget each is the worst serial case.
	if want := 6 * 40 * time.Second; d < want {
		t.Fatalf("timeouts.list is %s, want at least %s", d, want)
	}
}

type e2eWorld struct {
	t       *testing.T
	tools   e2eTools
	tr      *tracker
	r       *rig
	connEnv []string
	ids     []string          // the PR ids, acme/api#301...
	anchor  map[string]string // PR id to its keyless anchor's bead id
	review  map[string]string // PR id to its keyless review item's bead id
	shimCtl string            // the control directory of the bd passthrough ("" without one)
}

func deciderConfig(dir string, ceiling int) string {
	b, _ := json.Marshal(map[string]any{
		"agent_tracker_backend": "beads", "beads_dir": dir,
		"work_items_query": "work-items-all", "work_items_ceiling": ceiling,
	})
	return string(b)
}

// newE2EWorld seeds n PRs, each with a keyless anchor and a keyless review item
// at the PR's head (the shapes the old sync left), in a fresh tracker, and
// builds the rig whose every tracker call reaches it. legacy leaves the PRs'
// store rows at version 0 (never hydrated); otherwise they are hydrated.
func newE2EWorld(t *testing.T, tools e2eTools, n int, legacy bool) *e2eWorld {
	t.Helper()
	return newE2EWorldWith(t, tools, e2eOpts{n: n, legacy: legacy, seed: true})
}

// e2eOpts are the variations of one world.
type e2eOpts struct {
	n      int  // the number of PRs
	legacy bool // the PRs' store rows stay at version 0 (never hydrated)
	// seed gives every PR a keyless anchor and a keyless review item at its head
	// (the shapes the old sync left); unset, the tracker starts empty.
	seed bool
	// shim puts a passthrough over bd on the connector's PATH that logs every
	// write and fails the Nth one on request (see bdShim).
	shim bool
	// head is every PR's head sha; unset, the fixture's default (head0301 ...).
	head string
}

func newE2EWorldWith(t *testing.T, tools e2eTools, o e2eOpts) *e2eWorld {
	t.Helper()
	n, legacy := o.n, o.legacy
	w := &e2eWorld{t: t, tools: tools, tr: newTracker(t, tools), anchor: map[string]string{}, review: map[string]string{}}
	var prs []string
	for i := 0; i < n; i++ {
		num := 301 + i
		id := fmt.Sprintf("acme/api#%d", num)
		w.ids = append(w.ids, id)
		if o.seed {
			md := map[string]string{
				"repo": "acme/api", "pr_number": strconv.Itoa(num), "state": "open",
				"branch": fmt.Sprintf("feature-%d", num), "base": "main", "author": "phillipgreenii", "draft": "false",
			}
			w.anchor[id] = w.tr.create(fmt.Sprintf("%s: Cache the lookups %d", id, num), "merge-request", "", md)
			w.review[id] = w.tr.create("review-pr: "+id, "task", w.anchor[id], map[string]string{
				"repo": "acme/api", "pr_number": strconv.Itoa(num), "branch": fmt.Sprintf("feature-%d", num),
				"head_sha": fmt.Sprintf("head%04d", num), "ownership": "mine",
			})
		}
		head := ""
		if o.head != "" {
			head = fmt.Sprintf(`, "head_sha": %q`, o.head)
		}
		prs = append(prs, fmt.Sprintf(`{"number": %d, "title": "Cache the lookups %d", "author": "phillipgreenii"%s}`, num, num, head))
	}
	quoted := make([]string, len(w.ids))
	for i, id := range w.ids {
		quoted[i] = strconv.Quote(id)
	}
	fx := fmt.Sprintf(`{"name": "lane-e2e", "description": "Open PRs of mine with keyless legacy items.", "entities": [%s], "prs": [%s]}`,
		strings.Join(quoted, ","), strings.Join(prs, ","))

	conn := tools.connectorDir(t)
	registry := filepath.Join(t.TempDir(), "config.yaml")
	must(t, os.WriteFile(registry, []byte(laneRegistry(tools)), 0o600))
	sep := string(os.PathListSeparator)
	connPath := conn + sep + strings.Join(tools.toolDirs(), sep)
	if o.shim {
		w.shimCtl = t.TempDir()
		connPath = writeBDShim(t, tools, w.shimCtl) + sep + connPath
	}
	w.connEnv = e2eEnv(tools,
		"PATH="+connPath,
		"HOME="+t.TempDir(), "XDG_STATE_HOME="+t.TempDir(),
		"PG_PR_CONFIG="+registry, "PG_CONNECTOR_ISSUE_BEADS_DIR="+w.tr.dir,
		// The backend's bd budget keeps its slots and window under the account's
		// home unless told otherwise; the world keeps them in its own directory.
		"PG_CONNECTOR_ISSUE_BEADS_BUDGET_DIR="+t.TempDir())
	var legacyIDs []string
	if legacy {
		legacyIDs = w.ids
	}
	w.r = newRigWith(t, fx, rigOpts{
		cutover: true, legacy: legacyIDs, bins: &tools.bins, laneMode: "apply",
		real:          &realTracker{connector: tools.connector, env: dedupEnv(w.connEnv), all: true},
		deciderConfig: deciderConfig(w.tr.dir, 1000),
	})
	if !legacy {
		w.r.hydrate()
	}
	return w
}

// dedupEnv keeps the LAST value of every variable (e2eEnv puts a PATH first
// and the caller's PATH later).
func dedupEnv(env []string) []string {
	last := map[string]int{}
	for i, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		last[k] = i
	}
	var out []string
	for i, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if last[k] == i {
			out = append(out, kv)
		}
	}
	return out
}

// use points the world's failure reporting at the scenario that is running (the
// world itself, and its temporary directories, belong to the whole test).
func (w *e2eWorld) use(t *testing.T) *e2eWorld {
	w.t, w.r.t, w.tr.t = t, t, t
	return w
}

// tick lets the wall clock move on between two runs, so that the writes of
// each carry a later audit timestamp. Only the real bd needs it: its comment
// ids say nothing of order, so two writes in one second would list in no
// particular order. The fake bd's comment ids increase with every comment, and
// the rollback listing breaks a timestamp tie by comment id, so the order is
// already the order the writes were made.
func (w *e2eWorld) tick() {
	if !w.tools.fake {
		time.Sleep(1100 * time.Millisecond)
	}
}

func (w *e2eWorld) setCeiling(n int) {
	w.t.Helper()
	must(w.t, os.WriteFile(filepath.Join(w.r.dir, "config", "pg-decider.json"), []byte(deciderConfig(w.tr.dir, n)), 0o644))
}

// listing is the real connector's answer to the decider's lookup query.
func (w *e2eWorld) listing() (raw []byte, items int) {
	w.t.Helper()
	cmd := exec.Command(w.tools.connector, "issue", "list", "--query", "work-items-all", "--backend", "beads")
	cmd.Env = dedupEnv(w.connEnv)
	cmd.Dir = w.r.dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		w.t.Fatalf("real listing: %v\n%s%s", err, out.String(), errb.String())
	}
	var ans struct {
		Entities  []json.RawMessage `json:"entities"`
		Truncated bool              `json:"truncated"`
	}
	if err := json.Unmarshal(out.Bytes(), &ans); err != nil {
		w.t.Fatalf("decode the real listing: %v\n%s", err, out.String())
	}
	if ans.Truncated {
		w.t.Fatal("the tracker's listing reports itself truncated before the scenario made it so")
	}
	return out.Bytes(), len(ans.Entities)
}

func (w *e2eWorld) planJSON(id string) action.PlanResult {
	w.t.Helper()
	res := w.r.exec(context.Background(), filepath.Join(w.r.binDir, "pg-decider"), "plan", "pr", id, "--json")
	if res.code != 0 {
		w.t.Fatalf("pg-decider plan pr %s --json exited %d:\n%s%s", id, res.code, res.stdout, res.stderr)
	}
	p, err := parsePlanJSON(res.stdout)
	if err != nil {
		w.t.Fatalf("decode the plan of %s: %v\n%s", id, err, res.stdout)
	}
	return *p
}

// trackerReads are the connector verbs that do not write.
var trackerReads = map[string]bool{"show": true, "list": true, "deps": true, "children": true}

// writes is the tracker-writing connector calls logged since call number from.
func (w *e2eWorld) writes(from int) []string {
	w.t.Helper()
	var out []string
	lines := strings.Split(w.r.ctlRead("calls.log"), "\n")
	for _, l := range lines[min(from, len(lines)):] {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == "issue" && !trackerReads[f[1]] {
			out = append(out, l)
		}
	}
	return out
}

func (w *e2eWorld) callCount() int { return len(strings.Split(w.r.ctlRead("calls.log"), "\n")) }

// pairs is the plan's (kind, operation) multiset, annotations left out (they
// write the store, not the tracker).
func pairs(p action.PlanResult) map[string]int {
	out := map[string]int{}
	for _, a := range p.Actions {
		if a.Op == action.OpAnnotate {
			continue
		}
		out[a.Kind+" "+string(a.Op)]++
	}
	return out
}

func (w *e2eWorld) lastRecord() RunRecord {
	w.t.Helper()
	recs := w.r.runRecords()
	if len(recs) == 0 {
		w.t.Fatal("no run record")
	}
	return recs[len(recs)-1]
}

// ---- the drift check ------------------------------------------------------------

var labelPair = regexp.MustCompile(`(\w+)="((?:[^"\\]|\\.)*)"`)

// decisionLabels reads the rule and reason label values of every
// pg_decider_decisions_total series of a metrics exposition.
func decisionLabels(exposition string) (rules, reasons []string, series int) {
	r, s := map[string]bool{}, map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(exposition))
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "pg_decider_decisions_total{") {
			continue
		}
		series++
		for _, m := range labelPair.FindAllStringSubmatch(l, -1) {
			switch m[1] {
			case "rule":
				r[m[2]] = true
			case "reason":
				// A series of an action other than skip carries no reason (the
				// empty label value); it is not a decider reason.
				if m[2] != "" {
					s[m[2]] = true
				}
			}
		}
	}
	return keys(r), keys(s), series
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// deciderRuleIDs are the rule ids the decider registers, for either entity type.
func deciderRuleIDs() []string {
	set := map[string]bool{}
	for _, typ := range []string{decide.EntityTypePR, decide.EntityTypeIssue} {
		for _, r := range decide.RulesFor(typ) {
			set[r.ID()] = true
		}
	}
	return keys(set)
}

// deciderSkipReasons are the skip reasons the decider can print: every string
// constant named Reason* in internal/action, read from the source so that a new
// reason added there is in the list without anyone editing a second list.
func deciderSkipReasons(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "action", "action.go"), nil, 0)
	if err != nil {
		t.Fatalf("read the decider's reasons: %v", err)
	}
	set := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if !strings.HasPrefix(name.Name, "Reason") || i >= len(vs.Values) {
				continue
			}
			if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, err := strconv.Unquote(lit.Value); err == nil {
					set[v] = true
				}
			}
		}
		return true
	})
	return keys(set)
}

// labelDrift names every value that is in one list and not in the other.
func labelDrift(label string, decider, desk []string) []string {
	in := func(set []string, v string) bool {
		for _, s := range set {
			if s == v {
				return true
			}
		}
		return false
	}
	var out []string
	for _, v := range decider {
		if !in(desk, v) {
			out = append(out, fmt.Sprintf("%s %q is in the decider's list and not in pg-desk's zero-filled series", label, v))
		}
	}
	for _, v := range desk {
		if !in(decider, v) {
			out = append(out, fmt.Sprintf("%s %q is in pg-desk's zero-filled series and not in the decider's list", label, v))
		}
	}
	return out
}

// ---- registering the scenarios ------------------------------------------------------

// laneRun registers the scenarios of one run of the suite (TestLaneFake or
// TestLaneE2E) and remembers their names, for the check that they equal the
// run's scenario list file.
type laneRun struct {
	t        *testing.T
	tools    e2eTools
	parallel bool // run every independent scenario alongside the others
	ran      []string
}

// run registers a scenario that builds a world of its own. With parallel set it
// runs alongside the other independent scenarios, after the sequential ones.
func (l *laneRun) run(name string, f func(*testing.T)) {
	l.ran = append(l.ran, name)
	l.t.Run(name, func(t *testing.T) {
		if l.parallel {
			t.Parallel()
		}
		f(t)
	})
}

// step registers a scenario that shares its world with the steps around it, so
// it always runs in order with them and never in parallel.
func (l *laneRun) step(name string, f func(*testing.T)) {
	l.ran = append(l.ran, name)
	l.t.Run(name, f)
}

// readScenarioNames reads a scenario list file: one name per line, "#" comments.
func readScenarioNames(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			names = append(names, l)
		}
	}
	return names
}

// scenarioList registers the last scenario of a run: the scenarios it registered
// are exactly the ones its list file names (the file the check and the script
// assert a PASS line for), and there are want of them.
func (l *laneRun) scenarioList(file string, want int) {
	l.step("the-scenario-list-is-the-list-the-gate-asserts", func(t *testing.T) {
		names := readScenarioNames(t, file)
		got := append([]string(nil), l.ran...)
		sort.Strings(names)
		sort.Strings(got)
		if strings.Join(names, "\n") != strings.Join(got, "\n") {
			t.Fatalf("the scenarios the test runs differ from %s:\nrun:  %v\nlist: %v", file, got, names)
		}
		if len(got) != want {
			t.Errorf("the test declares %d scenarios, but its minimum is %d (and the check's and the script's minimum must equal it)", len(got), want)
		}
	})
}
