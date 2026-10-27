package main

// Verb exhaustiveness on a schema-2 store (design: PR lane refactor, test
// strategy; packet P0b-a).
//
// The store migration once broke a deployed role because one verb the role
// runs still read a table the cutover drops. This file is the guard: it walks
// EVERY leaf verb of the command tree, requires each one to be classified in
// verbClasses below (an unclassified new verb fails the test), runs every
// verb classified safe in a fresh process against a store migrated to schema
// 2 under a deployment-style rendered configuration, and checks that the
// list of safe verbs installed in the package output (share/safe-verbs.txt,
// read by the deployment check) is exactly the safe set of the table.
//
// A safe verb passes when it runs on the migrated store without a schema or
// ledger error; it MAY still exit non-zero for another reason (a missing
// entity, no reachable connector). The unsafe table records why a verb is not
// guaranteed to run on a schema-2 store.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// verbChildEnv marks a re-executed copy of the test binary that runs main()
// with the verb in verbChildArgsEnv instead of running tests. This is the
// real pg-desk code path (flags, config loader, store open, exit code) in its
// own process, so each verb starts from fresh flag state.
const (
	verbChildEnv     = "PG_DESK_TEST_VERB_CHILD"
	verbChildArgsEnv = "PG_DESK_TEST_VERB_ARGS"
	argSep           = "\x1f"
)

// runVerbChildIfRequested is called first from TestMain: in a verb child it
// never returns.
func runVerbChildIfRequested() {
	if os.Getenv(verbChildEnv) == "" {
		return
	}
	os.Args = append([]string{"pg-desk"}, strings.Split(os.Getenv(verbChildArgsEnv), argSep)...)
	main()
	os.Exit(0)
}

// verbClass classifies one leaf verb. Exactly one of Reason (unsafe) or Args
// (safe) explains the entry: a safe verb carries the arguments the test runs
// it with; an unsafe verb carries the reason it is not run on a schema-2
// store.
type verbClass struct {
	Safe bool
	// Args is the argument list a safe verb runs with, after its path.
	Args []string
	// Daemon marks a long-running verb: the test starts it, waits for it to
	// listen, then stops it and expects a clean exit.
	Daemon bool
	// Reason says why an unsafe verb is not run on a schema-2 store.
	Reason string
}

const (
	testRepo   = "example-org/example-repo"
	testPR     = testRepo + "#1"
	testIssue  = "issue-1"
	testThread = "thread-1"
)

func safe(args ...string) verbClass { return verbClass{Safe: true, Args: args} }

func unsafe(reason string) verbClass { return verbClass{Reason: reason} }

// typedSafe builds the safe classification of the verbs that exist once per
// entity type (pr, issue, thread), for the entity id id.
func typedVerbs(typ, id string) map[string]verbClass {
	out := map[string]verbClass{
		typ + " annotate":        safe(id, "--key", "note", "--value", "x"),
		typ + " changes":         safe("--consumer", "verb-test", "--cached"),
		typ + " consumer forget": safe("verb-test"),
		typ + " consumer list":   safe(),
		typ + " hide":            safe(id, "test"),
		typ + " history":         safe(id),
		typ + " link add":        safe(id, "issue:other-1"),
		typ + " link remove":     safe(id, "issue:other-1"),
		typ + " open":            unsafe("execs the operator-configured browser opener"),
		typ + " refresh":         safe(id),
		typ + " show":            safe(id),
		typ + " suppress":        safe(id, "--kind", "example-kind"),
		typ + " unhide":          safe(id),
		typ + " unsuppress":      safe(id, "--kind", "example-kind"),
		typ + " wip":             safe("on", id),
	}
	return out
}

