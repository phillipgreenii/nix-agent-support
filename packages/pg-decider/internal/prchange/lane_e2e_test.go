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

// The real-tracker end-to-end gate of the PR lane (docket pg2-6mo45.3, packet
// P4c-1; nix check pg-decider-lane-e2e and the hook of the same name).
//
// What is real: pg-desk (a schema-2 store), pg-decider, pg-pr-change, the
// umbrella pg-connector, the beads backend pg-connector-issue-beads and a bd in
// a throwaway embedded store. Every `issue ...` and `search` call of every
// process reaches that tracker, so the ground truth of each scenario is the
// tracker's own listing (`bd list`), not a log of what a double was asked. What
// is not real is the forge: the pg-connector `pr ...` reads are answered by the
// fixture-driven fake the other real-tool tests share (rig_test.go), which is
// also how the unit of this packet reuses the harness instead of the parity
// gate's own sandbox (its fake connector answers reads only).
//
// The nix sandbox has no network, so the binaries come from the environment
// (the check exports the ones nix built; the hook builds them from source and
// exports the same names, scripts/run-pg-decider-lane-e2e.sh):
//
//	PG_PR_CHANGE_PG_DESK_BIN, PG_PR_CHANGE_PG_DECIDER_BIN, PG_PR_CHANGE_BIN
//	PG_PR_CHANGE_SQLITE3_BIN                          (optional; PATH otherwise)
//	PG_LANE_E2E_PG_CONNECTOR_BIN, PG_LANE_E2E_PG_CONNECTOR_ISSUE_BEADS_BIN
//	PG_PR_CHANGE_REQUIRE_BINARIES                     (set: a missing tool FAILS)
//
// bd and git are found on PATH. A missing tool is a LOUD failure whenever
// PG_PR_CHANGE_REQUIRE_BINARIES is set (the check and the hook set it), never a
// skip; without it the test skips, like the other real-tool tests, so a plain
// `go test ./...` stays hermetic.
const (
	envLaneConnectorBin      = "PG_LANE_E2E_PG_CONNECTOR_BIN"
	envLaneConnectorBeadsBin = "PG_LANE_E2E_PG_CONNECTOR_ISSUE_BEADS_BIN"
)

// laneE2EScenariosFile lists, one name per line, every scenario TestLaneE2E
// runs. The test fails when its own scenarios and the file differ, and the
// check and the hook assert a PASS line for each name in it.
const laneE2EScenariosFile = "testdata/lane-e2e-scenarios.txt"

// minLaneE2EScenarios is the number of scenarios the gate must run; keep it
// equal to the minScenarios of the nix check and of the hook script.
const minLaneE2EScenarios = 11

// e2eSeeded is N, the number of PRs whose keyless anchors the adoption
// scenarios seed.
const e2eSeeded = 3

type e2eTools struct {
	bins      binaries
	connector string // the real pg-connector
	beads     string // the real pg-connector-issue-beads
	bd        string
	git       string
}

func e2eNeed(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if v := os.Getenv(envRequireBins); v != "" && v != "0" {
		t.Fatalf("%s: %v (this is a failure, never a skip, because %s is set)", what, err, envRequireBins)
	}
	t.Skipf("%s: %v: skipping the end-to-end lane gate", what, err)
}

