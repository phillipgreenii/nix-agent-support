//go:build integration

package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/x/tmux"
)

// TestRealTmuxWithDaemonConf drives a real tmux server with the daemon's own
// conf line and checks that a pane whose command exits 3 is reported dead with
// status 3, the observation the daemon's dead-pane path relies on.
func TestRealTmuxWithDaemonConf(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		if os.Getenv("PG_ASSISTANTS_REQUIRE_TOOLS") == "1" {
			t.Fatalf("tmux not found and PG_ASSISTANTS_REQUIRE_TOOLS=1: %v", err)
		}
		t.Skip("tmux not on PATH")
	}
	// A short path: the tmux socket path under TMUX_TMPDIR must fit sun_path.
	dir, err := os.MkdirTemp("/tmp", "pga")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	var b [4]byte
	_, _ = rand.Read(b[:])
	c := tmux.Client{
		Bin:    bin,
		Socket: "pg-assistants-test-" + hex.EncodeToString(b[:]),
		Conf:   filepath.Join(dir, "tmux.conf"),
	}
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), bin, "-L", c.Socket, "kill-server").Run()
	})
	if err := c.WriteConf(TmuxConfLine); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := c.NewSession(ctx, "example-exit3", dir, nil, []string{"sh", "-c", "exit 3"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := c.State(ctx, "example-exit3")
		if err != nil {
			t.Fatal(err)
		}
		if st.Exists && st.Dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane not dead within 10s: %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	info, err := c.Dead(ctx, "example-exit3", 5)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != 3 || info.Signal != 0 {
		t.Fatalf("dead info = %+v, want status 3 signal 0", info)
	}
}
