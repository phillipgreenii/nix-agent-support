//go:build contract

package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TestBackend_RoundTrip_RealBD is the packet's required round-trip test
// "verified against a real ... bd invocation ... not mocks alone" [bead:
// Files]: create an issue, transition it, show it, and assert the new
// state is reflected — against a REAL `bd` binary in a disposable
// workspace directory.
//
// Behind the `contract` build tag on purpose, mirroring
// packages/pg-pr/pkg/beads/contract_test.go's own resolution to the EXACT
// failure this backend hit while developing this file (bead pg2-5ek6b,
// closed): `bd init`'s database defaults to the issue prefix, but the
// *server* it talks to (`bd dolt set database`'s doc: "default: issue
// prefix or beads"; host/port "auto-detected") is this machine's shared,
// always-running per-user dolt server (org.nixos.beads-dolt-server,
// 127.0.0.1:25252) whenever one is already up — a fresh t.TempDir() does
// NOT get a truly private embedded engine. That makes two things true
// that a bare `go test ./...`-run version of this test got wrong in
// practice: (1) concurrent test binaries can trip a spurious "already
// initialized" collision against that shared server (pg2-5ek6b's own
// diagnosis), so this must stay OFF the default unit-test build (and thus
// off the pre-commit run-unit-tests hook and the plain
// checks.pg-connector-go-tests gate) exactly like pg-pr's own contract
// split; (2) a FIXED prefix persists as a same-named database on that
// shared server across separate test runs (t.TempDir()'s cleanup only
// deletes the local directory, not a server-side database), so prefix
// below is time-based per run rather than a literal constant, to avoid
// colliding with a leftover from an earlier manual `-tags contract` run.
// It never touches the real "pg2" project database (a different name), so
// this is a same-server-different-database isolation, not a
// same-directory one — consistent with pg2-5ek6b's own finding that no
// actual production data was ever at risk, only a same-machine name
// collision.
//
// Run explicitly via `go test -tags contract ./cmd/pg-connector-issue-beads/internal/...`
// (mirroring packages/pb/packages/ccpool's own contract suites); it is
// skipped when `bd` is absent from PATH.
func TestBackend_RoundTrip_RealBD(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}

	dir := t.TempDir()
	env := cleanBDEnv()
	prefix := "tp" + fmt.Sprintf("%x", time.Now().UnixNano())[:10]

	initCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	initCmd := exec.CommandContext(initCtx, "bd", "init", "--prefix", prefix,
		"--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	initCmd.Dir = dir
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}

	b := New(&CLIRunner{Dir: dir, Env: env})
	ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelCtx()

	created, err := b.Create(ctx, issue.IssueInput{
		Title:     "round-trip probe",
		Priority:  "P1",
		IssueType: "task",
		Labels:    []string{"probe"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create returned empty ID")
	}
	if created.State != "open" {
		t.Fatalf("Create: State = %q, want open", created.State)
	}

	if err := b.Comment(ctx, created.ID, "commenting from the round-trip test"); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	if err := b.Transition(ctx, created.ID, "in_progress"); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	shown, err := b.Show(ctx, created.ID)
	if err != nil {
		t.Fatalf("Show after Transition: %v", err)
	}
	if shown.State != "in_progress" {
		t.Fatalf("Show after Transition: State = %q, want in_progress (the new state was not reflected)", shown.State)
	}
	if shown.ID != created.ID || shown.Title != created.Title {
		t.Fatalf("Show identity mismatch: got %+v, want id/title matching %+v", shown, created)
	}

	// A genuinely unknown id must round-trip as a well-formed ErrNotFound,
	// not a generic failure — exercised here against the real binary rather
	// than a fake, to prove decodeBDEnvelope's classification actually
	// matches real bd output (backend_test.go's fake-runner tests already
	// cover the message shapes; this confirms they were not fabricated).
	if _, err := b.Show(ctx, prefix+"-doesnotexist"); !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("Show(unknown id) = %v, want wrapping ErrNotFound", err)
	}
}

// cleanBDEnv strips BEADS_DIR/WORKSPACE_ROOT so the disposable workspace
// created above cannot accidentally bind to this machine's real beads
// workspace or shared dolt server — mirroring
// packages/pg-pr/pkg/beads/contract_test.go's buildCleanEnv.
func cleanBDEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.Index(kv, "="); i > 0 {
			k = kv[:i]
		}
		switch k {
		case "BEADS_DIR", "WORKSPACE_ROOT", "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT":
			continue
		}
		out = append(out, kv)
	}
	return out
}
