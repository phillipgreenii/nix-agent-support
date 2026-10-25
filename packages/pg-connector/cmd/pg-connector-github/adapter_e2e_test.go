package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
)

// The pg-router source adapter against the real stack, end to end: the real
// daemon child (the assembled serve over the counting fake gh), the real client
// binary the umbrella execs, the real umbrella binary and the real adapter
// binary, each a separate process. No tier is faked.
//
// The adapter is a module of its own (packages/pg-router-source-pg-connector)
// whose nix check source holds only that directory, and the daemon harness
// (servetest) is internal to this command, so neither module can host a test of
// the whole chain from its own side. This package can: it owns the harness, the
// umbrella is in its module, and the adapter is a separate PROCESS it only
// execs. Where the adapter binary comes from is adapterBinary's business.
//
// The scenario is the one an operator meets: an unscoped consumer (no query)
// polls through the adapter, the daemon's store is replaced by a file that is
// not a database (the daemon moves it aside and starts empty), the consumer's
// cursor is then expired, the adapter surfaces that as exit 1, an empty stdout
// and a cursor_expired line on stderr, and the umbrella's --reset recovers it.

const (
	adapterBinaryName  = "pg-router-source-pg-connector"
	adapterE2EBackend  = "e2e-pr-github"
	adapterE2EConsumer = "e2e-unscoped-consumer"
	// adapterE2EBuildDeadline is a hang guard for a cold go build under load.
	adapterE2EBuildDeadline = 8 * time.Minute
)

// adapterE2EBuild go-builds pkg (relative to dir, which holds the go.mod) into
// a fresh directory and returns the binary path. The build runs without any
// inherited GIT_* variable, which the commit-time test hook exports.
func adapterE2EBuild(t *testing.T, dir, pkg, name string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), adapterE2EBuildDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", out, pkg)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s in %s: %v\n%s", pkg, dir, err, b)
	}
	return out
}

// adapterBinary returns the directory holding the adapter binary. In a source
// checkout the sibling module is built from source, so the test never runs a
// stale installed copy. Where the sibling module is not part of the source (the
// nix check's source is this module only), the adapter must already be on PATH,
// and the check supplies it as a test dependency; its absence there is a
// failure, never a skip, so the check cannot go green without the chain.
func adapterBinary(t *testing.T, moduleRoot string) string {
	t.Helper()
	sibling := filepath.Join(moduleRoot, "..", adapterBinaryName)
	if _, err := os.Stat(filepath.Join(sibling, "go.mod")); err == nil {
		return filepath.Dir(adapterE2EBuild(t, sibling, "./cmd/"+adapterBinaryName, adapterBinaryName))
	}
	path, err := exec.LookPath(adapterBinaryName)
	if err != nil {
		t.Fatalf("the adapter is neither a sibling module of %s nor on PATH: %v", moduleRoot, err)
	}
	return filepath.Dir(path)
}

// adapterRun is one invocation of the adapter binary.
type adapterRun struct {
	stdout, stderr string
	exit           int
}

// adapterE2ERig is the environment the adapter and the umbrella run in.
type adapterE2ERig struct {
	env []string
}

// run execs the binary under the rig's environment.
func (r adapterE2ERig) run(t *testing.T, binary string, args ...string) adapterRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = "/"
	cmd.Env = r.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := adapterRun{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.exit = ee.ExitCode()
	default:
		t.Fatalf("exec %s %v: %v", binary, args, err)
	}
	return res
}

