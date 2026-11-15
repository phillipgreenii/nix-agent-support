package tokenguard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenfile"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/keychain"
)

// secretForms are the strings that must stay inside the token file: the token
// and the digest in the encodings a leak would likely use.
func secretForms(tokens ...string) []string {
	var out []string
	for _, tok := range tokens {
		d := sha256.Sum256([]byte(tok))
		out = append(out, tok, hex.EncodeToString(d[:]), base64.StdEncoding.EncodeToString(d[:]),
			base64.RawURLEncoding.EncodeToString(d[:]), string(d[:]))
	}
	return out
}

// leaks returns the secret forms found in any captured text.
func leaks(captured []string, secrets []string) []string {
	var found []string
	for _, c := range captured {
		for _, s := range secrets {
			if strings.Contains(c, s) {
				found = append(found, s)
			}
		}
	}
	return found
}

// leakyRecorder is the deliberately leaking variant (negative control).
type leakyRecorder struct {
	inner Recorder
	token string
}

func (l leakyRecorder) Record(e Event) {
	e.Result += " token=" + l.token
	l.inner.Record(e)
}

// capture collects everything an operator or a child could see.
type capture struct {
	mu   sync.Mutex
	text []string
}

func (c *capture) add(s ...string) {
	c.mu.Lock()
	c.text = append(c.text, s...)
	c.mu.Unlock()
}

func (c *capture) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.text...)
}

type captureRec struct {
	c     *capture
	extra Recorder
}

func (r captureRec) Record(e Event) {
	r.c.add(fmt.Sprintf("%+v", e))
	if r.extra != nil {
		r.extra.Record(e)
	}
}

// leakPjira is a pjira stand-in that reads the token through the stable link
// like the real one and records its argv.
type leakPjira struct {
	accept *string
	link   string
	cap    *capture
}

func (p *leakPjira) Binary() string { return "/abs/pjira" }

func (p *leakPjira) Run(_ context.Context, args ...string) (string, error) {
	p.cap.add(strings.Join(args, " "))
	b, err := os.ReadFile(p.link)
	if err != nil {
		return "", fmt.Errorf("pjira: read token file: %w", err)
	}
	if string(b) != *p.accept {
		return "", fmt.Errorf("pjira issue PROJ-1: status 401 Unauthorized")
	}
	return "{}", nil
}

// drive runs start, rotate, same-token, read-error and shutdown against the
// real token file store. It returns everything captured, the secret forms, the
// two directories and the token file path.
func drive(t *testing.T, leaky bool) (captured, secrets []string, tmp, state, tokenPath string) {
	t.Helper()
	tmp, state = t.TempDir(), t.TempDir()
	for _, d := range []string{tmp, state} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(state, "jira-token")
	store, err := tokenfile.New(tmp, link)
	if err != nil {
		t.Fatal(err)
	}
	tokenPath = store.Path()
	rot := "FIXTURE-ROTATED-0002"
	secrets = secretForms(fixtureToken, rot)
	cap := &capture{}
	clock := governor.NewManualClock(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
	mkRec := func() Recorder {
		var rec Recorder = captureRec{c: cap}
		if leaky {
			rec = leakyRecorder{inner: rec, token: fixtureToken}
		}
		return rec
	}
	newGuard := func(kc keychain.Keychain) *Guard {
		return New(Config{
			Keychain: kc, Store: store, Service: "svc", Account: "acct",
			Clock: clock, Timer: NewManualTimer(), Recorder: mkRec(),
		})
	}
	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 1000
	gov, err := governor.New(origin.GovernorConfig(cfg), governor.Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	accepted := rot
	inner := &leakPjira{accept: &accepted, link: link, cap: cap}
	runnerFor := func(g *Guard) *origin.Runner {
		run, err := origin.New(origin.Options{
			Inner: inner, Governor: gov, Concurrency: 4,
			DefaultPause: time.Minute, Clock: clock, Refresher: g,
		})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	step := func(run *origin.Runner) {
		if _, err := run.Run(context.Background(), "issue", "--", "PROJ-1"); err != nil {
			cap.add(err.Error())
		}
	}

	g := newGuard(keychain.NewFake(ok(fixtureToken), ok(rot), ok(rot)))
	run := runnerFor(g)
	g.Start(context.Background())
	clock.Advance(2 * time.Minute)
	step(run) // start token rejected: rotate and retry
	accepted = "none"
	clock.Advance(2 * time.Minute)
	step(run) // rotated token rejected: same token, degraded
	step(run) // refused
	g.Close()
	captured = append(captured, cap.all()...)
	filesDuring := tokenPath
	_ = filesDuring
	return captured, secrets, tmp, state, tokenPath
}

// secretFiles lists every non-directory entry under dirs whose content holds a
// secret form (symlinks are not followed).
func secretFiles(t *testing.T, secrets []string, dirs ...string) []string {
	t.Helper()
	var hit []string
	for _, d := range dirs {
		err := filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if len(leaks([]string{string(b)}, secrets)) > 0 {
				hit = append(hit, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return hit
}

func TestTokenAndDigestOnlyInTokenFile(t *testing.T) {
	captured, secrets, tmp, state, tokenPath := drive(t, false)
	if got := leaks(captured, secrets); len(got) > 0 {
		t.Fatalf("token or digest leaked into logs, events, errors or argv: %d hits", len(got))
	}
	if len(captured) < 5 {
		t.Fatalf("captured only %d items; the drive did not exercise the paths", len(captured))
	}
	files := secretFiles(t, secrets, tmp, state)
	if len(files) != 1 || files[0] != tokenPath {
		t.Errorf("secret found in %v, want only %s", files, tokenPath)
	}
	if b, _ := os.ReadFile(tokenPath); string(b) != "FIXTURE-ROTATED-0002" {
		t.Error("token file does not hold the rotated token")
	}
}

func TestLeakDetectorFailsOnALeakingVariant(t *testing.T) {
	captured, secrets, _, _, _ := drive(t, true)
	if len(leaks(captured, secrets)) == 0 {
		t.Fatal("negative control: the leaking recorder went undetected")
	}
}

func TestReadErrorAndShutdownLeakNothing(t *testing.T) {
	tmp, state := t.TempDir(), t.TempDir()
	for _, d := range []string{tmp, state} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := tokenfile.New(tmp, filepath.Join(state, "jira-token"))
	if err != nil {
		t.Fatal(err)
	}
	cap := &capture{}
	g := New(Config{
		Keychain: keychain.NewFake(keychain.Fail(keychain.KindLocked)), Store: store,
		Timer: NewManualTimer(), Recorder: captureRec{c: cap},
	})
	g.Start(context.Background())
	g.Close()
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	cap.add(fmt.Sprintf("%+v", g.State()))
	if got := leaks(cap.all(), secretForms(fixtureToken)); len(got) > 0 {
		t.Errorf("leaked: %v", got)
	}
	if hit := secretFiles(t, secretForms(fixtureToken), tmp, state); len(hit) > 0 {
		t.Errorf("secret left on disk after shutdown: %v", hit)
	}
}