// verbClasses is the hand-written classification of every leaf verb. Adding a
// verb to the command tree without an entry here fails
// TestVerbExhaustiveness; a safe entry also needs its line in
// share/safe-verbs.txt (regenerate it with PG_DESK_UPDATE_SAFE_VERBS=1).
func verbClasses() map[string]verbClass {
	m := map[string]verbClass{
		"attention explain": safe("pr:" + testPR),
		"attention list":    safe(),
		"doctor":            safe(),
		"feedback list":     safe("1"),
		"feedback set":      safe("1", "c1", "--disposition", "will-fix"),
		"focus close":       safe("--dry-run"),
		"focus explain":     safe("pr:" + testPR),
		"focus pull":        safe("--dry-run"),
		"focus replan":      safe(),
		"focus select":      safe("--dry-run"),
		"focus show":        safe(),
		"freshness":         safe(),
		"heartbeat":         safe(),
		"heartbeat-item":    safe(),
		"hide":              safe("1", "test"),
		"import-pg-pr-annotations": unsafe("one-shot cutover import that reads the retired pg-pr annotation " +
			"store and writes the old per-column annotation table, which schema 2 replaced"),
		"ledger":    unsafe("reads the entity-to-bead ledger that the schema-2 cutover drops (the verb is a stub)"),
		"links":     safe(testPR),
		"migrate":   unsafe("the one-way schema cutover itself: it refuses a store that is already at schema 2"),
		"open":      unsafe("execs the operator-configured browser opener"),
		"reconcile": unsafe("lists the entity-to-bead ledger that the schema-2 cutover drops"),
		"run":       safe("pr", "1"),
		"serve":     {Safe: true, Args: []string{"--addr", "127.0.0.1:0"}, Daemon: true},
		"show": unsafe("reads the old per-column annotation table and the ledger, which schema 2 replaced or " +
			"dropped; pr show is the schema-2 verb"),
		"status": safe(),
		"sweep":  safe(),
		"unhide": safe("1"),
		"wip":    safe("on", "1"),
	}
	for k, v := range typedVerbs("pr", "1") {
		m[k] = v
	}
	for k, v := range typedVerbs("issue", testIssue) {
		m[k] = v
	}
	for k, v := range typedVerbs("thread", testThread) {
		m[k] = v
	}
	// The verbs only the pr entity type has.
	m["pr feedback list"] = safe("1")
	m["pr feedback set"] = safe("1", "c1", "--disposition", "will-fix")
	m["pr force-review"] = safe("1")
	m["pr head-check"] = safe("1", "--head-sha", "0123456789abcdef")
	// The PR lane's pg-desk-owned write path (P3b1a): both write the lane
	// state in the meta table and the entity's active flag, all schema 2.
	// lane-record runs here on an empty input (/dev/null), which it rejects
	// with a decode error, not a schema error.
	m["pr lane-record"] = safe("--input", "/dev/null")
	m["pr mark-inactive"] = safe("1")
	// The issue-only one-shot backfill of the backend and watched columns: it
	// reads a schema-2 store and a daemon, never the dropped ledger.
	m["issue backfill-backend"] = safe()
	// The verbs `version` reads nothing from the store at all.
	m["version"] = safe()
	return m
}

// isCobraBuiltin reports whether a root-level command name is one cobra adds
// itself: help, completion, and the hidden __complete commands that appear
// once any test has exercised shell completion. They are not pg-desk verbs: a
// role never runs them and they read no store, and which of them exist depends
// on what ran before, so they MUST NOT enter the walk.
func isCobraBuiltin(name string) bool {
	return name == "help" || name == "completion" || strings.HasPrefix(name, "__")
}

// leafVerbs returns every leaf verb path of the command tree, sorted, without
// the cobra built-ins.
func leafVerbs() []string {
	var out []string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		subs := c.Commands()
		if len(subs) == 0 && len(path) > 0 {
			out = append(out, strings.Join(path, " "))
		}
		for _, s := range subs {
			if len(path) == 0 && isCobraBuiltin(s.Name()) {
				continue
			}
			walk(s, append(append([]string{}, path...), s.Name()))
		}
	}
	walk(rootCmd, nil)
	sort.Strings(out)
	return out
}