func laneE2ETools(t *testing.T) e2eTools {
	t.Helper()
	tools := e2eTools{bins: realBinaries(t)}
	var err error
	for _, e := range []struct {
		env string
		dst *string
	}{{envLaneConnectorBin, &tools.connector}, {envLaneConnectorBeadsBin, &tools.beads}} {
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
	tools.bd, err = exec.LookPath("bd")
	e2eNeed(t, "bd (the real tracker)", err)
	tools.git, err = exec.LookPath("git")
	e2eNeed(t, "git (bd init needs it)", err)
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

// ---- the tracker ------------------------------------------------------------

// tracker is one throwaway beads workspace (bd's embedded mode, a private
// directory) and the environment bd and the real connector run under.
type tracker struct {
	t   *testing.T
	bd  string
	dir string
	env []string
}

func e2eEnv(tools e2eTools, extra ...string) []string {
	path := strings.Join([]string{filepath.Dir(tools.bd), filepath.Dir(tools.git), "/usr/bin", "/bin"}, string(os.PathListSeparator))
	env := []string{
		"PATH=" + path,
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
	meta, err := os.ReadFile(filepath.Join(tr.dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("read bd metadata: %v", err)
	}
	if !strings.Contains(string(meta), `"dolt_mode": "embedded"`) {
		t.Fatalf("bd init did not produce a private embedded store, refusing to continue:\n%s", meta)
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

// e2eRegistry is the connector registry of the real tracker: the beads backend
// and its two named queries, in the shape the deployment declares them (the
// all-status listing names every status and no limit, since the backend appends
// its own).
const e2eRegistry = `connector:
  issue:
    - name: beads
      command: [pg-connector-issue-beads]
search:
  sources: [pg-connector-issue-beads]
backends:
  beads:
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

type e2eWorld struct {
	t       *testing.T
	tools   e2eTools
	tr      *tracker
	r       *rig
	connEnv []string
	ids     []string          // the PR ids, acme/api#301...
	anchor  map[string]string // PR id to its keyless anchor's bead id
	review  map[string]string // PR id to its keyless review item's bead id
}

func deciderConfig(dir string, ceiling int) string {
	b, _ := json.Marshal(map[string]any{
		"agent_tracker_backend": "beads", "beads_dir": dir,
		"work_items_query": "work-items-all", "work_items_ceiling": ceiling,
	})
	return string(b)
}

// newE2EWorld seeds n PRs, each with a keyless anchor and a keyless review item
// at the PR's head (the shapes the old sync left), in a fresh real tracker, and
// builds the rig whose every tracker call reaches it. legacy leaves the PRs'
// store rows at version 0 (never hydrated); otherwise they are hydrated.
func newE2EWorld(t *testing.T, tools e2eTools, n int, legacy bool) *e2eWorld {
	t.Helper()
	w := &e2eWorld{t: t, tools: tools, tr: newTracker(t, tools), anchor: map[string]string{}, review: map[string]string{}}
	var prs []string
	for i := 0; i < n; i++ {
		num := 301 + i
		id := fmt.Sprintf("acme/api#%d", num)
		w.ids = append(w.ids, id)
		md := map[string]string{
			"repo": "acme/api", "pr_number": strconv.Itoa(num), "state": "open",
			"branch": fmt.Sprintf("feature-%d", num), "base": "main", "author": "phillipgreenii", "draft": "false",
		}
		w.anchor[id] = w.tr.create(fmt.Sprintf("%s: Cache the lookups %d", id, num), "merge-request", "", md)
		w.review[id] = w.tr.create("review-pr: "+id, "task", w.anchor[id], map[string]string{
			"repo": "acme/api", "pr_number": strconv.Itoa(num), "branch": fmt.Sprintf("feature-%d", num),
			"head_sha": fmt.Sprintf("head%04d", num), "ownership": "mine",
		})
		prs = append(prs, fmt.Sprintf(`{"number": %d, "title": "Cache the lookups %d", "author": "phillipgreenii"}`, num, num))
	}
	quoted := make([]string, len(w.ids))
	for i, id := range w.ids {
		quoted[i] = strconv.Quote(id)
	}
	fx := fmt.Sprintf(`{"name": "lane-e2e", "description": "Open PRs of mine with keyless legacy items.", "entities": [%s], "prs": [%s]}`,
		strings.Join(quoted, ","), strings.Join(prs, ","))

	conn := tools.connectorDir(t)
	registry := filepath.Join(t.TempDir(), "config.yaml")
	must(t, os.WriteFile(registry, []byte(e2eRegistry), 0o600))
	w.connEnv = e2eEnv(tools,
		"PATH="+conn+string(os.PathListSeparator)+filepath.Dir(tools.bd)+string(os.PathListSeparator)+filepath.Dir(tools.git)+string(os.PathListSeparator)+"/usr/bin:/bin",
		"HOME="+t.TempDir(), "XDG_STATE_HOME="+t.TempDir(),
		"PG_PR_CONFIG="+registry, "PG_CONNECTOR_ISSUE_BEADS_DIR="+w.tr.dir)
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
		w.t.Fatal("the real tracker's listing reports itself truncated before the scenario made it so")
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

// ---- the scenarios ---------------------------------------------------------------

func readScenarioNames(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(laneE2EScenariosFile)
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

func TestLaneE2E(t *testing.T) {
	tools := laneE2ETools(t)
	var ran []string
	run := func(name string, f func(*testing.T)) {
		ran = append(ran, name)
		t.Run(name, f)
	}

	// The adoption world: N PRs with keyless anchors and review items. The next
	// three scenarios are three views of one history (plan, apply, apply again).
	top := t
	world := newE2EWorld(top, tools, e2eSeeded, false)
	var planned map[string]int // the plans' (kind, op) counts over the N PRs, read before the first apply
	var beforeFirst, afterFirst map[string]string

	run("n-keyless-anchors-are-adopted-from-the-trackers-own-listing-with-no-creates", func(t *testing.T) {
		w := world.use(t)
		before := w.tr.snapshot()
		beforeFirst = before
		if len(before) != 2*e2eSeeded {
			t.Fatalf("setup: the tracker holds %d beads, want %d", len(before), 2*e2eSeeded)
		}
		for _, it := range w.tr.items() {
			if _, ok := metaOf(it)["dedup_key"]; ok {
				t.Fatalf("setup: %v is already keyed", it["id"])
			}
		}
		planned = map[string]int{}
		start := w.callCount()
		for _, id := range w.ids {
			p := pairs(w.planJSON(id))
			for k, n := range p {
				planned[k] += n
			}
			if p["anchor create"] != 0 || p["review-pr create"] != 0 {
				t.Fatalf("%s: the plan creates what the tracker already lists: %v", id, p)
			}
			if p["anchor update"] == 0 {
				t.Fatalf("%s: the plan does not adopt the keyless anchor (no anchor update): %v", id, p)
			}
			res := w.r.change("plan", id)
			if res.code != 0 {
				t.Fatalf("plan %s exit %d:\n%s", id, res.code, res.stderr)
			}
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Fatalf("plan wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("plan made tracker writes: %v", wr)
		}

		start = w.callCount()
		for _, id := range w.ids {
			res := w.r.change("apply", id)
			if res.code != 0 {
				t.Fatalf("apply %s exit %d:\n%s", id, res.code, res.stderr)
			}
			rec := w.lastRecord()
			if rec.Adoption == nil || rec.Adoption.Outcome != "adopted" {
				t.Errorf("%s: adoption outcome %+v, want adopted", id, rec.Adoption)
			}
		}
		for _, l := range w.writes(start) {
			if strings.HasPrefix(l, "issue create") {
				t.Errorf("apply created a bead: %s", l)
			}
		}
		afterFirst = w.tr.snapshot()
		if len(afterFirst) != len(before) {
			t.Fatalf("the tracker holds %d beads after adoption, want the same %d (no creates): %s", len(afterFirst), len(before), sameSnapshot(before, afterFirst))
		}
		keyed := 0
		for _, it := range w.tr.items() {
			k, _ := metaOf(it)["dedup_key"].(string)
			if k == "" {
				t.Errorf("%v (%s) was not keyed by the adoption", it["id"], itemKind(it))
				continue
			}
			keyed++
		}
		if keyed != 2*e2eSeeded {
			t.Errorf("%d items keyed, want %d (N anchors and N review items)", keyed, 2*e2eSeeded)
		}
	})

	run("the-second-apply-writes-nothing", func(t *testing.T) {
		w := world.use(t)
		start := w.callCount()
		for _, id := range w.ids {
			res := w.r.change("apply", id)
			if res.code != 0 {
				t.Fatalf("second apply %s exit %d:\n%s", id, res.code, res.stderr)
			}
			rec := w.lastRecord()
			if rec.Residual == nil || *rec.Residual != 0 || (rec.Decide != nil && len(rec.Decide.Actions) != 0) {
				t.Errorf("%s: the second apply still plans actions (residual %v, decide %+v)", id, rec.Residual, rec.Decide)
			}
		}
		if got := w.tr.snapshot(); sameSnapshot(afterFirst, got) != "" {
			t.Fatalf("the second apply wrote to the tracker: %s", sameSnapshot(afterFirst, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("the second apply made tracker writes: %v", wr)
		}
	})

	run("plan-and-apply-agree-per-kind-and-operation", func(t *testing.T) {
		w := world.use(t)
		// The plans read before the first apply against what the apply did to
		// the tracker, per kind: the (kind, operation) pairs the plans list are
		// exactly the ones the tracker shows after it, and nothing else changed.
		if len(planned) == 0 || beforeFirst == nil || afterFirst == nil {
			t.Fatal("the adoption scenario recorded no plan or snapshot: the comparison would be vacuous")
		}
		byID := map[string]map[string]any{}
		for _, it := range w.tr.items() {
			byID[it["id"].(string)] = it
		}
		touched := map[string]int{}
		for id, v := range afterFirst {
			old, existed := beforeFirst[id]
			op := ""
			switch {
			case !existed:
				op = "create"
			case old != v:
				op = "update"
			default:
				continue
			}
			touched[itemKind(byID[id])+" "+op]++
		}
		for k, n := range planned {
			if touched[k] == 0 {
				t.Errorf("the plans list %q (%d actions) and the tracker shows no such change after the apply: tracker %v", k, n, touched)
			}
		}
		for k, n := range touched {
			if planned[k] == 0 {
				t.Errorf("the tracker shows %q on %d items and the plans did not list it: plans %v", k, n, planned)
			}
		}
		// Every item an apply updated is one a plan listed an update for, by kind:
		// the plans' counts per kind are at least the items changed.
		for k, n := range touched {
			if planned[k] < n {
				t.Errorf("%q: %d items changed but the plans list only %d", k, n, planned[k])
			}
		}
	})

	run("an-unhydrated-view-is-refused-and-nothing-is-written", func(t *testing.T) {
		w := newE2EWorld(t, tools, 1, true)
		id := w.ids[0]
		if got := w.r.neverHydrated(); got != "1" {
			t.Fatalf("setup: the never-hydrated count is %s, want 1", got)
		}
		// A refresh that reports success and stores nothing (the loss the
		// refusal exists for): pg-desk's other verbs, `pr show` included, are the
		// real ones, so the command reads a real version-0 view.
		real := filepath.Join(w.r.binDir, "pg-desk")
		kept := real + ".real"
		must(t, os.Rename(real, kept))
		must(t, os.WriteFile(real, []byte("#!/bin/sh\ncase \"$*\" in\n'pr refresh '*) echo 'no change'; exit 0;;\nesac\nexec "+shq(kept)+" \"$@\"\n"), 0o755))
		before := w.tr.snapshot()
		start := w.callCount()
		for _, mode := range []string{"plan", "apply"} {
			res := w.r.change(mode, id)
			if res.code != ExitUnhydrated {
				t.Fatalf("%s: exit %d, want %d:\n%s", mode, res.code, ExitUnhydrated, res.stderr)
			}
			contains(t, mode+" refusal", res.stderr, "class=unhydrated", "view refused", "the decider was not run")
		}
		if n := len(w.r.deciderRuns()) + len(w.r.deciderPlanReads()); n != 0 {
			t.Errorf("the decider ran %d times for a refused view", n)
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Fatalf("a refused run wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("a refused run made tracker writes: %v", wr)
		}
		// The same PR, hydrated for real, is accepted.
		must(t, os.Remove(real))
		must(t, os.Rename(kept, real))
		if res := w.r.change("plan", id); res.code != 0 {
			t.Fatalf("after a real refresh the plan exited %d:\n%s", res.code, res.stderr)
		}
	})

	// The fail-closed scenarios share one world that no scenario of it writes to:
	// each run it makes is refused (or is a plan), so the tracker's own listing
	// before and after is the proof that nothing was written.
	var closed *e2eWorld
	failClosedWorld := func(t *testing.T) *e2eWorld {
		t.Helper()
		if closed == nil {
			closed = newE2EWorld(top, tools, 1, false)
		}
		return closed.use(t)
	}

	run("a-listing-above-the-ceiling-fails-the-run-and-writes-nothing", func(t *testing.T) {
		w := failClosedWorld(t)
		_, items := w.listing()
		w.setCeiling(items - 1)
		before := w.tr.snapshot()
		start := w.callCount()
		for _, mode := range []string{"plan", "apply"} {
			res := w.r.change(mode, w.ids[0])
			if res.code != ExitDecideFailed {
				t.Fatalf("%s: exit %d, want %d:\n%s", mode, res.code, ExitDecideFailed, res.stderr)
			}
			contains(t, mode+" failure line", res.stderr, "decide_failed", "above the ceiling", fmt.Sprintf("work_items_ceiling=%d", items-1))
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Fatalf("a failed-closed run wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("a failed-closed run made tracker writes: %v", wr)
		}
		lane := w.r.lane()
		if lane["pr_lane.state.lookup_ceiling"] != strconv.Itoa(items-1) || lane["pr_lane.state.lookup_items"] != strconv.Itoa(items) {
			t.Errorf("the lane gauges do not carry the listing (%d items, ceiling %d): %v", items, items-1, lane)
		}
	})

	run("a-listing-of-exactly-the-ceiling-is-accepted", func(t *testing.T) {
		w := failClosedWorld(t)
		_, items := w.listing()
		w.setCeiling(items)
		res := w.r.change("plan", w.ids[0])
		if res.code != 0 {
			t.Fatalf("a listing of %d items at ceiling %d was refused: exit %d:\n%s", items, items, res.code, res.stderr)
		}
	})

	run("a-listing-that-reports-itself-truncated-fails-the-run-and-writes-nothing", func(t *testing.T) {
		w := failClosedWorld(t)
		w.setCeiling(1000)
		raw, _ := w.listing()
		// The real beads backend never truncates the unranged listing, so a
		// tracker that does is shown by the real answer with the flag set.
		var ans map[string]json.RawMessage
		must(t, json.Unmarshal(raw, &ans))
		ans["truncated"] = json.RawMessage("true")
		out, err := json.Marshal(ans)
		must(t, err)
		w.r.ctlWrite("work-items.out", string(out)+"\n")
		before := w.tr.snapshot()
		start := w.callCount()
		for _, mode := range []string{"plan", "apply"} {
			res := w.r.change(mode, w.ids[0])
			if res.code != ExitDecideFailed {
				t.Fatalf("%s: exit %d, want %d:\n%s", mode, res.code, ExitDecideFailed, res.stderr)
			}
			contains(t, mode+" failure line", res.stderr, "decide_failed", "truncated")
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Fatalf("a failed-closed run wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Fatalf("a failed-closed run made tracker writes: %v", wr)
		}
	})

	run("the-bd-list-query-of-a-prs-review-items-returns-every-state", func(t *testing.T) {
		tr := newTracker(t, tools)
		md := func(repo string, n int, sha string) map[string]string {
			return map[string]string{"repo": repo, "pr_number": strconv.Itoa(n), "head_sha": sha}
		}
		title := func(repo string, n int, sha string) string {
			return fmt.Sprintf("review-pr: %s#%d @ %s", repo, n, sha[:7])
		}
		mine := map[string]string{}
		for _, st := range []string{"open", "in_progress", "blocked", "deferred", "closed"} {
			sha := fmt.Sprintf("%s0000000000", st[:2])
			id := tr.create(title("acme/api", 7, sha), "task", "", md("acme/api", 7, sha))
			switch st {
			case "closed":
				tr.run("close", id, "--reason", "reviewed")
			case "open":
			default:
				tr.run("update", id, "--status", st)
			}
			mine[id] = st
		}
		// Neighbors the query must leave out: another PR of the repo whose number
		// starts with 7, the same number in another repository, and the PR's own
		// anchor and feedback item (no review-pr: prefix).
		tr.create(title("acme/api", 70, "abcdef0"), "task", "", md("acme/api", 70, "abcdef0"))
		tr.create(title("other/api", 7, "abcdef1"), "task", "", md("other/api", 7, "abcdef1"))
		tr.create("acme/api#7: Add the export command", "merge-request", "", md("acme/api", 7, "abcdef2"))
		tr.create("process-feedback: acme/api#7", "task", "", md("acme/api", 7, "abcdef3"))

		query := func(repo string, n int) []map[string]any {
			return decodeItems(t, tr.run("list", "--all", "--limit", "0", "--metadata-field", "repo="+repo,
				"--metadata-field", "pr_number="+strconv.Itoa(n), "--title-contains", "review-pr:", "--sort", "created", "--json"))
		}
		got := query("acme/api", 7)
		seen := map[string]string{}
		for _, it := range got {
			seen[it["id"].(string)], _ = it["status"].(string)
		}
		if len(seen) != len(mine) {
			t.Fatalf("the query returned %d items, want the %d review items of the PR in every state: %v", len(seen), len(mine), seen)
		}
		for id, st := range mine {
			if seen[id] != st {
				t.Errorf("review item %s: returned in state %q, want %q", id, seen[id], st)
			}
		}
		if n := len(query("acme/api", 1)); n != 0 {
			t.Errorf("a PR with no review item got %d (the filters do not discriminate)", n)
		}
		// bd list's own 50-row default would hide older rows; the query names --limit 0.
		if len(tr.items()) != len(mine)+4 {
			t.Errorf("setup: the tracker holds %d beads", len(tr.items()))
		}
	})

	run("pg-desks-zero-filled-decision-labels-equal-the-deciders-rules-and-reasons", func(t *testing.T) {
		// serve answers /metrics once the store has been written to: hydrate the
		// fixture PR. No lane run is recorded, so every series is the zero fill.
		r := newRig(t, fxMerged, true)
		r.hydrate()
		rules, reasons, series := decisionLabels(r.scrape())
		if series == 0 {
			t.Fatal("a fresh schema-2 store's scrape has no pg_decider_decisions_total series: the comparison would be vacuous")
		}
		dRules, dReasons := deciderRuleIDs(), deciderSkipReasons(t)
		if len(dRules) < 13 || len(dReasons) < 5 {
			t.Fatalf("the decider's lists are suspiciously short: %d rules %v, %d reasons %v", len(dRules), dRules, len(dReasons), dReasons)
		}
		drift := append(labelDrift("rule", dRules, rules), labelDrift("reason", dReasons, reasons)...)
		if len(drift) != 0 {
			t.Fatalf("pg-desk's copy of the decider's lists has drifted (update packages/pg-desk/internal/lanestate/vocab.go):\n%s", strings.Join(drift, "\n"))
		}
	})

	run("the-drift-check-fails-on-a-decider-value-pg-desk-lacks-and-on-the-reverse", func(t *testing.T) {
		rules := []string{"adoption", "review.head-advanced"}
		reasons := []string{"hidden", "not matched"}
		if d := labelDrift("rule", rules, rules); len(d) != 0 {
			t.Fatalf("equal lists drift: %v", d)
		}
		cases := []struct {
			name           string
			dRules, dRsns  []string
			kRules, kRsns  []string
			want, wantKind string
		}{
			{"a decider reason absent from pg-desk's copy", rules, append(append([]string{}, reasons...), "newly-added"), rules, reasons, "newly-added", "reason"},
			{"a pg-desk reason the decider lacks", rules, reasons, rules, append(append([]string{}, reasons...), "stale"), "stale", "reason"},
			{"a decider rule absent from pg-desk's copy", append(append([]string{}, rules...), "review.new-rule"), reasons, rules, reasons, "review.new-rule", "rule"},
			{"a pg-desk rule the decider lacks", rules, reasons, append(append([]string{}, rules...), "gone.rule"), reasons, "gone.rule", "rule"},
		}
		for _, c := range cases {
			drift := append(labelDrift("rule", c.dRules, c.kRules), labelDrift("reason", c.dRsns, c.kRsns)...)
			if len(drift) != 1 || !strings.Contains(drift[0], c.wantKind+" \""+c.want+"\"") {
				t.Errorf("%s: drift %v, want exactly one finding naming %q", c.name, drift, c.want)
			}
		}
	})

	run("the-scenario-list-is-the-list-the-gate-asserts", func(t *testing.T) {
		want := readScenarioNames(t)
		got := append([]string(nil), ran...)
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, "\n") != strings.Join(got, "\n") {
			t.Fatalf("the scenarios the test runs differ from %s:\nrun:  %v\nlist: %v", laneE2EScenariosFile, got, want)
		}
		if len(got) != minLaneE2EScenarios {
			t.Errorf("the test declares %d scenarios, but minLaneE2EScenarios is %d (and the nix check's and the hook's minimum must equal it)", len(got), minLaneE2EScenarios)
		}
	})
}
