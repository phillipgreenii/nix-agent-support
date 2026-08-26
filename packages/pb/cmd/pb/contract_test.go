//go:build contract

// Contract tests pin pb's REAL external surfaces against drift: the bd gate
// surface, the co-location invariant, the multi-DB dedupe key, the git patch-id
// behaviour, and the pn workspace-info schema. They drive real bd/git (and
// optionally pn) and are deliberately NOT part of `nix flake check` / the pure
// sandbox — run them on a dev machine or CI:
//
//	cd packages/pb && go test -tags contract -p 1 ./...
//
// Each test skips when its binary is absent. All bd/git state is isolated to a
// temp HOME/XDG so real bd uses an EMBEDDED Dolt DB (never the shared :25252
// server) and git config writes stay in temp.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

// ---------------------------------------------------------------------------
// Shared isolation + exec helpers
// ---------------------------------------------------------------------------

func skipNoBin(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not on PATH", n)
		}
	}
}

// hermeticEnviron returns os.Environ() with the git-hook-injected vars
// (GIT_DIR, GIT_INDEX_FILE, GIT_WORK_TREE, GIT_PREFIX, GIT_OBJECT_DIRECTORY,
// GIT_COMMON_DIR) removed. Those vars repoint a test's own tmpdir git/bd
// subprocesses at the REAL repo when the test binary itself runs from inside
// a git commit hook (e.g. this repo's own pre-commit/pre-push test gate) --
// the same hermeticity leak packages/pb/internal/patchid/patchid_test.go had
// before 98f8c95d. Duplicated per-package rather than shared/exported:
// patchid_test.go's copy of this helper is unexported and test-only, so it
// cannot be imported from another package.
func hermeticEnviron() []string {
	skipVars := map[string]bool{
		"GIT_DIR": true, "GIT_INDEX_FILE": true, "GIT_WORK_TREE": true,
		"GIT_PREFIX": true, "GIT_OBJECT_DIRECTORY": true, "GIT_COMMON_DIR": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		if k := strings.SplitN(kv, "=", 2)[0]; !skipVars[k] {
			env = append(env, kv)
		}
	}
	return env
}

// hermeticCLIRunner wraps run.CLIRunner, filling in hermeticEnviron() whenever
// the caller leaves opts.Env nil -- patchid.go's and pn/info.go's git/pn
// invocations both do, so without this TestContract_GitPatchID and
// TestContract_PNInfoSchema would inherit the same leak.
type hermeticCLIRunner struct{}

func (hermeticCLIRunner) Run(ctx context.Context, name string, args []string, opts run.Options) (run.Result, error) {
	if opts.Env == nil {
		opts.Env = hermeticEnviron()
	}
	return run.CLIRunner{}.Run(ctx, name, args, opts)
}

// isolate pins HOME + XDG_* to a temp dir, scrubs workspace-binding vars, sets
// BD_JSON_ENVELOPE=1 and a deterministic git identity. The result: real bd boots
// an embedded Dolt DB and git is hermetic.
func isolate(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	for k, sub := range map[string]string{
		"HOME":            "home",
		"XDG_DATA_HOME":   "data",
		"XDG_STATE_HOME":  "state",
		"XDG_CONFIG_HOME": "cfg",
	} {
		dir := filepath.Join(base, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(k, dir)
	}
	for _, k := range []string{"BEADS_DIR", "WORKSPACE_ROOT", "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	t.Setenv("BD_JSON_ENVELOPE", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@e.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@e.com")
	t.Setenv("GIT_EDITOR", "true")
	t.Setenv("GIT_SEQUENCE_EDITOR", "true")
}

// shellOut runs name in dir, returning stdout; on error it fails the test.
func shellOut(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	out, err := shellTry(dir, name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return out
}

// shellTry runs name in dir, returning (stdout, error-with-stderr). Does not fail.
func shellTry(dir, name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = hermeticEnviron()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func bdInit(t *testing.T, dir, prefix string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shellOut(t, dir, "bd", "init", "--prefix", prefix)
}

func dataID(t *testing.T, jsonOut string) string {
	t.Helper()
	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &env); err != nil {
		t.Fatalf("parse .data.id: %v\n%s", err, jsonOut)
	}
	return env.Data.ID
}

func readyIDs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := shellOut(t, dir, "bd", "ready", "--json")
	var env struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse bd ready: %v\n%s", err, out)
	}
	set := map[string]bool{}
	for _, b := range env.Data {
		set[b.ID] = true
	}
	return set
}

// ---------------------------------------------------------------------------
// bd gate surface
// ---------------------------------------------------------------------------

