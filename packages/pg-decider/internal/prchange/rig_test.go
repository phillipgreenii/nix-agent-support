package prchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/parity"
)

// The real-tool rig: the real pg-desk, the real pg-decider and the real
// pg-pr-change run in a hermetic sandbox (own state directory, config, HOME and
// PATH). The one thing that is not real is the forge side: the pg-connector on
// PATH is a script that answers `pr show` and friends from a fixture (the
// parity gate's fake) and takes its failures and its tracker answers from
// control files, the way the fake forge shim of the connector's own tests is
// driven (`fail.*`, a call log, `block`).
//
// The binaries come from the environment, each an absolute path:
//
//	PG_PR_CHANGE_PG_DESK_BIN, PG_PR_CHANGE_PG_DECIDER_BIN, PG_PR_CHANGE_BIN
//	PG_PR_CHANGE_SQLITE3_BIN        (optional; PATH otherwise)
//	PG_PR_CHANGE_REQUIRE_BINARIES   (set: a missing binary FAILS instead of skipping)
const (
	envDeskBin     = "PG_PR_CHANGE_PG_DESK_BIN"
	envDeciderBin  = "PG_PR_CHANGE_PG_DECIDER_BIN"
	envChangeBin   = "PG_PR_CHANGE_BIN"
	envSqliteBin   = "PG_PR_CHANGE_SQLITE3_BIN"
	envRequireBins = "PG_PR_CHANGE_REQUIRE_BINARIES"
)

type binaries struct{ desk, decider, change, sqlite string }

// realBinaries returns the built binaries, skipping the test when they are not
// all provided, unless PG_PR_CHANGE_REQUIRE_BINARIES is set (the nix check sets
// it, so a gate that cannot find its binaries cannot pass by running nothing).
func realBinaries(t *testing.T) binaries {
	t.Helper()
	b := binaries{os.Getenv(envDeskBin), os.Getenv(envDeciderBin), os.Getenv(envChangeBin), os.Getenv(envSqliteBin)}
	if b.desk == "" || b.decider == "" || b.change == "" {
		msg := envDeskBin + ", " + envDeciderBin + " and " + envChangeBin + " are not all set"
		if v := os.Getenv(envRequireBins); v != "" && v != "0" {
			t.Fatalf("%s, and %s is set: the test that runs the real binaries cannot be skipped", msg, envRequireBins)
		}
		t.Skip(msg + ": skipping the test that runs the real binaries")
	}
	for _, p := range []string{b.desk, b.decider, b.change} {
		if !filepath.IsAbs(p) {
			t.Fatalf("%s is not an absolute path", p)
		}
	}
	return b
}

// sqliteBin is the sqlite3 the legacy-row helper and the store assertions use,
// found when first needed.
func (b *binaries) sqliteBin(t *testing.T) string {
	t.Helper()
	if b.sqlite == "" {
		p, err := exec.LookPath("sqlite3")
		if err != nil {
			t.Fatalf("this scenario reads the store and needs sqlite3: set %s or put sqlite3 on PATH", envSqliteBin)
		}
		b.sqlite = p
	}
	return b.sqlite
}

func isExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// unwrapped is the program to run for a nix-wrapped binary: the wrapper
// PREFIXES the real pg-connector onto PATH, which would shadow the fake, so the
// `.<name>-wrapped` sibling is run instead. A script with no such sibling is
// refused rather than run, because the real connector could then be reached.
func unwrapped(t *testing.T, bin string) string {
	t.Helper()
	if w := filepath.Join(filepath.Dir(bin), "."+filepath.Base(bin)+"-wrapped"); isExec(w) {
		return w
	}
	f, err := os.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 64)
	n, _ := f.Read(head)
	if bytes.HasPrefix(head[:n], []byte("#!")) {
		t.Fatalf("%s is a wrapper script with no unwrapped sibling; refusing to run it, the real pg-connector could be on its PATH", bin)
	}
	return bin
}

// rig is one hermetic run.
type rig struct {
	t         *testing.T
	b         binaries
	dir       string
	binDir    string
	ctl       string // control files and logs of the fake forge/tracker
	stateHome string
	lockDir   string
	env       []string
	changeBin string
	fx        *parity.Fixture
}

