//go:build smoke

package gate_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildPB compiles the pb binary into a fresh t.TempDir() on every call.
// The binary lives for the lifetime of the calling test and is removed when
// that test's TempDir is cleaned up.  Rebuilding per-test (~1-2 s) is
// acceptable given only two smoke tests exist and avoids shared-state bugs
// where the first test's TempDir (and therefore its binary) is deleted before
// the second test runs.
func buildPB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "pb")
	// Module root is two levels up from internal/gate.
	cmd := exec.Command("go", "build", "-o", out, "./cmd/pb")
	cmd.Dir = mustModuleRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build pb: %v\n%s", err, b)
	}
	return out
}

// mustModuleRoot returns the pb module root (dir containing go.mod) by walking up.
func mustModuleRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd() // .../packages/pb/internal/gate
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatal("go.mod not found walking up from test dir")
		}
		d = parent
	}
}

// setupSmokeWorkspace stands up a real pn workspace with two repos (a producer
// holding the gated change, and a terminal whose trivial apply.sh records
// applied-state) using bare remotes + pn init/clone/lock. It returns the
// workspace root. PATH-resolved real pn/bd/git are required (test skips if not).
func setupSmokeWorkspace(t *testing.T) string {
	t.Helper()
	requireBinaries(t, "pn", "bd", "git", "nix")
	isolateBeadsEnv(t) // temp HOME/XDG, scrub BEADS_DIR/WORKSPACE_ROOT, BD_JSON_ENVELOPE=1, git identity
	ws := t.TempDir()
	t.Setenv("PN_WORKSPACE_ROOT", ws)

	remotes := filepath.Join(ws, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}

	// producer bare remote: one commit containing the change we will gate.
	producerBare := filepath.Join(remotes, "producer.git")
	runTool(t, ws, "git", "init", "--bare", "-b", "main", producerBare)
	pw := t.TempDir()
	runTool(t, pw, "git", "clone", "file://"+producerBare, ".")
	writeFile(t, filepath.Join(pw, "change.txt"), "the gated change\n")
	runTool(t, pw, "git", "add", "change.txt")
	runTool(t, pw, "git", "commit", "-m", "the gated change")
	runTool(t, pw, "git", "push", "-u", "origin", "main")

	// terminal (consumer) bare remote: trivial flake + apply.sh.
	consumerBare := filepath.Join(remotes, "consumer.git")
	runTool(t, ws, "git", "init", "--bare", "-b", "main", consumerBare)
	cw := t.TempDir()
	runTool(t, cw, "git", "clone", "file://"+consumerBare, ".")
	writeFile(t, filepath.Join(cw, "flake.nix"), "{ inputs = {}; outputs = { self, ... }: {}; }\n")
	writeFile(t, filepath.Join(cw, "apply.sh"), "#!/bin/sh\nset -e\ntouch applied.txt\n")
	if err := os.Chmod(filepath.Join(cw, "apply.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	runTool(t, cw, "git", "add", "flake.nix", "apply.sh")
	runTool(t, cw, "git", "commit", "-m", "init terminal")
	runTool(t, cw, "git", "push", "-u", "origin", "main")

	// workspace config: wsid set, consumer is the terminal, trivial apply.
	writeFile(t, filepath.Join(ws, "pn-workspace.toml"), ""+
		"[workspace]\n"+
		"name = \"smoke-pb\"\n"+
		"id = \"smoke-pb\"\n"+
		"terminal = \"consumer\"\n"+
		"apply_command = \"./apply.sh\"\n\n"+
		"[repos.consumer]\n"+
		"url = \"file://"+consumerBare+"\"\n\n"+
		"[repos.producer]\n"+
		"url = \"file://"+producerBare+"\"\n")

	runTool(t, ws, "pn", "workspace", "init")
	runTool(t, ws, "pn", "workspace", "clone")
	runTool(t, ws, "pn", "workspace", "lock")
	// `workspace allow` is REQUIRED before apply: apply_command is config-sourced
	// argv, so pn's Apply is TOFU trust-gated (repo-base ADR 0019, beads pg2-oymai /
	// pg2-x2q6o) and aborts with "workspace hooks not trusted" without it. Missing
	// this, no applied-state is ever recorded and TestSmoke_GateLifecycle_HappyPath
	// fails at the apply step — repo-base's own s36 smoke scenario carries the same
	// step for the same reason.
	runTool(t, ws, "pn", "workspace", "allow")

	// isolated embedded-Dolt beads workspace at the root.
	runTool(t, ws, "bd", "init", "--prefix", "pbsm")
	return ws
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// runToolAllow runs a tool and returns (stdout, exitcode) without failing on
// non-zero exit (pb gate check exits 1 when anything is skipped).
func runToolAllow(t *testing.T, dir, name string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	return "", -1
}

func TestSmoke_GateLifecycle_HappyPath(t *testing.T) {
	pb := buildPB(t)
	ws := setupSmokeWorkspace(t)

	// 1-2. deferred follow-up bead + gate it on producer HEAD.
	bead := createDeferredBead(t, ws, "verify the gated change works")
	runTool(t, ws, pb, "gate", "create", "--blocks", bead, "--repo", "producer")
	// 2b. un-defer: the gate now holds it.
	runTool(t, ws, "bd", "update", bead, "--defer", "")

	// 3. blocked: absent from bd ready.
	if inReady(t, ws, bead) {
		t.Fatalf("bead %s must NOT be ready while the gate is open (pre-apply)", bead)
	}

	// 4. pb gate check changes nothing before apply.
	out, _ := runToolAllow(t, ws, pb, "gate", "check", "--json")
	var pre struct {
		Resolved []string `json:"resolved"`
	}
	if err := json.Unmarshal([]byte(firstJSONLine(out)), &pre); err != nil {
		t.Fatalf("parse pre-apply gate check json: %v\n%s", err, out)
	}
	if len(pre.Resolved) != 0 {
		t.Fatalf("pre-apply gate check must resolve nothing, got %v", pre.Resolved)
	}
	if inReady(t, ws, bead) {
		t.Fatalf("bead %s must still be blocked after a no-op pre-apply check", bead)
	}

	// 5. apply (records applied_ref=HEAD for every repo).
	runTool(t, ws, "pn", "workspace", "apply")

	// 6. pb gate check now resolves the gate.
	out2, _ := runToolAllow(t, ws, pb, "gate", "check", "--json")
	var post struct {
		Resolved []string `json:"resolved"`
	}
	if err := json.Unmarshal([]byte(firstJSONLine(out2)), &post); err != nil {
		t.Fatalf("parse post-apply gate check json: %v\n%s", err, out2)
	}
	if len(post.Resolved) == 0 {
		t.Fatalf("post-apply gate check must resolve the gate, got none\n%s", out2)
	}

	// 7. the bead is now ready.
	if !inReady(t, ws, bead) {
		t.Fatalf("bead %s must be ready after apply + gate check resolves the gate", bead)
	}
}

// firstJSONLine returns the first line that looks like a JSON object, so human
// log lines emitted before the --json payload don't break parsing.
func firstJSONLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "{") {
			return ln
		}
	}
	return s
}