func TestContract_BDGateSurface(t *testing.T) {
	skipNoBin(t, "bd")
	isolate(t)
	if v, err := shellTry("", "bd", "version"); err == nil {
		t.Logf("bd version: %s", strings.TrimSpace(v)) // surface a future bump
	}
	ws := t.TempDir()
	bdInit(t, ws, "ct")

	bead := dataID(t, shellOut(t, ws, "bd", "create", "verify", "--json"))
	if !readyIDs(t, ws)[bead] {
		t.Fatalf("freshly created bead %s should be ready", bead)
	}

	createOut := shellOut(t, ws, "bd", "gate", "create", "--type=pn:applied",
		"--blocks", bead, "--await-id", "home:repo-a:abc123", "--reason", "pn:applied gate", "--json")
	gate := dataID(t, createOut)
	if gate == "" {
		t.Fatalf("gate create returned no id: %s", createOut)
	}

	// gate list carries the envelope + the pn:applied fields + created_at.
	listOut := shellOut(t, ws, "bd", "gate", "list", "--limit", "0", "--json")
	var listEnv struct {
		Data []struct {
			ID        string            `json:"id"`
			IssueType string            `json:"issue_type"`
			AwaitType string            `json:"await_type"`
			AwaitID   string            `json:"await_id"`
			CreatedAt string            `json:"created_at"`
			Metadata  map[string]string `json:"metadata"`
		} `json:"data"`
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(listOut), &listEnv); err != nil {
		t.Fatalf("parse gate list envelope: %v\n%s", err, listOut)
	}
	if listEnv.SchemaVersion == 0 {
		t.Errorf("expected schema_version in envelope: %s", listOut)
	}
	var found *int
	for i := range listEnv.Data {
		if listEnv.Data[i].ID == gate {
			found = &i
			break
		}
	}
	if found == nil {
		t.Fatalf("gate %s not in gate list: %s", gate, listOut)
	}
	g := listEnv.Data[*found]
	if g.IssueType != "gate" || g.AwaitType != "pn:applied" || g.AwaitID != "home:repo-a:abc123" {
		t.Errorf("gate surface drift: %+v", g)
	}
	if g.CreatedAt == "" {
		t.Errorf("gate missing created_at (needed for stale-age): %+v", g)
	}

	// the gate holds the bead out of `bd ready`.
	if readyIDs(t, ws)[bead] {
		t.Fatalf("bead %s must NOT be ready while gate %s is open", bead, gate)
	}

	// set-metadata round-trips into gate list metadata.
	shellOut(t, ws, "bd", "update", gate, "--set-metadata", "applied_baseline=base1")
	listOut2 := shellOut(t, ws, "bd", "gate", "list", "--limit", "0", "--json")
	if err := json.Unmarshal([]byte(listOut2), &listEnv); err != nil {
		t.Fatalf("re-parse gate list: %v", err)
	}
	for _, gg := range listEnv.Data {
		if gg.ID == gate && gg.Metadata["applied_baseline"] != "base1" {
			t.Errorf("applied_baseline did not round-trip: %+v", gg)
		}
	}

	// resolve unblocks the bead.
	shellOut(t, ws, "bd", "gate", "resolve", gate, "--reason", "applied")
	if !readyIDs(t, ws)[bead] {
		t.Fatalf("bead %s must be ready after gate %s resolves", bead, gate)
	}
}

// ---------------------------------------------------------------------------
// co-location invariant: a cross-DB blocks edge does NOT hold a bead
// ---------------------------------------------------------------------------

func TestContract_CrossDBBlockDoesNotHold(t *testing.T) {
	skipNoBin(t, "bd")
	isolate(t)
	dbA := filepath.Join(t.TempDir(), "a")
	dbB := filepath.Join(t.TempDir(), "b")
	bdInit(t, dbA, "cta")
	bdInit(t, dbB, "ctb")

	beadB := dataID(t, shellOut(t, dbB, "bd", "create", "lives-in-B", "--json"))

	// Attempt to gate beadB from DB-A. bd may reject a foreign --blocks id or may
	// accept a dangling edge; either way the invariant is the same: the gate in
	// DB-A cannot hold beadB out of DB-B's `bd ready`.
	if _, err := shellTry(dbA, "bd", "gate", "create", "--type=pn:applied",
		"--blocks", beadB, "--await-id", "home:r:p", "--json"); err != nil {
		t.Logf("cross-DB gate create rejected (also proves co-location is required): %v", err)
	}
	if !readyIDs(t, dbB)[beadB] {
		t.Fatalf("bead %s in DB-B must remain ready despite a gate in DB-A — gates MUST be co-located", beadB)
	}
}

