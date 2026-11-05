//go:build integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/freshness"
)

// Integration tier (V-4): this test builds the real pg-connector umbrella and
// the real pg-connector-github backend client, and runs them as processes, so
// it is not part of the unit gate. It runs with `go test -tags integration`.

// buildConnectorBinaries builds the umbrella and the GitHub backend client from
// the sibling pg-connector module into one fresh directory and returns it. The
// client is built with a short call budget, so a daemon that is not there is
// answered in about a second rather than after the production retry window.
func buildConnectorBinaries(t *testing.T) string {
	t.Helper()
	// A caller that already has both binaries (the nix check, whose sandbox
	// cannot build a sibling module) names them and nothing is built.
	if um, gh := os.Getenv("PG_DESK_E2E_PG_CONNECTOR"), os.Getenv("PG_DESK_E2E_PG_CONNECTOR_GITHUB"); um != "" && gh != "" {
		binDir := t.TempDir()
		for name, target := range map[string]string{"pg-connector": um, "pg-connector-github": gh} {
			if err := os.Symlink(target, filepath.Join(binDir, name)); err != nil {
				t.Fatal(err)
			}
		}
		return binDir
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go toolchain on PATH to build the real binaries: %v", err)
	}
	modDir, err := filepath.Abs(filepath.Join("..", "..", "..", "pg-connector"))
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	for _, b := range []struct{ pkg, name, ldflags string }{
		{"./cmd/pg-connector", "pg-connector", ""},
		{"./cmd/pg-connector-github", "pg-connector-github", "-X main.clientBudgetOverride=2s"},
	} {
		args := []string{"build", "-o", filepath.Join(binDir, b.name)}
		if b.ldflags != "" {
			args = append(args, "-ldflags", b.ldflags)
		}
		args = append(args, b.pkg)
		cmd := exec.Command(goBin, args...)
		cmd.Dir = modDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", b.pkg, err, out)
		}
	}
	return binDir
}

// shortDir returns a short temp directory: a unix socket path has a hard length
// limit that t.TempDir() can exceed.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "pgd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

const daemonDownLedgerBefore = `[
 {"type":"pr","backend":"pg-connector-pr-github","query":"mine","consumers":{},"refreshed_at":"2026-10-06T11:00:00Z","last_error":null},
 {"type":"pr","backend":"pg-connector-pr-github","query":"team","consumers":{},"refreshed_at":"2026-10-06T11:00:00Z","last_error":null}]`

// A ledger a healthy probe would leave behind: the daemon answered, so the
// connector stamped a newer whole-query origin answer.
const daemonUpLedgerAfter = `[
 {"type":"pr","backend":"pg-connector-pr-github","query":"mine","consumers":{},"refreshed_at":"2026-10-06T11:59:00Z","last_error":null},
 {"type":"pr","backend":"pg-connector-pr-github","query":"team","consumers":{},"refreshed_at":"2026-10-06T11:59:00Z","last_error":null}]`

// TestHeartbeatProbeWithTheDaemonDownLeavesTheSourceRowsStale (V-3) drives the
// heartbeat's real freshness probe (no stubbed heartbeatRunConnector) through
// the real pg-connector umbrella and the real GitHub backend client, with a
// socket nothing listens on: the probe's `pr changes` fails, the heartbeat
// still exits 0, stderr names the query as not probed, and the
// source_fetch.pg-connector-pr-github rows do not advance, so they age into
// stale.
func TestHeartbeatProbeWithTheDaemonDownLeavesTheSourceRowsStale(t *testing.T) {
	binDir := buildConnectorBinaries(t)

	// The backend client's config names a socket with no daemon behind it.
	state := shortDir(t)
	backendCfg := filepath.Join(state, "backend.json")
	writeFile(t, backendCfg, `{"state_dir":"`+filepath.Join(state, "state")+`","socket_path":"`+filepath.Join(state, "s")+`"}`)
	registry := filepath.Join(state, "registry.yaml")
	writeFile(t, registry, `connector:
  pr:
    - command: [pg-connector-github, --config, `+backendCfg+`, pr]
      name: pg-connector-pr-github
backends:
  pg-connector-pr-github:
    queries:
      mine: "is:open author:@me"
      team: "is:open team-review-requested:org/team"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("PG_PR_CONFIG", registry)
	t.Setenv("XDG_STATE_HOME", filepath.Join(state, "xdg"))

	st, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)

	// Seed the rows as an earlier, healthy heartbeat left them.
	rows, err := freshness.ParseLedgerShow([]byte(daemonDownLedgerBefore))
	if err != nil {
		t.Fatal(err)
	}
	if err := freshness.Update(st, rows); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListMetaPrefix(freshness.MetaKeyPrefix)
	if err != nil || len(before) != 2 {
		t.Fatalf("seeded rows = %v, %v; want one per query", before, err)
	}

	// The ledger read is the one seam left stubbed (the real read is the
	// operator's own ledger). With the daemon down nothing stamps it, so it
	// still shows the old times: that is what the probe failing means.
	withLedgerOnly(t, daemonDownLedgerBefore)

	stderr, err := runHeartbeat(t)
	if err != nil {
		t.Fatalf("heartbeat must exit 0 with the daemon down: %v", err)
	}
	if !strings.Contains(stderr, "not probed") || !strings.Contains(stderr, "pg-connector-pr-github") {
		t.Errorf("stderr = %q, want a not-probed query named", stderr)
	}
	after, err := st.ListMetaPrefix(freshness.MetaKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	assertRowsUnchanged(t, before, after)

	// Negative control: the same assertion MUST fail when the rows do advance,
	// as they do when a probe reaches the daemon and the ledger is re-stamped.
	t.Run("negative control: a stamped ledger advances the rows", func(t *testing.T) {
		withLedgerOnly(t, daemonUpLedgerAfter)
		if _, err := runHeartbeat(t); err != nil {
			t.Fatal(err)
		}
		advanced, err := st.ListMetaPrefix(freshness.MetaKeyPrefix)
		if err != nil {
			t.Fatal(err)
		}
		probe := &recordingT{}
		assertRowsUnchanged(probe, before, advanced)
		if !probe.failed {
			t.Errorf("rows %v did not differ from %v: the stale-rows assertion cannot detect an advance", advanced, before)
		}
	})
}

// withLedgerOnly stubs only the ledger read, leaving the real probe seam.
func withLedgerOnly(t *testing.T, out string) {
	t.Helper()
	orig := heartbeatRunLedgerShow
	t.Cleanup(func() { heartbeatRunLedgerShow = orig })
	heartbeatRunLedgerShow = func(_ context.Context) ([]byte, error) { return []byte(out), nil }
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// failer is the part of testing.T assertRowsUnchanged needs.
type failer interface {
	Helper()
	Errorf(string, ...any)
}

type recordingT struct{ failed bool }

func (r *recordingT) Helper()               {}
func (r *recordingT) Errorf(string, ...any) { r.failed = true }

func assertRowsUnchanged(t failer, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Errorf("source_fetch rows = %v, want %v", after, before)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s advanced to %q from %q with the daemon down", k, after[k], v)
		}
	}
}
