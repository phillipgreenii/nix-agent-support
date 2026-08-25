//go:build integration

package beads

// Integration/smoke tests that exercise pr-pool's bd client against a REAL `bd`
// binary backed by an isolated, throwaway embedded-Dolt store (NOT the shared
// dolt sql-server). They are the guard that keeps pr-pool's parsing in sync with
// bd's actual `--json` output shape: the unit tests pin a hand-written envelope
// fixture, while these prove that fixture matches what the installed bd really
// emits. They were written for pg2-ygbt, where bd's `{"data":[...]}` envelope
// silently made every discovery query return zero ready beads.
//
// Isolation: each test gets its own t.TempDir() and runs `bd init` in embedded
// mode there, so nothing touches the real beads/Dolt server. BEADS_DIR /
// WORKSPACE_ROOT are scrubbed by NewCLIRunnerForRepo, and bd resolves its store
// from the runner's Dir.
//
// Skips automatically when `bd` is not on PATH (e.g. the nix build sandbox, where
// bd is only wired onto pr-pool's runtime PATH via wrapProgram) or under
// `go test -short`.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// bdRepo spins up an isolated embedded-Dolt beads store in a temp dir and returns
// a runner rooted there. Skips the test if bd is unavailable or -short is set.
func bdRepo(t *testing.T) (context.Context, *CLIRunner) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping bd integration test in -short mode")
	}
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH; skipping bd integration test")
	}
	// Non-interactive so `bd init`/writes never block on a prompt. Set before
	// constructing the runner so it lands in the scrubbed env snapshot.
	t.Setenv("BD_NON_INTERACTIVE", "1")

	dir := t.TempDir()
	r := NewCLIRunnerForRepo(dir)

	// Embedded Dolt init (default backend, no external server). Generous timeout:
	// the first embedded-engine init does real work.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if out, err := r.Run(ctx, "init", "--prefix", "tst"); err != nil {
		t.Skipf("bd init failed (embedded dolt unavailable in this env): %v\n%s", err, out)
	}
	return ctx, r
}

// quickCreate creates a bead via `bd q` (prints only the id) and returns its id.
func quickCreate(ctx context.Context, t *testing.T, r *CLIRunner, title string) string {
	t.Helper()
	out, err := r.Run(ctx, "q", title)
	if err != nil {
		t.Fatalf("bd q %q: %v\n%s", title, err, out)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		t.Fatalf("bd q %q returned no id; stdout=%q", title, out)
	}
	return id
}

// TestIntegration_ReadyParsesRealBdEnvelope is the core regression for pg2-ygbt:
// the exact worker discovery query, run through pr-pool's own Ready() against a
// real bd, must return the worker-ready bead and exclude the human-labeled one.
// Before the unwrapData fix this returned zero issues because bd's `{"data":[...]}`
// envelope failed pr-pool's bare-array decode.
func TestIntegration_ReadyParsesRealBdEnvelope(t *testing.T) {
	ctx, r := bdRepo(t)

	// A worker-ready bead the pool SHOULD pick up...
	want := quickCreate(ctx, t, r, "do the work")
	if out, err := r.Run(ctx, "update", want, "--add-label", "worker-ready"); err != nil {
		t.Fatalf("label worker-ready: %v\n%s", err, out)
	}
	// ...and a worker-ready bead escalated to a human, which the query must exclude.
	human := quickCreate(ctx, t, r, "needs a human")
	if out, err := r.Run(ctx, "update", human, "--add-label", "worker-ready", "--add-label", "human"); err != nil {
		t.Fatalf("label human: %v\n%s", err, out)
	}

	// The literal worker discovery query (mirror of discover.discoverWorker).
	got, err := Ready(ctx, r, "--label", "worker-ready", "--exclude-label", "human")
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}

	ids := map[string]bool{}
	for _, iss := range got {
		ids[iss.ID] = true
	}
	if !ids[want] {
		t.Errorf("worker-ready bead %s not discovered through real bd; got %d issues: %v", want, len(got), ids)
	}
	if ids[human] {
		t.Errorf("human-labeled bead %s must be excluded by --exclude-label human; got %v", human, ids)
	}
	// Labels must survive the envelope decode (HasLabel feeds worker pre-flight).
	for _, iss := range got {
		if iss.ID == want && !iss.HasLabel("worker-ready") {
			t.Errorf("discovered bead %s lost its worker-ready label through decode; labels=%v", want, iss.Labels)
		}
	}
}

// TestIntegration_ShowObjParsesRealBdEnvelope guards the decodeOne path: ShowObj
// must recover a populated Issue (id + labels) from real `bd show --json`, not the
// empty Issue an envelope-blind decoder produced.
func TestIntegration_ShowObjParsesRealBdEnvelope(t *testing.T) {
	ctx, r := bdRepo(t)

	id := quickCreate(ctx, t, r, "show me")
	if out, err := r.Run(ctx, "update", id, "--add-label", "worker-ready"); err != nil {
		t.Fatalf("label: %v\n%s", err, out)
	}

	iss, err := ShowObj(ctx, r, id)
	if err != nil {
		t.Fatalf("ShowObj: %v", err)
	}
	if iss.ID != id {
		t.Fatalf("ShowObj returned empty/wrong issue from real bd: %+v", iss)
	}
	if iss.Status == "" {
		t.Errorf("ShowObj lost status through real-bd decode: %+v", iss)
	}
	if !iss.HasLabel("worker-ready") {
		t.Errorf("ShowObj lost labels through real-bd decode; labels=%v", iss.Labels)
	}

	got, err := HasLabel(ctx, r, id, "worker-ready")
	if err != nil || !got {
		t.Errorf("HasLabel(worker-ready) = %v, err=%v through real bd; want true", got, err)
	}
}

// TestIntegration_ReadyEmptyStoreIsEmptyNotError: a fresh store with no matching
// beads must yield an empty slice and no error (the no-ready-work case), so the
// pool idles cleanly rather than crashing or treating it as infra failure.
func TestIntegration_ReadyEmptyStoreIsEmptyNotError(t *testing.T) {
	ctx, r := bdRepo(t)

	got, err := Ready(ctx, r, "--label", "worker-ready", "--exclude-label", "human")
	if err != nil {
		t.Fatalf("Ready on empty store errored: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty store should yield no ready beads, got %d: %v", len(got), got)
	}
}