func TestSmoke_GateStale_MsPrecision(t *testing.T) {
	pb := buildPB(t)
	ws := setupSmokeWorkspace(t)

	// No apply happens here → producer applied_ref is empty → neither gate can
	// resolve; both are stale-evaluated. bd stores created_at at SECOND
	// granularity, so use a wide 5s separation + a sub-second 2500ms threshold:
	// the older gate (floored age ≥5s) crosses it; the younger (floored age
	// ≤~2s even with subprocess-spawn jitter) does not.
	parseGateID := func(out string) string {
		t.Helper()
		var cr struct {
			Gates []struct {
				GateID string `json:"gate-id"`
			} `json:"gates"`
		}
		if err := json.Unmarshal([]byte(firstJSONLine(out)), &cr); err != nil {
			t.Fatalf("parse gate create json: %v\n%s", err, out)
		}
		if len(cr.Gates) == 0 {
			t.Fatalf("gate create returned no gates\n%s", out)
		}
		return cr.Gates[0].GateID
	}

	beadOld := createDeferredBead(t, ws, "older gated follow-up")
	oldOut := runTool(t, ws, pb, "gate", "create", "--blocks", beadOld, "--repo", "producer", "--json")
	oldGateID := parseGateID(oldOut)
	runTool(t, ws, "bd", "update", beadOld, "--defer", "")

	time.Sleep(5 * time.Second)

	beadNew := createDeferredBead(t, ws, "newer gated follow-up")
	newOut := runTool(t, ws, pb, "gate", "create", "--blocks", beadNew, "--repo", "producer", "--json")
	newGateID := parseGateID(newOut)
	runTool(t, ws, "bd", "update", beadNew, "--defer", "")

	// Act for real with the default convert-to-human handler at a 2500ms threshold.
	out, _ := runToolAllow(t, ws, pb, "gate", "check", "--stale-after=2500ms", "--json")
	var res struct {
		Resolved     []string `json:"resolved"`
		StaleActions []struct {
			GateID string `json:"gate-id"`
			Action string `json:"action"`
		} `json:"stale_actions"`
	}
	if err := json.Unmarshal([]byte(firstJSONLine(out)), &res); err != nil {
		t.Fatalf("parse stale gate check json: %v\n%s", err, out)
	}
	if len(res.Resolved) != 0 {
		t.Fatalf("no gate is applied, so none should resolve; got resolved=%v", res.Resolved)
	}
	// Exactly the older gate (floored age ≥5s) crosses the 2500ms threshold; the younger does not.
	if len(res.StaleActions) != 1 {
		t.Fatalf("expected exactly 1 stale action (the older gate), got %d: %+v", len(res.StaleActions), res.StaleActions)
	}
	if res.StaleActions[0].Action != "convert-to-human" {
		t.Fatalf("expected convert-to-human action, got %q", res.StaleActions[0].Action)
	}
	// The stale gate must be the OLDER one, not the newer one.
	if res.StaleActions[0].GateID != oldGateID {
		t.Fatalf("stale gate must be the older gate %q, got %q (newer gate is %q)",
			oldGateID, res.StaleActions[0].GateID, newGateID)
	}
}