const rigConfigTmpl = `self_login: phillipgreenii
team_members:
  - teammate
repos:
  - remote: %s
actor: pr-change-rig
sync:
  mode: off
%s`

// newRig builds the sandbox for fixture JSON fxJSON. The store is created and
// cut over to schema 2 only when cutover is true; legacy lists entities whose
// old-schema row (version 0, never hydrated) is written before the cutover.
func newRig(t *testing.T, fxJSON string, cutover bool, legacy ...string) *rig {
	t.Helper()
	return newRigWith(t, fxJSON, rigOpts{cutover: cutover, legacy: legacy})
}

// rigOpts are the variations of one rig.
type rigOpts struct {
	cutover bool
	legacy  []string
	// bins, when set, are the binaries to run instead of the environment's.
	bins *binaries
	// real, when set, routes `search`, `issue create` and `issue show` to a real
	// pg-connector.
	real *realTracker
	// env entries (KEY=value) added to every child process.
	env []string
	// laneMode, when set, is the configured pr_lane.mode (off, plan or apply).
	laneMode string
	// deciderConfig, when set, is the pg-decider configuration file's JSON
	// instead of parity.DeciderConfigJSON (the lookup query and a ceiling far
	// above any fixture).
	deciderConfig string
}

// realTracker is a real pg-connector (and the real bd behind it) that the
// connector shim hands the tracker verbs (search, issue create, issue show) to.
type realTracker struct {
	connector string   // absolute path of the real pg-connector
	env       []string // KEY=value entries the real connector runs under
}

func newRigWith(t *testing.T, fxJSON string, o rigOpts) *rig {
	t.Helper()
	var b binaries
	if o.bins != nil {
		b = *o.bins
	} else {
		b = realBinaries(t)
	}
	cutover, legacy := o.cutover, o.legacy
	fx, err := parity.ParseFixture([]byte(fxJSON))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t: t, b: b, dir: dir, fx: fx,
		binDir: filepath.Join(dir, "bin"), ctl: filepath.Join(dir, "ctl"),
		stateHome: filepath.Join(dir, "state"), lockDir: filepath.Join(dir, "locks"),
	}
	runtimeDir := filepath.Join(dir, "runtime")
	for _, d := range []string{r.binDir, r.ctl, r.stateHome, filepath.Join(dir, "config"), filepath.Join(dir, "home"), filepath.Join(dir, "tmp"), filepath.Join(dir, "work")} {
		must(t, os.MkdirAll(d, 0o755))
	}
	must(t, os.MkdirAll(runtimeDir, 0o700))
	deskExec, deciderExec := unwrapped(t, b.desk), unwrapped(t, b.decider)
	r.changeBin = unwrapped(t, b.change)

	must(t, os.Symlink(deskExec, filepath.Join(r.binDir, "pg-desk")))
	// pg-decider is wrapped in a logging script, so a test can prove the decider
	// did or did not run.
	decider := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %s/decider.log\nexec %s \"$@\"\n", shq(r.ctl), shq(deciderExec))
	must(t, os.WriteFile(filepath.Join(r.binDir, "pg-decider"), []byte(decider), 0o755))
	// pg-connector: the control-file wrapper over the fixture-driven fake.
	base := filepath.Join(dir, "pg-connector-base")
	must(t, os.WriteFile(base, []byte(parity.FakeConnectorScript(fx, filepath.Join(dir, "base-calls.log"))), 0o755))
	must(t, os.WriteFile(filepath.Join(r.binDir, "pg-connector"), []byte(forgeShim(r.ctl, base, o.real)), 0o755))
	for _, f := range []string{"calls.log", "decider.log", "created.log"} {
		must(t, os.WriteFile(filepath.Join(r.ctl, f), nil, 0o644))
	}
	cfg := filepath.Join(dir, "config", "pg-desk.yaml")
	must(t, os.WriteFile(cfg, []byte(fmt.Sprintf(rigConfigTmpl, parity.FixtureRepo, laneConfig(o.laneMode))), 0o644))
	deciderCfg := filepath.Join(dir, "config", "pg-decider.json")
	cfgJSON := o.deciderConfig
	if cfgJSON == "" {
		cfgJSON = parity.DeciderConfigJSON()
	}
	must(t, os.WriteFile(deciderCfg, []byte(cfgJSON), 0o644))
	r.env = []string{
		"PG_DECIDER_CONFIG=" + deciderCfg,
		"PATH=" + r.binDir,
		"HOME=" + filepath.Join(dir, "home"),
		"TMPDIR=" + filepath.Join(dir, "tmp"),
		"XDG_STATE_HOME=" + r.stateHome,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"PG_DESK_CONFIG=" + cfg,
		"PG_PR_CHANGE_LOCK_DIR=" + r.lockDir,
	}
	r.env = append(r.env, o.env...)

	// The store must be the sandbox's own.
	status := r.desk("status")
	if want := filepath.Join(r.stateHome, "pg-desk", "store.db"); !strings.Contains(status.stdout, "store: "+want) {
		t.Fatalf("refusing to run: pg-desk resolves its store elsewhere than %s:\n%s", want, status.stdout)
	}
	r.mustDesk("heartbeat")
	for _, e := range legacy {
		r.legacyRow(e)
	}
	if cutover {
		r.mustDesk("migrate", "--cutover")
	}
	return r
}