func TestAdapterE2E_AnUnscopedConsumersCursorExpiredReachesTheAdapterAndResetRecovers(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	adapterDir := adapterBinary(t, moduleRoot)
	umbrella := adapterE2EBuild(t, moduleRoot, "./cmd/pg-connector", "pg-connector")

	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)

	// The umbrella's registry names the real client binary (a bare name, found on
	// PATH) against the rendered daemon config, as a deployment does.
	client := servetest.Binary(t, "", "")
	home := t.TempDir()
	registry := filepath.Join(home, "registry.yaml")
	manifest := fmt.Sprintf("connector:\n  pr:\n    - name: %s\n      command:\n        - %s\n        - --config\n        - %s\n        - pr\n",
		adapterE2EBackend, filepath.Base(client), d.ConfigPath)
	if err := os.WriteFile(registry, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	rig := adapterE2ERig{env: []string{
		// The adapter finds the umbrella by name on PATH, as in production.
		"PATH=" + filepath.Dir(umbrella) + string(os.PathListSeparator) + filepath.Dir(client) + string(os.PathListSeparator) + adapterDir + string(os.PathListSeparator) + servetest.DaemonPath(),
		"HOME=" + home,
		"XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"PG_PR_CONFIG=" + registry,
	}}
	adapter := filepath.Join(adapterDir, adapterBinaryName)
	poll := func() adapterRun {
		t.Helper()
		// No query argument: the consumer is unscoped.
		return rig.run(t, adapter, "changes", "pr", "--consumer", adapterE2EConsumer)
	}
	wantNoItems := func(what string, res adapterRun) {
		t.Helper()
		if res.exit != 0 || strings.TrimSpace(res.stdout) != "[]" || res.stderr != "" {
			t.Fatalf("%s: exit=%d stdout=%q stderr=%q, want exit 0, no items and no stderr", what, res.exit, res.stdout, res.stderr)
		}
	}

	// The first poll registers the consumer at the tail of the feed.
	wantNoItems("the first poll", poll())

	// Replace the daemon's store with a file that is not a database; the daemon
	// moves it aside and starts empty, so the consumer's cursor is expired.
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("SIGTERM exit = %d; output: %s", code, d.Stderr())
	}
	sidecars, _ := filepath.Glob(d.StorePath() + "-*")
	for _, f := range sidecars {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(d.StorePath(), []byte(strings.Repeat("this is not a sqlite database; ", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	d.Start(t)
	confAwaitBaseline(t, d, servetest.QueryMine)

	// The adapter fails the call, prints nothing on stdout and names the cause
	// on stderr, where pg-router counts it as a source failure.
	res := poll()
	if res.exit != 1 || res.stdout != "" {
		t.Fatalf("after a moved-aside store: exit=%d stdout=%q stderr=%q, want exit 1 and an empty stdout", res.exit, res.stdout, res.stderr)
	}
	if want := adapterE2EBackend + ": cursor_expired:"; !strings.Contains(res.stderr, want) {
		t.Fatalf("stderr = %q, want a line containing %q", res.stderr, want)
	}
	// The failure is stable: the adapter does not advance or reset the cursor.
	if again := poll(); again.exit != 1 || again.stdout != "" || !strings.Contains(again.stderr, "cursor_expired:") {
		t.Fatalf("a second poll: exit=%d stdout=%q stderr=%q, want the same failure", again.exit, again.stdout, again.stderr)
	}

	// The operator's recovery step is the umbrella's --reset, which replays the
	// watched members of the feed.
	reset := rig.run(t, umbrella, "pr", "changes", "--consumer", adapterE2EConsumer, "--reset", "--output", "json")
	if reset.exit != 0 {
		t.Fatalf("the umbrella --reset: exit=%d stdout=%q stderr=%q", reset.exit, reset.stdout, reset.stderr)
	}
	var replay struct {
		Changes []struct {
			Change string `json:"change"`
			Entity struct {
				ID string `json:"id"`
			} `json:"entity"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(reset.stdout), &replay); err != nil {
		t.Fatalf("decode the reset wire: %v (stdout=%s)", err, reset.stdout)
	}
	var got []string
	for _, c := range replay.Changes {
		got = append(got, c.Change+" "+c.Entity.ID)
	}
	slices.Sort(got)
	want := []string{"added " + confPRA.Ref(), "added " + confPRB.Ref()}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the reset replays %v, want the watched members %v", got, want)
	}

	// The consumer is healthy again through the adapter, which sees the feed
	// from the replay's acknowledged position on.
	wantNoItems("the poll after the reset", poll())
}
