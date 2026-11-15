package serve_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenguard"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/keychain"
)

// The token handoff is tested in-process: the real token file store on a temp
// directory the rig owns (0700), a scripted fake keychain (never the real one)
// and a fake pjira script that reads its token through the link.

const tokenKey = `,"token_link":"@ROOT@/state/jira-token","keychain_account":"me@example.invalid"`

const tokenLaunchesPjira = `#!/bin/sh
echo launch >> "@ROOT@/launches"
cat "@ROOT@/state/jira-token" >> "@ROOT@/seen" || { echo "pjira: read token file: gone" >&2; exit 1; }
printf '\n' >> "@ROOT@/seen"
case "$1" in
search)
  printf '%s' '{"items":[{"key":"ALPHA-1","summary":"title of ALPHA-1","status":"Some Status","status_category":"indeterminate","issuetype":"Task","project":"ALPHA","updated":"2026-10-05T10:11:12.000+0000","comments":[]}],"truncated":false}'
  ;;
*)
  echo "fake pjira: unexpected args $*" >&2
  exit 1
  ;;
esac
`

// tokenRig is a rig with the token handoff opted in and a 0700 temp directory.
func tokenRig(t *testing.T, before func(r *rig)) (*rig, string) {
	t.Helper()
	var tmp string
	r := newRigWith(t, tokenLaunchesPjira, "", tokenKey, func(r *rig) {
		tmp = filepath.Join(r.root, "tmp")
		if err := os.Mkdir(tmp, 0o700); err != nil {
			t.Fatal(err)
		}
		if before != nil {
			before(r)
		}
	})
	return r, tmp
}

func tokenOpts(kc keychain.Keychain, tmp string, timer tokenguard.Timer, clock governor.Clock) func(*serve.Options) {
	return func(o *serve.Options) {
		o.Keychain = kc
		o.TempDir = func() string { return tmp }
		o.TokenTimer = timer
		o.TokenClock = clock
	}
}

func (r *rig) link() string { return filepath.Join(r.state, "jira-token") }

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func gone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s must be gone, Lstat err = %v", path, err)
	}
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func TestTokenHandoffStartReadsOnceAndPjiraReadsThroughTheLink(t *testing.T) {
	r, tmp := tokenRig(t, nil)
	kc := keychain.NewFake(keychain.Result{Token: "TOK-ONE"})
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, tokenguard.NewManualTimer(), nil))
	d.waitReady()

	if n := kc.Calls(); n != 1 {
		t.Fatalf("keychain reads at start = %d, want exactly 1", n)
	}
	if svc, acc := kc.LastRequest(); svc != "zr-jira" || acc != "me@example.invalid" {
		t.Errorf("keychain item = %q %q", svc, acc)
	}
	target, err := os.Readlink(r.link())
	if err != nil {
		t.Fatalf("the link must be a symlink: %v", err)
	}
	if !strings.HasPrefix(target, tmp+string(filepath.Separator)) {
		t.Errorf("the link points at %q, want a file inside the temp dir %q", target, tmp)
	}
	fi, err := os.Stat(target)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("token file stat = %v, %v; want mode 0600", fi, err)
	}

	resp := r.mustCall(serve.Instance, "show", map[string]any{"id": "ALPHA-1"})
	if resp.Error != nil {
		t.Fatalf("show: %+v", resp.Error)
	}
	if seen := lines(t, filepath.Join(r.root, "seen")); len(seen) == 0 || seen[0] != "TOK-ONE" {
		t.Errorf("pjira read %v through the link, want TOK-ONE", seen)
	}
	if n := kc.Calls(); n != 1 {
		t.Errorf("keychain reads after pjira calls = %d, want still 1", n)
	}

	if code := d.stop(); code != serve.ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, d.stderr.String())
	}
	// A clean shutdown removes the file and the link, and the closing flag keeps
	// the rest of the drain from reading the keychain.
	gone(t, r.link())
	gone(t, target)
	if n := kc.Calls(); n != 1 {
		t.Errorf("keychain reads after the drain = %d, want 1", n)
	}
	if hasChange(r.daemonLog(), "token_file_remove_failed") {
		t.Error("token_file_remove_failed was logged on a clean shutdown")
	}
}

func TestTokenFileHookRunsBeforeBackgroundWithinTheHookBudget(t *testing.T) {
	r, tmp := tokenRig(t, nil)
	kc := keychain.NewFake(keychain.Result{Token: "TOK-ONE"})
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, tokenguard.NewManualTimer(), nil))
	d.waitReady()
	if code := d.stop(); code != serve.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	phases := map[string]int{}
	var order []string
	for _, l := range r.daemonLog() {
		if l["change"] == "shutdown_phase" {
			p, _ := l["phase"].(string)
			order = append(order, p)
			phases[p] = len(order) - 1
			if p == "hook:token-file" {
				if ms, _ := l["duration_ms"].(float64); ms >= 10_000 {
					t.Errorf("hook:token-file took %v ms, the budget for all hooks is 10 s", ms)
				}
			}
		}
	}
	tf, okT := phases["hook:token-file"]
	bg, okB := phases["hook:background"]
	if !okT || !okB || tf >= bg {
		t.Errorf("want hook:token-file before hook:background, got order %v", order)
	}
	if w, ok := phases["hook:writes"]; !ok || w > tf {
		t.Errorf("want hook:writes before hook:token-file, got order %v", order)
	}
}