func safeVerbList(classes map[string]verbClass) []string {
	var out []string
	for v, c := range classes {
		if c.Safe {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

const safeVerbsHeader = "# Verbs of pg-desk that run on a store migrated to schema 2 (one verb path per line).\n" +
	"# Generated from the classification table in cmd/pg-desk/verbs_schema2_test.go:\n" +
	"# regenerate with PG_DESK_UPDATE_SAFE_VERBS=1 nice -n 10 go test ./cmd/pg-desk -run TestVerbExhaustiveness\n" +
	"# run and sweep are safe only under sync.mode off: the sync stage reads the ledger that schema 2 drops.\n"

func renderSafeVerbs(verbs []string) string {
	return safeVerbsHeader + strings.Join(verbs, "\n") + "\n"
}

// parseSafeVerbs reads the list format: one verb path per line, blank lines
// and # comments ignored.
func parseSafeVerbs(data string) []string {
	var out []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// safeVerbsPath is the list as it is installed into the package output
// (default.nix copies it to share/pg-desk/safe-verbs.txt).
const safeVerbsPath = "../../share/pg-desk/safe-verbs.txt"

// TestVerbExhaustiveness is the guard: it runs the three checks below as
// subtests, in order (the classification and the list are cheap and fail
// first, then the verbs run).
func TestVerbExhaustiveness(t *testing.T) {
	t.Run("every verb is classified", checkVerbTreeIsFullyClassified)
	t.Run("safe-verb list matches the tree", checkSafeVerbListMatchesTree)
	t.Run("safe verbs run on a schema-2 store", checkEverySafeVerbRunsOnASchema2Store)
}

// checkVerbTreeIsFullyClassified: every leaf verb of the tree has exactly one
// entry in the table and every entry names a verb that exists; an unsafe
// entry carries a reason, a safe one does not.
func checkVerbTreeIsFullyClassified(t *testing.T) {
	tree := leafVerbs()
	if len(tree) < 10 {
		t.Fatalf("walked only %d verbs: the walk is broken", len(tree))
	}
	for _, problem := range classificationProblems(tree, verbClasses()) {
		t.Error(problem)
	}
}

// classificationProblems lists what is wrong with a classification table
// against a verb tree: a verb with no entry, an entry for no verb, a safe
// entry with a reason, an unsafe entry without one.
func classificationProblems(tree []string, classes map[string]verbClass) []string {
	var out []string
	inTree := map[string]bool{}
	for _, v := range tree {
		inTree[v] = true
		if _, ok := classes[v]; !ok {
			out = append(out, fmt.Sprintf("verb %q has no classification: add it to verbClasses (safe with the arguments to run it, or unsafe with a reason) and regenerate share/pg-desk/safe-verbs.txt", v))
		}
	}
	for v, c := range classes {
		if !inTree[v] {
			out = append(out, fmt.Sprintf("classification names %q, which is not a leaf verb of the command tree", v))
		}
		if c.Safe && c.Reason != "" {
			out = append(out, fmt.Sprintf("verb %q is safe but carries a reason", v))
		}
		if !c.Safe && strings.TrimSpace(c.Reason) == "" {
			out = append(out, fmt.Sprintf("verb %q is unsafe without a reason", v))
		}
	}
	sort.Strings(out)
	return out
}

// TestClassificationRejectsAnUnclassifiedVerb is the plain negative case for
// the exhaustiveness check: a verb added to the tree without a table entry, a
// stale entry, and an unsafe entry without a reason each fail it.
func TestClassificationRejectsAnUnclassifiedVerb(t *testing.T) {
	classes := verbClasses()
	tree := leafVerbs()
	if got := classificationProblems(tree, classes); len(got) != 0 {
		t.Fatalf("the real table must be clean, got %v", got)
	}
	if got := classificationProblems(append(append([]string{}, tree...), "pr brand-new"), classes); len(got) != 1 || !strings.Contains(got[0], `"pr brand-new" has no classification`) {
		t.Errorf("a new verb without an entry must fail, got %v", got)
	}
	withStale := map[string]verbClass{"pr gone": safe()}
	for k, v := range classes {
		withStale[k] = v
	}
	if got := classificationProblems(tree, withStale); len(got) != 1 || !strings.Contains(got[0], `"pr gone"`) {
		t.Errorf("an entry for a missing verb must fail, got %v", got)
	}
	noReason := map[string]verbClass{}
	for k, v := range classes {
		noReason[k] = v
	}
	noReason["ledger"] = verbClass{}
	if got := classificationProblems(tree, noReason); len(got) != 1 || !strings.Contains(got[0], "without a reason") {
		t.Errorf("an unsafe entry without a reason must fail, got %v", got)
	}
}

// checkSafeVerbListMatchesTree: the file installed as share/pg-desk/safe-verbs.txt
// lists exactly the verbs the table marks safe, and every listed verb exists.
func checkSafeVerbListMatchesTree(t *testing.T) {
	want := safeVerbList(verbClasses())
	if os.Getenv("PG_DESK_UPDATE_SAFE_VERBS") != "" {
		if err := os.MkdirAll(filepath.Dir(safeVerbsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(safeVerbsPath, []byte(renderSafeVerbs(want)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(safeVerbsPath)
	if err != nil {
		t.Fatalf("safe-verb list: %v (regenerate with PG_DESK_UPDATE_SAFE_VERBS=1)", err)
	}
	got := parseSafeVerbs(string(data))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("share/safe-verbs.txt disagrees with the classification table\n got: %q\nwant: %q\nregenerate it with PG_DESK_UPDATE_SAFE_VERBS=1", got, want)
	}
	inTree := map[string]bool{}
	for _, v := range leafVerbs() {
		inTree[v] = true
	}
	for _, v := range got {
		if !inTree[v] {
			t.Errorf("safe-verb list names %q, which is not a leaf verb of the command tree", v)
		}
	}
}

// TestSafeVerbListRejectsDisagreement is the plain negative case for the
// list check: a list with a verb missing, or with an extra one, does not
// equal the table's safe set.
func TestSafeVerbListRejectsDisagreement(t *testing.T) {
	want := safeVerbList(verbClasses())
	same := func(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }
	if !same(parseSafeVerbs(renderSafeVerbs(want)), want) {
		t.Fatal("a freshly rendered list must round-trip")
	}
	if same(parseSafeVerbs(renderSafeVerbs(want[1:])), want) {
		t.Error("a list missing a safe verb must not match")
	}
	if same(parseSafeVerbs(renderSafeVerbs(append(append([]string{}, want...), "no such verb"))), want) {
		t.Error("a list with an extra verb must not match")
	}
}

// storeBreak matches the failures a store migration causes inside the store:
// a table or column the cutover dropped or replaced (the ledger, the old
// annotation columns). A verb prints it on stderr, and doctor on stdout as the
// reason of a failed check, so both streams are read.
//
// oldSchemaRefusal matches a verb refusing the store as an old-schema one; it
// is read on stderr only (status prints the schema version on stdout).
var (
	storeBreak       = regexp.MustCompile(`(?i)no such (table|column)|sql logic error|database is locked`)
	oldSchemaRefusal = regexp.MustCompile(`(?i)old schema|migrate --cutover|unmigrated`)
)

// schemaOrLedgerError returns the schema or ledger error a verb's output
// shows, or "" when it shows none.
func schemaOrLedgerError(res verbResult) string {
	if m := storeBreak.FindString(res.stdout + "\n" + res.stderr); m != "" {
		return m
	}
	return oldSchemaRefusal.FindString(res.stderr)
}

// deploymentStyleConfig renders the keys a deployment's configuration
// carries (what the home-manager module emits), with example values.
func deploymentStyleConfig(serveLog string) string {
	return fmt.Sprintf(`self_login: example-user
actor: example-actor
repos:
  - remote: %s
sync:
  mode: off
pr_lane:
  mode: off
watch:
  pr:
    queries: [example-pr-query]
  issue:
    queries: [example-issue-query]
serve:
  addr: 127.0.0.1:0
  log: %s
`, testRepo, serveLog)
}

// verbFixture is a hermetic environment: a store migrated to schema 2 with a
// seeded pr, a deployment-style configuration, and a pg-connector stand-in on
// PATH that reports every call as a failure (no verb here reaches a network).
type verbFixture struct {
	dir      string
	env      []string
	serveLog string
}

func newVerbFixture(t *testing.T) *verbFixture {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	storePath := filepath.Join(state, "pg-desk", "store.db")
	if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
		t.Fatal(err)
	}
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.UpsertEntity(store.Entity{Repo: testRepo, EntityType: entityTypePR, EntityID: testPR, Facts: `{}`, AsOf: "2026-09-16T00:00:00Z"}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	if err := st.UpsertInterpretation(store.Interpretation{Repo: testRepo, EntityType: entityTypePR, EntityID: testPR, Degraded: true, AsOf: "2026-09-16T00:00:00Z"}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}
	if err := st.Cutover(); err != nil {
		t.Fatalf("cutover: %v", err)
	}
	if v, err := st.SchemaVersion(); err != nil || v != store.NewSchemaVersion {
		t.Fatalf("fixture store is at schema %d (err %v), want %d", v, err, store.NewSchemaVersion)
	}
	for _, e := range []store.Entity{
		{Repo: testRepo, EntityType: "issue", EntityID: testIssue, Facts: `{}`, AsOf: "2026-09-16T00:00:00Z"},
		{Repo: testRepo, EntityType: "issue", EntityID: "other-1", Facts: `{}`, AsOf: "2026-09-16T00:00:00Z"},
		{Repo: testRepo, EntityType: "thread", EntityID: testThread, Facts: `{}`, AsOf: "2026-09-16T00:00:00Z"},
	} {
		if _, err := st.WriteEntityWithLog(e, 0, []string{"created"}, "poll", "2026-09-16T00:00:00Z"); err != nil {
			t.Fatalf("seed %s: %v", e.EntityID, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	serveLog := filepath.Join(dir, "serve.log")
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(deploymentStyleConfig(serveLog)), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	connector := "#!/bin/sh\necho '{\"error\":{\"code\":\"unavailable\",\"message\":\"no connector in this test\"}}'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "pg-connector"), []byte(connector), 0o755); err != nil {
		t.Fatal(err)
	}
	return &verbFixture{
		dir:      dir,
		serveLog: serveLog,
		env: []string{
			"PATH=" + bin + ":/usr/bin:/bin",
			"HOME=" + dir,
			"XDG_STATE_HOME=" + state,
			"XDG_RUNTIME_DIR=" + filepath.Join(dir, "run"),
			"PG_DESK_CONFIG=" + cfgPath,
			verbChildEnv + "=1",
		},
	}
}

// verbResult is one verb run.
type verbResult struct {
	stdout, stderr string
	exit           int
	err            error // non-nil when the process could not be started or timed out
}

// start launches the verb as its own process (a re-execution of this test
// binary, see runVerbChildIfRequested). Only the daemon verb still runs this
// way: its SIGTERM-to-clean-exit contract belongs to a real process.
func (f *verbFixture) start(ctx context.Context, verb string, args []string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	full := append(strings.Fields(verb), args...)
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(append([]string{}, f.env...), verbChildArgsEnv+"="+strings.Join(full, argSep))
	cmd.Stdin = strings.NewReader("")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	return cmd, &out, &errb
}

// run runs one verb IN PROCESS (bead pg2-zpmuzy: a re-executed race-built test
// binary sleeps 1 s at every exit, and this walk used to start about seventy
// of them). It is the same code path as the process the daemon verb still
// gets: the root command tree, the flag parser, the config loader, the store
// open and the exit-code mapping (execute), under this fixture's hermetic
// environment, with the standard streams captured and every flag put back to
// its default afterwards so each verb starts from fresh flag state.
func (f *verbFixture) run(verb string, args []string) (res verbResult) {
	full := append(strings.Fields(verb), args...)
	defer f.applyEnv()()
	restoreStd, readStd, err := captureStd(f.dir)
	if err != nil {
		return verbResult{err: err}
	}
	defer func() {
		restoreStd()
		res.stdout, res.stderr = readStd()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rootCmd.SetArgs(full)
	defer func() {
		rootCmd.SetArgs(nil)
		resetFlags(rootCmd)
	}()
	defer func() {
		// A verb that panics must read as a crash in its stderr, as the
		// child process did, not take the whole test binary down.
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "panic: %v\ngoroutine 1 [running]:\n%s", r, debug.Stack())
			res.exit = 2
		}
	}()
	res.exit = execute(ctx, os.Stderr)
	if ctx.Err() != nil {
		res.err = fmt.Errorf("timed out: %w", ctx.Err())
	}
	return res
}

// applyEnv replaces the whole process environment with the fixture's (as the
// child process had: nothing of the caller's environment leaks in) and
// returns the function that puts the original back.
func (f *verbFixture) applyEnv() func() {
	saved := os.Environ()
	os.Clearenv()
	for _, kv := range f.env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != verbChildEnv {
			_ = os.Setenv(k, v)
		}
	}
	return func() {
		os.Clearenv()
		for _, kv := range saved {
			if k, v, ok := strings.Cut(kv, "="); ok {
				_ = os.Setenv(k, v)
			}
		}
	}
}

// captureStd points os.Stdin at an empty input and os.Stdout and os.Stderr at
// files under dir, and returns the function that restores them and the one
// that reads what was written.
func captureStd(dir string) (restore func(), read func() (stdout, stderr string), err error) {
	outF, err := os.CreateTemp(dir, "verb-stdout-")
	if err != nil {
		return nil, nil, err
	}
	errF, err := os.CreateTemp(dir, "verb-stderr-")
	if err != nil {
		_ = outF.Close()
		return nil, nil, err
	}
	inF, err := os.Open(os.DevNull)
	if err != nil {
		_ = outF.Close()
		_ = errF.Close()
		return nil, nil, err
	}
	origIn, origOut, origErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = inF, outF, errF
	restore = func() {
		os.Stdin, os.Stdout, os.Stderr = origIn, origOut, origErr
		_ = inF.Close()
	}
	read = func() (string, string) {
		o, _ := os.ReadFile(outF.Name())
		e, _ := os.ReadFile(errF.Name())
		_ = outF.Close()
		_ = errF.Close()
		return string(o), string(e)
	}
	return restore, read, nil
}

// resetFlags puts every flag of c and its subcommands back to its default and
// clears its changed mark, so the next in-process run starts from the state a
// fresh process would.
func resetFlags(c *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			var def []string
			if inner := strings.Trim(f.DefValue, "[]"); inner != "" {
				def = strings.Split(inner, ",")
			}
			_ = sv.Replace(def)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

// runDaemon starts a long-running verb, waits for its log to say it is
// listening, stops it with SIGTERM and returns its result.
func (f *verbFixture) runDaemon(verb string, args []string) verbResult {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, out, errb := f.start(ctx, verb, args)
	if err := cmd.Start(); err != nil {
		return verbResult{err: err}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.After(20 * time.Second)
	listening := false
	for !listening {
		select {
		case err := <-done:
			res := verbResult{stdout: out.String(), stderr: errb.String()}
			if ee, ok := err.(*exec.ExitError); ok {
				res.exit = ee.ExitCode()
			}
			res.err = fmt.Errorf("daemon exited before it listened: %v", err)
			return res
		case <-deadline:
			_ = cmd.Process.Kill()
			<-done
			return verbResult{stdout: out.String(), stderr: errb.String(), err: fmt.Errorf("daemon did not listen within 20s")}
		case <-time.After(50 * time.Millisecond):
			if b, err := os.ReadFile(f.serveLog); err == nil && strings.Contains(string(b), "listening") {
				listening = true
			}
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	err := <-done
	res := verbResult{stdout: out.String(), stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.exit = ee.ExitCode()
	}
	return res
}

// checkEverySafeVerbRunsOnASchema2Store runs each safe verb on the migrated
// fixture store. The test fails when a verb's error output names a schema,
// ledger or dropped-table problem, when a verb cannot be started, or when a
// verb crashes (a Go panic).
func checkEverySafeVerbRunsOnASchema2Store(t *testing.T) {
	classes := verbClasses()
	for _, verb := range safeVerbList(classes) {
		c := classes[verb]
		t.Run(strings.ReplaceAll(verb, " ", "_"), func(t *testing.T) {
			f := newVerbFixture(t)
			var res verbResult
			if c.Daemon {
				res = f.runDaemon(verb, c.Args)
			} else {
				res = f.run(verb, c.Args)
			}
			t.Logf("%s %v: exit=%d\nstderr: %s", verb, c.Args, res.exit, strings.TrimSpace(res.stderr))
			if res.err != nil {
				t.Fatalf("%s: %v\nstderr: %s", verb, res.err, res.stderr)
			}
			if strings.Contains(res.stderr, "panic:") || strings.Contains(res.stderr, "goroutine ") {
				t.Fatalf("%s crashed:\n%s", verb, res.stderr)
			}
			if m := schemaOrLedgerError(res); m != "" {
				t.Fatalf("%s hit a schema or ledger error on a schema-2 store (%q):\nstdout: %s\nstderr: %s", verb, m, res.stdout, res.stderr)
			}
			if c.Daemon && res.exit != 0 {
				t.Fatalf("%s did not stop cleanly on SIGTERM: exit %d\n%s", verb, res.exit, res.stderr)
			}
		})
	}
}

// TestSchemaErrorDetectionRejectsAMigrationBreak is the plain negative case
// for the check above: output such as a dropped-table failure, or a verb
// refusing an old-schema store, is a schema error, and ordinary failures are
// not.
func TestSchemaErrorDetectionRejectsAMigrationBreak(t *testing.T) {
	for _, bad := range []verbResult{
		{stderr: "Error: list ledger: SQL logic error: no such table: ledger (1)"},
		{stderr: "Error: store is on the old schema (schema version 1, need 2): run pg-desk migrate --cutover"},
		{stdout: "stranded cycles: FAIL (get annotation: no such column: comment_id)"},
	} {
		if schemaOrLedgerError(bad) == "" {
			t.Errorf("%+v must be recognised as a schema or ledger error", bad)
		}
	}
	for _, ok := range []verbResult{
		{stderr: "Error: refresh: pg-connector: unavailable"},
		{stderr: "heartbeat: source ages not refreshed: pg-connector ledger show: exit status 1"},
		{stdout: "schema_version: 2\nentities: 1\n"},
	} {
		if m := schemaOrLedgerError(ok); m != "" {
			t.Errorf("%+v wrongly matched %q", ok, m)
		}
	}
}

// TestHarnessSeesARealMigrationBreak is the negative control for the whole
// walk: the legacy top-level reconcile lists the entity-to-bead ledger that the
// schema-2 cutover dropped, so running it on the migrated store MUST be flagged. It proves the
// fixture really is at schema 2 and the check really reads the verbs' output;
// without it a harness that ran nothing would pass every verb. If reconcile is
// ever ported to schema 2, point this at another verb the table marks unsafe
// for a dropped-table or dropped-column reason (it was hide until pg2-ifsje
// ported the top-level hide, unhide, wip and feedback verbs).
func TestHarnessSeesARealMigrationBreak(t *testing.T) {
	classes := verbClasses()
	if classes["reconcile"].Safe {
		t.Fatal("this control needs reconcile to be classified unsafe")
	}
	res := newVerbFixture(t).run("reconcile", nil)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if schemaOrLedgerError(res) == "" {
		t.Errorf("legacy reconcile ran clean on a schema-2 store; the check would miss a migration break:\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}
}
