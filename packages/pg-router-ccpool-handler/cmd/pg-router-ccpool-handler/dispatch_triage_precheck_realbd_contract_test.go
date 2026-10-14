//go:build contract

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router/conformance"
)

// Real-bd regression guard for the `triage` dispatch precheck (INV-CCH-22,
// bead pg2-p9lcg), behind the `contract` build tag (global rule V-3).
//
// WHY THIS FILE EXISTS: whether a bead "still matches the triager's discovery
// query" is a property of what the REAL `bd show` returns for a bead the real
// `bd` closed, labelled, deferred or blocked; the fake `bd` scripts of
// dispatch_precheck_test.go only impersonate that shape. These tests drive the
// real runDispatch against a REAL `bd` in a throwaway store (dtest.NewRealBD:
// embedded dolt private to a t.TempDir(), no shared server) and read the bead
// back to prove a decline wrote nothing to it.
//
// "Launch path reached" is told from "declined" the way the fake-bd tests tell
// it: the dispatch PATH holds only `bd`, no `ccpool`, so a dispatch that gets
// past the precheck stops at its first ccpool call and declines with reason
// session-in-flight.
//
// Hermetic environment: the PROCESS environment (which runDispatch's bd runner
// inherits) is rebuilt from an ALLOWLIST (see allowlistProcessEnv), so no GIT_*
// variable a commit hook exports and no BEADS_*/BD_*/WORKSPACE_ROOT binding can
// reach the child bd, the same approach as pg-desk's hermeticChildEnv.
//
// Run it directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run Triage_RealBD ./cmd/pg-router-ccpool-handler/

// processEnvAllowlist is the complete list of parent-environment names the
// dispatch under test may inherit. It MUST NOT name anything that selects a git
// repository or a beads workspace.
var processEnvAllowlist = map[string]bool{
	"TMPDIR": true, "USER": true, "LOGNAME": true, "LANG": true, "TZ": true, "TERM": true,
}

// allowlistProcessEnv replaces the process environment with the allowlisted
// names plus the given PATH and a private HOME, and restores the original at
// cleanup. Nothing else survives.
func allowlistProcessEnv(t *testing.T, path, home string) {
	t.Helper()
	saved := os.Environ()
	os.Clearenv()
	for _, kv := range saved {
		k, v, _ := strings.Cut(kv, "=")
		if processEnvAllowlist[k] || strings.HasPrefix(k, "LC_") {
			_ = os.Setenv(k, v)
		}
	}
	_ = os.Setenv("PATH", path)
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	_ = os.Setenv("BEADS_DOLT_AUTO_START", "0")
	_ = os.Setenv("BD_NON_INTERACTIVE", "1")
	_ = os.Setenv("BD_JSON_ENVELOPE", "1")
	t.Cleanup(func() {
		os.Clearenv()
		for _, kv := range saved {
			k, v, _ := strings.Cut(kv, "=")
			_ = os.Setenv(k, v)
		}
	})
}

// TestTriage_RealBD_AllowlistEnvDropsHookVariables pins the hermetic setup itself: a
// git-hook-shaped parent environment must not survive allowlistProcessEnv.
func TestTriage_RealBD_AllowlistEnvDropsHookVariables(t *testing.T) {
	t.Setenv("GIT_DIR", "/outer/repo/.git")
	t.Setenv("GIT_INDEX_FILE", "/outer/repo/.git/index")
	t.Setenv("BEADS_DIR", "/outer/repo/.beads")
	t.Setenv("WORKSPACE_ROOT", "/outer")
	t.Setenv("BD_ACTOR", "outer")
	t.Setenv("SOMETHING_ELSE", "1")
	allowlistProcessEnv(t, "/bin", t.TempDir())
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if (strings.HasPrefix(k, "GIT_") && k != "GIT_CONFIG_NOSYSTEM") || k == "BEADS_DIR" || k == "WORKSPACE_ROOT" || k == "BD_ACTOR" || k == "SOMETHING_ELSE" {
			t.Errorf("%s leaked into the allowlisted process environment", k)
		}
	}
}

// triageBeadFixture creates one bead per scenario in the real store.
type triageBeadFixture struct {
	ctx context.Context
	r   *beads.CLIRunner
}

func (f triageBeadFixture) bd(t *testing.T, args ...string) string {
	t.Helper()
	out, err := f.r.Run(f.ctx, args...)
	if err != nil {
		t.Fatalf("bd %v: %v\n%s", args, err, out)
	}
	return out
}

func (f triageBeadFixture) bead(t *testing.T, title string, labels ...string) string {
	t.Helper()
	args := []string{"q", title}
	for _, l := range labels {
		args = append(args, "-l", l)
	}
	id := strings.TrimSpace(f.bd(t, args...))
	if id == "" {
		t.Fatalf("bd q %q returned no id", title)
	}
	return id
}

// show is the bead exactly as the real bd reports it, used to prove a decline
// wrote nothing.
func (f triageBeadFixture) show(t *testing.T, id string) string {
	t.Helper()
	return f.bd(t, "show", id, "--json")
}