func TestTokenHandoffLockedKeychainStartsDegradedWithoutLaunchingPjira(t *testing.T) {
	r, tmp := tokenRig(t, nil)
	kc := keychain.NewFake(keychain.Fail(keychain.KindLocked))
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, tokenguard.NewManualTimer(), nil))
	d.waitReady() // a locked keychain never stops the daemon

	resp := r.mustCall(serve.Instance, "show", map[string]any{"id": "ALPHA-1"})
	if resp.Error == nil {
		t.Fatal("a degraded daemon must refuse the origin call")
	}
	if n := len(lines(t, filepath.Join(r.root, "launches"))); n != 0 {
		t.Errorf("pjira launches while degraded token_unavailable = %d, want 0", n)
	}
	gone(t, r.link())
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d; stderr: %s", code, d.stderr.String())
	}
}

func TestTokenHandoffMissingKeychainItemStartsDegraded(t *testing.T) {
	r, tmp := tokenRig(t, nil)
	kc := keychain.NewFake(keychain.Fail(keychain.KindItemMissing))
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, tokenguard.NewManualTimer(), nil))
	d.waitReady()
	if n := len(lines(t, filepath.Join(r.root, "launches"))); n != 0 {
		t.Errorf("pjira launches = %d, want 0", n)
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d", code)
	}
}

func TestTokenHandoffRefusedTempDirStartsDegradedAndRetries(t *testing.T) {
	r, tmp := tokenRig(t, nil)
	// Group and other bits make the directory unsafe: fail closed.
	if err := os.Chmod(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	kc := keychain.NewFake(keychain.Result{Token: "TOK-LATE"})
	timer := tokenguard.NewManualTimer()
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, timer, nil))
	d.waitReady() // a refused temp dir never stops the daemon

	if n := kc.Calls(); n != 0 {
		t.Errorf("keychain reads with a refused temp dir = %d, want 0", n)
	}
	if resp := r.mustCall(serve.Instance, "show", map[string]any{"id": "ALPHA-1"}); resp.Error == nil {
		t.Error("a daemon degraded token_file must refuse the origin call")
	}
	if n := len(lines(t, filepath.Join(r.root, "launches"))); n != 0 {
		t.Errorf("pjira launches = %d, want 0", n)
	}
	gone(t, r.link())
	if !hasChange(r.daemonLog(), "auth_degraded") {
		t.Error("the degrade must be logged as auth_degraded")
	}

	// The operator fixes the directory; the first retry (one minute) recovers.
	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	timer.AwaitPending(1)
	timer.Advance(time.Minute)
	until(t, 10*time.Second, "the retry to write the token file", func() bool {
		b, err := os.ReadFile(r.link())
		return err == nil && string(b) == "TOK-LATE"
	})
	if n := kc.Calls(); n != 1 {
		t.Errorf("keychain reads after the retry = %d, want 1", n)
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d", code)
	}
}

func TestTokenHandoffUnsetTMPDIRStartsDegradedTokenFile(t *testing.T) {
	r, _ := tokenRig(t, nil)
	kc := keychain.NewFake(keychain.Result{Token: "X"})
	d := r.startWith(serve.Hooks{}, func(o *serve.Options) {
		o.Keychain = kc
		o.TempDir = func() string { return "" }
		o.TokenTimer = tokenguard.NewManualTimer()
	})
	d.waitReady()
	if n := kc.Calls(); n != 0 {
		t.Errorf("keychain reads with TMPDIR unset = %d, want 0", n)
	}
	gone(t, r.link())
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d", code)
	}
}

// writeNewerStore leaves a store a newer binary wrote: startup after the token
// handoff refuses it, which is an error return from d.start.
func writeNewerStore(t *testing.T) func(r *rig) {
	return func(r *rig) {
		db, err := sql.Open("sqlite", "file:"+r.storePath())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(context.Background(), `CREATE TABLE from_the_future (x INTEGER); PRAGMA user_version = 99`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTokenHandoffFileAndLinkAreRemovedAfterAnErrorReturnFromStart(t *testing.T) {
	r, tmp := tokenRig(t, writeNewerStore(t))
	kc := keychain.NewFake(keychain.Result{Token: "TOK-ONE"})
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, tokenguard.NewManualTimer(), nil))
	if code := d.wait(30 * time.Second); code != serve.ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, serve.ExitRefused, d.stderr.String())
	}
	if n := kc.Calls(); n != 1 {
		t.Errorf("keychain reads = %d, want 1 (the handoff starts before the store opens)", n)
	}
	gone(t, r.link())
	gone(t, filepath.Join(tmp, "pg-connector-issue-jira", "jira-token"))
}