// laneConfig is the pr_lane block of the rig's pg-desk configuration.
func laneConfig(mode string) string {
	if mode == "" {
		return ""
	}
	return "pr_lane:\n  mode: " + mode + "\n"
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// forgeShim renders the pg-connector the rig puts on PATH. Controls (files in
// ctl): block (every `pr show` waits until it is removed), fail.pr-show (the
// wire code `pr show` answers with, or "not_found" for the connector's exit 4),
// fail.detail (the wire code `pr files` answers with: a degraded read),
// search.out and search.code (the answer and exit code of `search`),
// work-items.out and work-items.code (the answer and exit code of the decider's
// all-status lookup `issue list --query work-items-all`; the fixture's beads
// answer it when absent),
// fail.create (the wire code `issue create` answers with). Every call is
// appended to calls.log; every `issue create` (its whole argv, a multi-line description included) to created.log.
func forgeShim(ctl, base string, real *realTracker) string {
	c := shq(ctl)
	// With a real tracker, the tracker verbs go to the real pg-connector instead
	// of the control-file answers below.
	realCase := ""
	if real != nil {
		realCase = "'search '*|'issue create '*|'issue show '*)\n  exec env " + strings.Join(quoteAll(real.env), " ") + " " + shq(real.connector) + ` "$@";;` + "\n"
	}
	return `#!/bin/sh
CTL=` + c + `
# The shim's own tools (the test process's PATH); the programs under test keep the sparse PATH they were given.
PATH=` + shq(os.Getenv("PATH")) + `":/usr/bin:/bin"
printf '%s\n' "$*" >> "$CTL/calls.log"
code_answer() {
  # $1 wire code: print the wire error envelope and fail the call.
  printf '{"error":{"code":"%s","message":"%s"}}\n' "$1" "${2:-forced $1}"
  exit 1
}
case "$*" in
` + realCase + `'pr show '*)
  while [ -f "$CTL/block" ]; do sleep 0.05; done
  if [ -f "$CTL/fail.pr-show" ]; then
    mode=$(cat "$CTL/fail.pr-show")
    case "$mode" in
      not_found) exit 4;;
      *) code_answer "$mode" "$(cat "$CTL/fail.pr-show.message" 2>/dev/null || echo "forced $mode")";;
    esac
  fi;;
'pr files '*)
  if [ -f "$CTL/fail.detail" ]; then code_answer "$(cat "$CTL/fail.detail")"; fi;;
'search '*)
  if [ -f "$CTL/search.out" ]; then
    cat "$CTL/search.out"
    exit "$(cat "$CTL/search.code" 2>/dev/null || echo 0)"
  fi
  printf '{"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":0}],"groups":[]}\n'
  exit 0;;
'issue list --query ` + parity.WorkItemsQuery + `'*)
  if [ -f "$CTL/work-items.out" ]; then
    cat "$CTL/work-items.out"
    exit "$(cat "$CTL/work-items.code" 2>/dev/null || echo 0)"
  fi;;
'issue create '*)
  if [ -f "$CTL/fail.create" ]; then code_answer "$(cat "$CTL/fail.create")"; fi
  printf '%s\n' "$*" >> "$CTL/created.log"
  n=0
  if [ -f "$CTL/created.n" ]; then read -r n < "$CTL/created.n"; fi
  n=$((n + 1))
  printf '%s\n' "$n" > "$CTL/created.n"
  printf '{"result":{"id":"bd-esc-%s"}}\n' "$n"
  exit 0;;
esac
exec ` + shq(base) + ` "$@"
`
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = shq(s)
	}
	return out
}