func TestTriage_RealBD_Dispatch(t *testing.T) {
	ctx, r := dtest.NewRealBD(t, "triage-actor")
	f := triageBeadFixture{ctx: ctx, r: r}

	openEscalated := f.bead(t, "open escalated", "escalated")
	peerHeld := f.bead(t, "open escalated, assigned to a peer", "escalated")
	f.bd(t, "update", peerHeld, "--assignee", "someone-else")
	closed := f.bead(t, "closed escalated", "escalated")
	f.bd(t, "close", closed)
	human := f.bead(t, "escalated for a human", "escalated", "human")
	deEscalated := f.bead(t, "de-escalated")
	deferred := f.bead(t, "deferred escalated", "escalated")
	f.bd(t, "defer", deferred, "--until", "2999-01-01")
	blocked := f.bead(t, "blocked escalated", "escalated")
	blocker := f.bead(t, "an open blocker")
	f.bd(t, "dep", "add", blocked, blocker)

	// The fixture must be what the test says it is: the real discovery query
	// lists exactly the beads the precheck is meant to let through.
	listed := f.bd(t, "ready", "--label", "escalated", "--exclude-label", "human", "--json", "--limit", "0")
	for id, want := range map[string]bool{
		openEscalated: true, peerHeld: true,
		closed: false, human: false, deEscalated: false, deferred: false, blocked: false,
	} {
		if got := strings.Contains(listed, `"`+id+`"`); got != want {
			t.Fatalf("discovery query lists %s = %v, want %v (fixture is not what the test assumes):\n%s", id, got, want, listed)
		}
	}

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The dispatch PATH holds only the tools bd's own wrapper needs: bd itself
	// and bash (its guard scripts start with `env bash`). No ccpool.
	for _, tool := range []string{"bd", "bash"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skip(tool + " not on PATH")
		}
		if err := os.Symlink(p, filepath.Join(binDir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	allowlistProcessEnv(t, binDir, t.TempDir())

	roleDir := t.TempDir()
	rolePath := filepath.Join(roleDir, "triage.json")
	roleJSON := `{"name":"triager","type":"ccpool","ccpool":{"actor":"triage-actor","completion":"close-or-triage","precheck":"triage","beadsDir":` +
		mustJSON(t, r.Dir) + `,"onFailure":"add-human","onDispatchFail":"unclaim","promptBody":"hello"}}`
	if err := os.WriteFile(rolePath, []byte(roleJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	const unreadable = "sx-nosuchbead"
	tests := []struct {
		name       string
		id         string
		wantReason string
	}{
		{"open escalated bead launches as before", openEscalated, busyReasonSessionInFlight},
		{"open escalated bead held by a peer launches as before", peerHeld, busyReasonSessionInFlight},
		{"closed bead is declined", closed, "skipped-bead-not-open"},
		{"human bead is declined", human, "skipped-bead-human"},
		{"de-escalated bead is declined", deEscalated, "skipped-bead-not-escalated"},
		{"deferred bead is declined", deferred, "skipped-bead-deferred"},
		{"blocked bead is declined", blocked, "skipped-bead-blocked"},
		{"unreadable bead fails open", unreadable, busyReasonSessionInFlight},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stateHome := t.TempDir()
			t.Setenv("XDG_STATE_HOME", stateHome)
			before := ""
			if tc.id != unreadable {
				before = f.show(t, tc.id)
			}
			var logs bytes.Buffer
			prevLog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(prevLog)
			restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"`+tc.id+`","type":"task"}}}`)
			defer restoreIn()
			var code int
			got := captureStdout(t, func() { code = runDispatch([]string{"--role-config", rolePath}) })
			var reply struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal([]byte(got), &reply); err != nil {
				t.Fatalf("stdout = %q, not valid JSON: %v", got, err)
			}
			if code != conformance.ExitBusy || reply.Reason != tc.wantReason {
				t.Fatalf("got exit %d reason %q, want ExitBusy with %q", code, reply.Reason, tc.wantReason)
			}
			// The bead read must really have happened (or really failed): a launch
			// case that passed only because bd could not run would prove nothing.
			if refreshFailed := strings.Contains(logs.String(), "could not refresh item metadata"); refreshFailed != (tc.id == unreadable) {
				t.Fatalf("bead refresh failed = %v for %s, want %v:\n%s", refreshFailed, tc.id, tc.id == unreadable, logs.String())
			}
			skipRecords := precheckSkipRecords(t, filepath.Join(stateHome, "pg-router-ccpool-handler", "events.jsonl"))
			if tc.wantReason == busyReasonSessionInFlight {
				if len(skipRecords) != 0 {
					t.Fatalf("a dispatch that launches must write no precheck_skip record, got %v", skipRecords)
				}
				return
			}
			if len(skipRecords) != 1 || skipRecords[0]["reason"] != tc.wantReason || skipRecords[0]["bead"] != tc.id {
				t.Fatalf("want exactly one precheck_skip record for %s with reason %q, got %v", tc.id, tc.wantReason, skipRecords)
			}
			if after := f.show(t, tc.id); after != before {
				t.Fatalf("a precheck decline wrote to the bead:\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// precheckSkipRecords returns every precheck_skip record of the event log (none
// when the log does not exist).
func precheckSkipRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("event log line %q: %v", line, err)
		}
		if rec["kind"] == "precheck_skip" {
			out = append(out, rec)
		}
	}
	return out
}
