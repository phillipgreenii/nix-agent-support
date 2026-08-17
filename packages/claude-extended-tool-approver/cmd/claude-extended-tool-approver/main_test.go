package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

// testScratchDir is this package's single scratch root. Everything the suite
// writes — the shared XDG_DATA_HOME ask log, and (under the `integration` tag)
// the compiled binary under test — lives beneath it, so the one RemoveAll in
// TestMain is the whole cleanup story.
var testScratchDir string

// tmpfsScratchRoot is the well-known in-memory filesystem preferred for the
// suite's scratch state. See preferTmpfsScratch.
const tmpfsScratchRoot = "/dev/shm"

// TestMain does the CHEAP, package-wide setup only, and is deliberately
// UNTAGGED so it governs both the default unit pass and the `integration` pass.
// It must stay cheap: it runs even when the only tests selected are the handful
// of in-process ones, so anything expensive here (notably compiling the binary,
// which now happens lazily in testBinary) would put I/O back on the build path
// that main_integration_test.go's tag exists to clear.
func TestMain(m *testing.M) {
	preferTmpfsScratch()

	// Make every store THIS PROCESS opens non-durable. Creating one costs 11
	// durable flushes, and fsync latency is a host-filesystem property spanning
	// orders of magnitude — measured at 1.1-3.6s per fsync on the loaded QEMU VM
	// that builds monorepod (tc-fqu7). Durability is meaningless for a database
	// deleted when the test exits, so tests opt out of it. See synchronousPragma
	// in internal/asklog/store.go for the full write-up.
	//
	// This reaches ONLY this process, and that is deliberate: any binary exec'd
	// by the integration tests is the SHIPPED one and keeps the SHIPPED
	// durability, because no env var or flag may change how the real binary
	// behaves (16e1fd4d, pg2-iay90).
	asklog.SetSynchronousForTests("OFF")

	scratch, err := os.MkdirTemp("", "claude-extended-tool-approver-tests-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testScratchDir = scratch

	// Isolate the ask-log for the WHOLE package. The hook-mode tests under the
	// `integration` tag run the real binary, which opens asklog.DefaultDBPath()
	// and INSERTS a row per invocation. Without this, every `go test` run wrote
	// synthetic rows into the developer's real
	// ~/.local/share/claude-extended-tool-approver/asks.db — permanently
	// polluting the corpus that `evaluate` treats as ground truth, and running
	// schema migrations against it as a side effect of testing.
	//
	// Setting it here (rather than in each test) makes isolation the default, so
	// a newly added hook-mode test cannot reintroduce the leak. Individual tests
	// that need their own store still override it with t.Setenv, which wins.
	if err := os.Setenv("XDG_DATA_HOME", filepath.Join(testScratchDir, "xdg-data")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(testScratchDir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(testScratchDir)
	os.Exit(code)
}

// preferTmpfsScratch points TMPDIR at an in-memory filesystem when one is
// available, so the suite's durable writes cost RAM rather than disk.
//
// This is the belt to SetSynchronousForTests' braces, and it covers what that
// seam cannot: the ask log opened by an exec'd SHIPPED binary keeps shipped
// durability by design, so its flushes are real. Redirecting the scratch tree to
// tmpfs makes those flushes cheap without changing a single shipped semantic —
// measured in internal/asklog/store.go at ~0.8us per fsync on tmpfs versus ~50ms
// on a healthy ext4 and 1.1-3.6s on a degraded virtual disk, a spread of roughly
// 60,000x.
//
// Both halves of the suite inherit it: t.TempDir() and os.MkdirTemp("") each
// resolve their base through TMPDIR, so this one call moves the in-process
// stores AND the child's XDG_DATA_HOME.
//
// It is a PREFERENCE, never a requirement. /dev/shm is a Linux convention and is
// absent on macOS, and nix build sandboxes mount their own (sandbox-dev-shm-size
// defaults to 50% of RAM). When it is missing or unwritable the suite silently
// keeps the platform default and is merely slower, never broken.
func preferTmpfsScratch() {
	fi, err := os.Stat(tmpfsScratchRoot)
	if err != nil || !fi.IsDir() {
		return
	}
	probe, err := os.CreateTemp(tmpfsScratchRoot, "ceta-writable-probe-*")
	if err != nil {
		return
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)

	_ = os.Setenv("TMPDIR", tmpfsScratchRoot)
}