func (r *rig) ctlWrite(name, content string) {
	r.t.Helper()
	must(r.t, os.WriteFile(filepath.Join(r.ctl, name), []byte(content), 0o644))
}

func (r *rig) ctlRemove(name string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.ctl, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.t.Fatal(err)
	}
}

func (r *rig) ctlRead(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.ctl, name))
	must(r.t, err)
	return string(b)
}

// calls is the connector calls whose argv starts with prefix.
func (r *rig) calls(prefix string) []string {
	var out []string
	for _, l := range strings.Split(r.ctlRead("calls.log"), "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// created is the number of `issue create` calls the tracker double saw.
func (r *rig) created() int {
	return strings.Count(r.ctlRead("created.log"), "issue create --title")
}

// deciderRuns is the decider runs that decide (plan or apply). The read-only
// `plan --json` reads the command makes for its run record are listed apart, by
// deciderPlanReads.
func (r *rig) deciderRuns() []string {
	var out []string
	for _, l := range r.deciderLog() {
		if !strings.HasSuffix(l, " --json") {
			out = append(out, l)
		}
	}
	return out
}

func (r *rig) deciderPlanReads() []string {
	var out []string
	for _, l := range r.deciderLog() {
		if strings.HasSuffix(l, " --json") {
			out = append(out, l)
		}
	}
	return out
}

func (r *rig) deciderLog() []string {
	var out []string
	for _, l := range strings.Split(r.ctlRead("decider.log"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

type result struct {
	stdout, stderr string
	code           int
}

func (r *rig) exec(ctx context.Context, bin string, args ...string) result {
	r.t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = r.env
	cmd.Dir = filepath.Join(r.dir, "work")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := result{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee) && ctx.Err() == nil:
		res.code = ee.ExitCode()
	default:
		r.t.Fatalf("run %s %v: %v", filepath.Base(bin), args, err)
	}
	return res
}

func (r *rig) desk(args ...string) result {
	r.t.Helper()
	return r.exec(context.Background(), filepath.Join(r.binDir, "pg-desk"), args...)
}

func (r *rig) mustDesk(args ...string) result {
	r.t.Helper()
	res := r.desk(args...)
	if res.code != 0 {
		r.t.Fatalf("pg-desk %v exited %d:\n%s%s", args, res.code, res.stdout, res.stderr)
	}
	return res
}

// change runs the real pg-pr-change and checks the one invariant every run
// shares: its exit code is never 1 or 9.
func (r *rig) change(args ...string) result {
	r.t.Helper()
	res := r.exec(context.Background(), r.changeBin, args...)
	r.checkExit(res)
	return res
}

func (r *rig) checkExit(res result) {
	r.t.Helper()
	if res.code == 1 || res.code == 9 {
		r.t.Fatalf("the command exited %d, a code it never uses:\n%s", res.code, res.stderr)
	}
}

// legacyRow writes the old-schema row of entity (version 0, never hydrated)
// into the version-1 store, exactly as the cutover leaves every pre-existing
// row.
func (r *rig) legacyRow(entity string) {
	r.t.Helper()
	i := strings.LastIndex(entity, "#")
	repo := entity[:i]
	stmt := fmt.Sprintf("INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, stale, content_hash) VALUES ('%s', 'pr', '%s', '{}', '2026-09-01T00:00:00Z', 0, 'legacy-never-hydrated');", repo, entity)
	r.sqlite(stmt)
}

func (r *rig) storePath() string { return filepath.Join(r.stateHome, "pg-desk", "store.db") }

func (r *rig) sqlite(stmt string) string {
	r.t.Helper()
	out, err := exec.Command(r.b.sqliteBin(r.t), r.storePath(), stmt).CombinedOutput()
	if err != nil {
		r.t.Fatalf("sqlite3 %s: %v: %s", stmt, err, out)
	}
	return strings.TrimSpace(string(out))
}

// neverHydrated is how many PRs satisfy the predicate the
// pg_desk_pr_unhydrated gauge counts (open, active, version 0): the rows
// `serve` would export. It reads the store the sandbox's pg-desk wrote.
func (r *rig) neverHydrated() string {
	r.t.Helper()
	return r.sqlite("SELECT COUNT(*) FROM entity WHERE entity_type = 'pr' AND active = 1 AND version = 0;")
}

func (r *rig) active(entity string) string {
	r.t.Helper()
	return r.sqlite("SELECT active FROM entity WHERE entity_id = '" + entity + "';")
}

// hydrate refreshes every fixture PR and bead, as a daemon feed would have.
func (r *rig) hydrate() {
	r.t.Helper()
	for _, e := range r.fx.Entities {
		r.mustDesk("pr", "refresh", e)
	}
	for _, b := range r.fx.Beads {
		r.mustDesk("issue", "refresh", b.ID)
	}
}

// startBlocked starts the real command with `pr show` blocked, in its own
// process group, and returns once the connector call is in flight (so the lock
// is held). kill ends the whole group.
func (r *rig) startBlocked(args ...string) (kill func()) {
	r.t.Helper()
	r.ctlWrite("block", "")
	before := len(r.calls("pr show"))
	cmd := exec.Command(r.changeBin, args...)
	cmd.Env = r.env
	cmd.Dir = filepath.Join(r.dir, "work")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(r.t, cmd.Start())
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	kill = func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	r.t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	deadline := time.Now().Add(30 * time.Second)
	for len(r.calls("pr show")) <= before {
		if time.Now().After(deadline) {
			kill()
			r.t.Fatal("the blocked run never reached the connector")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return kill
}

// annotation reads decider annotation key of the PR's stored view (the
// decider's own namespace), "" when it is absent.
func (r *rig) annotation(entity, key string) string {
	r.t.Helper()
	res := r.mustDesk("pr", "show", entity, "--json")
	var v struct {
		Annotations struct {
			Decider map[string]map[string]string `json:"decider"`
		} `json:"annotations"`
	}
	must(r.t, json.Unmarshal([]byte(res.stdout), &v))
	return v.Annotations.Decider["pr-decider"][key]
}

// lane is every lane key of the store's meta table, as key to value.
func (r *rig) lane() map[string]string {
	r.t.Helper()
	out := map[string]string{}
	for _, l := range strings.Split(r.sqlite("SELECT key || ' <=> ' || value FROM meta WHERE key LIKE 'pr_lane.%' ORDER BY key;"), "\n") {
		if k, v, ok := strings.Cut(l, " <=> "); ok {
			out[k] = v
		}
	}
	return out
}

// runRecordPath is where the real command writes its run record in the rig
// (the default path under the rig's XDG_STATE_HOME).
func (r *rig) runRecordPath() string {
	return filepath.Join(r.stateHome, "pg-pr-change", RunRecordFileName)
}

// runRecords reads the run record file; a missing file is no records.
func (r *rig) runRecords() []RunRecord {
	r.t.Helper()
	if _, err := os.Stat(r.runRecordPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return readRecords(r.t, r.runRecordPath())
}

// scrape starts the real `pg-desk serve` on a free loopback port of the
// sandbox's store, GETs /metrics once the server answers, stops it and returns
// the exposition text. It is how the lane gauges (exported at scrape time from
// the persisted lane state) are read the way a scraper reads them.
func (r *rig) scrape() string {
	r.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(r.t, err)
	addr := l.Addr().String()
	must(r.t, l.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, filepath.Join(r.binDir, "pg-desk"), "serve", "--addr", addr)
	cmd.Env = r.env
	cmd.Dir = filepath.Join(r.dir, "work")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	must(r.t, cmd.Start())
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		cancel()
		<-done
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/metrics")
		if err == nil {
			body, rerr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if rerr == nil && resp.StatusCode == http.StatusOK {
				return string(body)
			}
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("pg-desk serve never answered /metrics on %s (last error %v): %s", addr, err, errb.String())
		}
		select {
		case <-done:
			r.t.Fatalf("pg-desk serve exited early: %s", errb.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// gauge reads one unlabeled sample of a metrics exposition ("" when absent).
func gauge(exposition, name string) string {
	for _, l := range strings.Split(exposition, "\n") {
		if v, ok := strings.CutPrefix(l, name+" "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