func TestTokenHandoffSecondInstanceIsRefusedWithoutTouchingTheFile(t *testing.T) {
	r, tmp := tokenRig(t, nil)
	kc1 := keychain.NewFake(keychain.Result{Token: "TOK-ONE"})
	d1 := r.startWith(serve.Hooks{}, tokenOpts(kc1, tmp, tokenguard.NewManualTimer(), nil))
	d1.waitReady()

	kc2 := keychain.NewFake(keychain.Result{Token: "TOK-TWO"})
	d2 := r.startWith(serve.Hooks{}, tokenOpts(kc2, tmp, tokenguard.NewManualTimer(), nil))
	if code := d2.wait(30 * time.Second); code != serve.ExitRefused {
		t.Fatalf("second instance exit = %d, want %d", code, serve.ExitRefused)
	}
	if n := kc2.Calls(); n != 0 {
		t.Errorf("the refused instance read the keychain %d times, want 0", n)
	}
	if got := readFile(t, r.link()); got != "TOK-ONE" {
		t.Errorf("the first instance's token file holds %q after the refusal, want TOK-ONE", got)
	}
	if code := d1.stop(); code != serve.ExitOK {
		t.Errorf("first instance exit = %d", code)
	}
	gone(t, r.link())
}

func TestTokenHandoffLeftoversOfAKilledDaemonAreRemovedBeforeTheKeychainRead(t *testing.T) {
	var staleFile string
	r, tmp := tokenRig(t, func(r *rig) {
		sub := filepath.Join(r.root, "tmp", "pg-connector-issue-jira")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		staleFile = filepath.Join(sub, "jira-token")
		if err := os.WriteFile(staleFile, []byte("STALE"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(staleFile, r.link()); err != nil {
			t.Fatal(err)
		}
	})
	var mu sync.Mutex
	var staleAtRead []bool
	kc := probeKeychain{fn: func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		_, e1 := os.Lstat(staleFile)
		_, e2 := os.Lstat(r.link())
		staleAtRead = append(staleAtRead, e1 == nil || e2 == nil)
		return "TOK-NEW", nil
	}}
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, tokenguard.NewManualTimer(), nil))
	d.waitReady()
	mu.Lock()
	defer mu.Unlock()
	if len(staleAtRead) != 1 || staleAtRead[0] {
		t.Errorf("leftovers present at the keychain read = %v, want one read with none", staleAtRead)
	}
	if got := readFile(t, r.link()); got != "TOK-NEW" {
		t.Errorf("link reads %q, want TOK-NEW", got)
	}
	_ = d.stop()
}

// probeKeychain runs fn for every read.
type probeKeychain struct{ fn func() (string, error) }

func (p probeKeychain) Password(context.Context, string, string) (string, error) { return p.fn() }

func TestTokenHandoffOffByDefaultReadsNoKeychainAndWritesNoFile(t *testing.T) {
	r := newRig(t, fakePjiraOK, "", nil)
	tmp := filepath.Join(r.root, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	kc := keychain.NewFake(keychain.Result{Token: "NEVER"})
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, tokenguard.NewManualTimer(), nil))
	d.waitReady()
	if resp := r.mustCall(serve.Instance, "show", map[string]any{"id": "ALPHA-1"}); resp.Error != nil {
		t.Fatalf("show: %+v", resp.Error)
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d", code)
	}
	if n := kc.Calls(); n != 0 {
		t.Errorf("keychain reads with token_link unset = %d, want 0", n)
	}
	gone(t, r.link())
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("the temp dir holds %d entries, want none", len(entries))
	}
}

func TestTokenKeepaliveTouchesTheFileEverySixHoursAndRewritesAMissingOne(t *testing.T) {
	r, tmp := tokenRig(t, nil)
	kc := keychain.NewFake(keychain.Result{Token: "TOK-ONE"}, keychain.Result{Token: "TOK-TWO"})
	timer := tokenguard.NewManualTimer()
	clock := governor.NewManualClock(time.Now())
	d := r.startWith(serve.Hooks{}, tokenOpts(kc, tmp, timer, clock))
	d.waitReady()
	file := filepath.Join(tmp, "pg-connector-issue-jira", "jira-token")

	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	timer.AwaitPending(1)
	timer.Advance(serve.KeepaliveInterval)
	until(t, 10*time.Second, "the keepalive to touch the file", func() bool {
		fi, err := os.Stat(file)
		return err == nil && time.Since(fi.ModTime()) < time.Hour
	})
	if n := kc.Calls(); n != 1 {
		t.Errorf("keychain reads after a touch = %d, want still 1", n)
	}

	// The OS cleaner removed the file: one keychain read and a rewrite.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	clock.Advance(keychainFloorForTest)
	timer.AwaitPending(1)
	timer.Advance(serve.KeepaliveInterval)
	until(t, 10*time.Second, "the keepalive to rewrite the missing file", func() bool {
		b, err := os.ReadFile(r.link())
		return err == nil && string(b) == "TOK-TWO"
	})
	if n := kc.Calls(); n != 2 {
		t.Errorf("keychain reads after the rewrite = %d, want 2", n)
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d", code)
	}
}

// keychainFloorForTest is longer than the guard's 60 s floor between reads.
const keychainFloorForTest = 2 * time.Minute
