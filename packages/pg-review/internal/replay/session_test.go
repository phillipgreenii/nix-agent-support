package replay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
)

// fakeLogCLI installs the fake ccpool script and returns the path of the log
// of its commands (one "<command> <args>" line each in <log>.args).
func fakeLogCLI(t *testing.T, scenario string) (ccpool.CLI, string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ccpool", "ccpool"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ccpool")
	if err := os.WriteFile(bin, b, 0o755); err != nil {
		t.Fatal(err)
	}
	sc := filepath.Join(dir, "scenario")
	if err := os.WriteFile(sc, []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log")
	t.Setenv("FAKE_CCPOOL_SCENARIO", sc)
	t.Setenv("FAKE_CCPOOL_LOG", log)
	return ccpool.CLI{Binary: bin, RecheckDelay: -1}, log
}

func commands(t *testing.T, log, name string) []string {
	t.Helper()
	b, _ := os.ReadFile(log + ".args")
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, name+" ") {
			out = append(out, strings.Fields(l)[1])
		}
	}
	return out
}

func assertAllClosed(t *testing.T, log string) {
	t.Helper()
	opened, closed := commands(t, log, "new"), commands(t, log, "close")
	if len(opened) == 0 {
		t.Fatal("no session was opened")
	}
	for _, id := range opened {
		found := false
		for _, c := range closed {
			found = found || c == id
		}
		if !found {
			t.Errorf("session %s opened but never closed (closed: %v)", id, closed)
		}
	}
}

func TestSessionAnalystClosesEverySessionOpened(t *testing.T) {
	cli, log := fakeLogCLI(t, "")
	a := &SessionAnalyst{Client: cli, Cwd: t.TempDir()}
	// A first attempt, a retry that opens a second session, then repair in it.
	for i := 0; i < 2; i++ {
		if _, err := a.Analyse(context.Background(), "p", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Repair(context.Background(), "fix"); err != nil {
		t.Fatal(err)
	}
	if err := a.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(commands(t, log, "new")); got != 2 {
		t.Fatalf("opened %d sessions, want 2", got)
	}
	assertAllClosed(t, log)
	if len(a.Opened()) != 0 {
		t.Errorf("still recorded open: %v", a.Opened())
	}
}

func TestSessionAnalystClosesSessionWhenStartFails(t *testing.T) {
	cli, log := fakeLogCLI(t, "new_exit=20\n")
	a := &SessionAnalyst{Client: cli, Cwd: t.TempDir()}
	if _, err := a.Analyse(context.Background(), "p", nil); err == nil {
		t.Fatal("want start error")
	}
	if err := a.CloseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertAllClosed(t, log)
}

func TestSessionAnalystClosesWhenReplyFailsAndContextCancelled(t *testing.T) {
	cli, log := fakeLogCLI(t, "reply_exit=23\n")
	a := &SessionAnalyst{Client: cli, Cwd: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := a.Analyse(ctx, "p", nil); err == nil {
		t.Fatal("want reply error")
	}
	cancel()
	if err := a.CloseAll(ctx); err != nil {
		t.Fatal(err)
	}
	assertAllClosed(t, log)
}

type failCloseClient struct {
	ccpool.Client
	closed []string
}

func (f *failCloseClient) Close(_ context.Context, id, _ string) error {
	f.closed = append(f.closed, id)
	if id == "bad" {
		return errors.New("boom")
	}
	return nil
}

func TestCloseAllContinuesPastAFailedCloseAndKeepsIt(t *testing.T) {
	c := &failCloseClient{}
	a := &SessionAnalyst{Client: c, opened: []string{"a", "bad", "b"}}
	err := a.CloseAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("err = %v, want it to name bad", err)
	}
	if strings.Join(c.closed, ",") != "a,bad,b" {
		t.Errorf("closed %v, want all three attempted", c.closed)
	}
	if got := a.Opened(); len(got) != 1 || got[0] != "bad" {
		t.Errorf("Opened = %v, want [bad]", got)
	}
}