// ---------------------------------------------------------------------------
// multi-DB dedupe key (real metadata field names + the dedupe logic)
// ---------------------------------------------------------------------------

func TestContract_MultiDBDedupeKey(t *testing.T) {
	skipNoBin(t, "bd")
	isolate(t)

	// Pin the REAL metadata.json field names discover relies on.
	real := t.TempDir()
	bdInit(t, real, "ctm")
	raw, err := os.ReadFile(filepath.Join(real, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("read real .beads/metadata.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse real metadata.json: %v\n%s", err, raw)
	}
	if _, ok := m["dolt_database"]; !ok {
		t.Errorf("real metadata.json missing dolt_database (discover dedupe key drift): %s", raw)
	}
	if _, ok := m["project_id"]; !ok {
		t.Errorf("real metadata.json missing project_id (discover dedupe key drift): %s", raw)
	}

	// Dedupe logic against the shared-server metadata shape (host+port+db+project).
	root := t.TempDir()
	writeServerBeads := func(name, host, port, db, project string) string {
		dir := filepath.Join(root, name)
		beads := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beads, 0o755); err != nil {
			t.Fatal(err)
		}
		meta := fmt.Sprintf(`{"dolt_server_host":%q,"dolt_database":%q,"project_id":%q}`, host, db, project)
		if err := os.WriteFile(filepath.Join(beads, "metadata.json"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beads, "dolt-server.port"), []byte(port+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	a := writeServerBeads("repo-a", "127.0.0.1", "25252", "pg2", "proj-1")
	b := writeServerBeads("repo-b", "127.0.0.1", "25252", "pg2", "proj-1") // same identity as a
	c := writeServerBeads("repo-c", "127.0.0.1", "25252", "pg2", "proj-2") // distinct project

	same, err := discover.DistinctDBs([]string{a, b}, root)
	if err != nil {
		t.Fatalf("DistinctDBs(same): %v", err)
	}
	if len(same) != 1 {
		t.Errorf("two .beads with identical Dolt identity must dedupe to 1, got %d: %+v", len(same), same)
	}
	distinct, err := discover.DistinctDBs([]string{a, c}, root)
	if err != nil {
		t.Fatalf("DistinctDBs(distinct): %v", err)
	}
	if len(distinct) != 2 {
		t.Errorf("distinct project_id must yield 2 DBs, got %d: %+v", len(distinct), distinct)
	}
}

// ---------------------------------------------------------------------------
// git patch-id behaviour (rebase-stable / near-context miss / squash loss /
// binary / --stable≠--verbatim)
// ---------------------------------------------------------------------------

func TestContract_GitPatchID(t *testing.T) {
	skipNoBin(t, "git")
	isolate(t)
	c := patchid.Client{R: hermeticCLIRunner{}}
	ctx := context.Background()

	newRepo := func(name string) string {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		shellOut(t, dir, "git", "init", "-b", "main")
		shellOut(t, dir, "git", "config", "commit.gpgsign", "false")
		return dir
	}
	lines := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "line" + strconv.Itoa(i+1)
		}
		return out
	}
	write := func(dir, name string, content []string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(content, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commitAll := func(dir, msg string) {
		shellOut(t, dir, "git", "add", "-A")
		shellOut(t, dir, "git", "commit", "-m", msg)
	}

	t.Run("rebase-stable-and-found-by-scan", func(t *testing.T) {
		dir := newRepo("stable")
		base := lines(30)
		write(dir, "f.txt", base)
		commitAll(dir, "base")
		shellOut(t, dir, "git", "checkout", "-b", "feature")
		feat := append([]string{}, base...)
		feat[19] = "CHANGED20"
		write(dir, "f.txt", feat)
		commitAll(dir, "feat")
		orig, err := c.Compute(ctx, dir, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		// far edit on main (line2), then rebase feature onto it.
		shellOut(t, dir, "git", "checkout", "main")
		far := append([]string{}, base...)
		far[1] = "MAIN2"
		write(dir, "f.txt", far)
		commitAll(dir, "main-far")
		shellOut(t, dir, "git", "checkout", "feature")
		shellOut(t, dir, "git", "rebase", "main")
		after, err := c.Compute(ctx, dir, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if after != orig {
			t.Errorf("patch-id must survive a far rebase: orig=%s after=%s", orig, after)
		}
		// pb gate check finds it via the bounded scan.
		set, err := c.ScanPatchIDs(ctx, dir, "-n 10 HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if !set[orig] {
			t.Errorf("bounded scan did not find rebased patch-id %s in %v", orig, set)
		}
	})

	t.Run("near-context-rebase-misses", func(t *testing.T) {
		dir := newRepo("near")
		base := lines(30)
		write(dir, "f.txt", base)
		commitAll(dir, "base")
		shellOut(t, dir, "git", "checkout", "-b", "feature")
		feat := append([]string{}, base...)
		feat[19] = "CHANGED20"
		write(dir, "f.txt", feat)
		commitAll(dir, "feat")
		orig, err := c.Compute(ctx, dir, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		shellOut(t, dir, "git", "checkout", "main")
		near := append([]string{}, base...)
		near[17] = "MAIN18" // within the 3-line diff context of line 20
		write(dir, "f.txt", near)
		commitAll(dir, "main-near")
		shellOut(t, dir, "git", "checkout", "feature")
		shellOut(t, dir, "git", "rebase", "main")
		set, err := c.ScanPatchIDs(ctx, dir, "-n 10 HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if set[orig] {
			t.Errorf("near-context rebase MUST change the patch-id (stale-handler territory); orig %s still present", orig)
		}
	})

	t.Run("squash-loses-component-patch-ids", func(t *testing.T) {
		dir := newRepo("squash")
		base := lines(30)
		write(dir, "f.txt", base)
		commitAll(dir, "base")
		baseSHA := strings.TrimSpace(shellOut(t, dir, "git", "rev-parse", "HEAD"))
		x := append([]string{}, base...)
		x[4] = "X5"
		write(dir, "f.txt", x)
		commitAll(dir, "X")
		px, err := c.Compute(ctx, dir, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		y := append([]string{}, x...)
		y[9] = "Y10"
		write(dir, "f.txt", y)
		commitAll(dir, "Y")
		py, err := c.Compute(ctx, dir, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		shellOut(t, dir, "git", "reset", "--soft", baseSHA)
		commitAll(dir, "Z (squash)")
		set, err := c.ScanPatchIDs(ctx, dir, baseSHA+"..HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if set[px] || set[py] {
			t.Errorf("squash MUST lose the component patch-ids; px-present=%v py-present=%v", set[px], set[py])
		}
	})

	t.Run("binary-yields-patch-id", func(t *testing.T) {
		dir := newRepo("binary")
		if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0, 1, 2, 3, 0xff, 0xfe, 0xfd}, 0o644); err != nil {
			t.Fatal(err)
		}
		commitAll(dir, "bin")
		id, err := c.Compute(ctx, dir, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if id == "" {
			t.Error("a binary change must still yield a patch-id")
		}
	})

	t.Run("stable-differs-from-verbatim", func(t *testing.T) {
		dir := newRepo("stablever")
		base := lines(30)
		write(dir, "f.txt", base)
		commitAll(dir, "base")
		two := append([]string{}, base...)
		two[4] = "D5"
		two[24] = "D25"
		write(dir, "f.txt", two)
		commitAll(dir, "two-hunks")
		stable, err := c.Compute(ctx, dir, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		show := shellOut(t, dir, "git", "show", "HEAD")
		var sout, serr bytes.Buffer
		vc := exec.Command("git", "-C", dir, "patch-id", "--verbatim")
		vc.Env = hermeticEnviron()
		vc.Stdin = strings.NewReader(show)
		vc.Stdout = &sout
		vc.Stderr = &serr
		if err := vc.Run(); err != nil {
			t.Fatalf("git patch-id --verbatim: %v\n%s", err, serr.String())
		}
		verbatim := strings.Fields(sout.String())[0]
		if stable == verbatim {
			t.Errorf("--stable and --verbatim must differ (pb pins --stable); both=%s", stable)
		}
	})
}

// ---------------------------------------------------------------------------
// pn workspace info schema (opt-in; Phase-3 smoke harness covers the live path)
// ---------------------------------------------------------------------------

func TestContract_PNInfoSchema(t *testing.T) {
	skipNoBin(t, "pn")
	ws := os.Getenv("PB_CONTRACT_PN_WS")
	if ws == "" {
		t.Skip("set PB_CONTRACT_PN_WS=<a real pn workspace dir> to run the live pn info schema check; " +
			"the Phase-3 smoke harness otherwise covers the live path")
	}
	info, err := pn.Client{R: hermeticCLIRunner{}}.Info(context.Background(), ws)
	if err != nil {
		t.Fatalf("pn workspace info in %q: %v", ws, err)
	}
	if info.Root == "" {
		t.Error("pn info root must be non-empty")
	}
	for _, r := range info.Repos {
		if r.Path == "" {
			t.Errorf("repo %q has empty path: %+v", r.Name, r)
		}
	}
}
