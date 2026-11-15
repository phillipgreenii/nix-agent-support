//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// realPjiraFixtureToken is the fixture token of the token handoff tests. It is
// never printed: every failure message below names counts and booleans, not
// the value, and the child's output is only ever shown after it is scrubbed.
const realPjiraFixtureToken = "FIXTURE-TOKEN-do-not-use-0001"

// realPjiraBinary resolves the REAL pjira (the package built from
// phillipg-nix-repo-base modules/jira): PJIRA_BINARY if set, else PATH. A
// missing binary is a failure, never a skip, so the opt-in check cannot pass
// without having exercised pjira.
func realPjiraBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("PJIRA_BINARY"); p != "" {
		return p
	}
	p, err := exec.LookPath("pjira")
	if err != nil {
		t.Fatalf("the real pjira is not on PATH and PJIRA_BINARY is unset: %v", err)
	}
	return p
}

// pjiraHarness is the shared fixture: a fake `security` first on PATH that
// appends one line to a counter file per launch, and a loopback stub Jira
// that records the Authorization header of every request.
type pjiraHarness struct {
	t         *testing.T
	pjira     string
	root      string // temp dir holding everything below
	binDir    string // fake security lives here
	counter   string // one line per security launch
	cfgHome   string // XDG_CONFIG_HOME
	tokenReal string // the real token file the link points at
	stub      *httptest.Server
	mu        sync.Mutex
	auths     []string
}

func newPjiraHarness(t *testing.T) *pjiraHarness {
	t.Helper()
	h := &pjiraHarness{t: t, pjira: realPjiraBinary(t)}
	h.root = t.TempDir()
	h.binDir = filepath.Join(h.root, "bin")
	h.counter = filepath.Join(h.root, "security-launches")
	h.cfgHome = filepath.Join(h.root, "xdg")
	h.tokenReal = filepath.Join(h.root, "tmp", "token")
	for _, d := range []string{h.binDir, filepath.Join(h.cfgHome, "pjira"), filepath.Dir(h.tokenReal)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(h.counter, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake security counts the launch and answers with the fixture token,
	// so a source=command run succeeds and the only signal is the counter.
	script := "#!/bin/sh\necho launched >> '" + h.counter + "'\nprintf '%s\\n' '" + realPjiraFixtureToken + "'\n"
	if err := os.WriteFile(filepath.Join(h.binDir, "security"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.tokenReal, []byte(realPjiraFixtureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.stub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.auths = append(h.auths, r.Header.Get("Authorization"))
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accountId":"fixture"}`))
	}))
	t.Cleanup(h.stub.Close)
	return h
}

// writeConfig writes the daemon-only pjira config; secret is the [secret]
// table body.
func (h *pjiraHarness) writeConfig(secret string) {
	h.t.Helper()
	cfg := "base_url = \"" + h.stub.URL + "\"\nemail = \"fixture@example.invalid\"\n[secret]\n" + secret
	if err := os.WriteFile(filepath.Join(h.cfgHome, "pjira", "config.toml"), []byte(cfg), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// run runs pjira with only PATH (fake security first), HOME and
// XDG_CONFIG_HOME. The returned output is scrubbed of the fixture token.
func (h *pjiraHarness) run(args ...string) (out string, exit int) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.pjira, args...)
	cmd.Env = []string{
		"PATH=" + h.binDir + ":/usr/bin:/bin",
		"HOME=" + h.root,
		"XDG_CONFIG_HOME=" + h.cfgHome,
	}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	exit = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			h.t.Fatalf("run pjira: %v", err)
		}
	}
	return strings.ReplaceAll(buf.String(), realPjiraFixtureToken, "<token>"), exit
}

func (h *pjiraHarness) launches() int {
	h.t.Helper()
	b, err := os.ReadFile(h.counter)
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.Count(string(b), "launched")
}

// stubSawToken reports whether the stub saw the fixture token in a Basic
// Authorization header.
func (h *pjiraHarness) stubSawToken() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range h.auths {
		raw, ok := strings.CutPrefix(a, "Basic ")
		if !ok {
			continue
		}
		dec, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			continue
		}
		if _, tok, ok := strings.Cut(string(dec), ":"); ok && tok == realPjiraFixtureToken {
			return true
		}
	}
	return false
}

// TestRealPjiraTokenFileThroughSymlinkLaunchesNoSecurity proves the central
// claim of the Jira token handoff against the REAL pjira: configured with
// source=file whose path goes through a symlink, and located through
// XDG_CONFIG_HOME, pjira reads the token from the file and never launches
// /usr/bin/security. The servetest shim is a fake pjira, so a zero counter
// against it would pass vacuously; this is the only test of the claim.
func TestRealPjiraTokenFileThroughSymlinkLaunchesNoSecurity(t *testing.T) {
	h := newPjiraHarness(t)
	link := filepath.Join(h.root, "state", "token-link")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(h.tokenReal, link); err != nil {
		t.Fatal(err)
	}

	t.Run("symlinked token file", func(t *testing.T) {
		h.writeConfig("source = \"file\"\npath = \"" + link + "\"\n")
		out, exit := h.run("auth-status")
		if exit != 0 {
			t.Fatalf("pjira auth-status exit = %d, want 0; output: %s", exit, out)
		}
		if n := h.launches(); n != 0 {
			t.Errorf("fake security launched %d times, want 0", n)
		}
		if !h.stubSawToken() {
			t.Errorf("the stub Jira did not see the fixture token in an Authorization header")
		}
	})

	t.Run("dangling link", func(t *testing.T) {
		dangling := filepath.Join(h.root, "state", "dangling-link")
		if err := os.Symlink(filepath.Join(h.root, "tmp", "gone"), dangling); err != nil {
			t.Fatal(err)
		}
		h.writeConfig("source = \"file\"\npath = \"" + dangling + "\"\n")
		out, exit := h.run("issue", "EXA-1")
		if exit == 0 {
			t.Errorf("pjira exit = 0 through a dangling link, want a failure; output: %s", out)
		}
		if !strings.Contains(out, "pjira: read token file") {
			t.Errorf("pjira output lacks %q; output: %s", "pjira: read token file", out)
		}
		if n := h.launches(); n != 0 {
			t.Errorf("fake security launched %d times after the dangling link, want 0", n)
		}
	})
}

// TestRealPjiraSourceCommandLaunchesSecurity is the negative control of the
// test above: with the OLD config (source=command running security) the very
// same harness records a fake-security launch. If it ever recorded none, the
// zero-launch assertion above could not fail and would prove nothing.
func TestRealPjiraSourceCommandLaunchesSecurity(t *testing.T) {
	h := newPjiraHarness(t)
	h.writeConfig("source = \"command\"\ncommand = [\"security\", \"find-generic-password\", \"-s\", \"zr-jira\", \"-w\"]\n")
	out, exit := h.run("auth-status")
	if exit != 0 {
		t.Fatalf("pjira auth-status exit = %d, want 0; output: %s", exit, out)
	}
	if n := h.launches(); n < 1 {
		t.Errorf("fake security launched %d times under source=command, want at least 1: the harness cannot see a launch", n)
	}
}
