//go:build integration

package serve_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
)

// The crash query against a real child process and the real process table: a
// registry file names the child, the child is SIGKILLed with the file left
// behind (crashed), or the file is deleted first (closed normally, not listed).
func TestCrashQueryAgainstARealChildProcess(t *testing.T) {
	e := clienttest.Start(t, clienttest.Options{RealProcs: true})
	real := proc.NewReal()
	spawn := func(session string) (*exec.Cmd, string) {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		st, err := real.StartTime(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		rel := fmt.Sprintf("sessions/%d.json", cmd.Process.Pid)
		e.Write(rel, fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/example/work","startedAt":%d,"procStart":%q,"kind":"interactive","status":"idle","updatedAt":%d}`,
			cmd.Process.Pid, session, st.UnixMilli(), proc.FormatStart(st), time.Now().UnixMilli()))
		return cmd, rel
	}
	crashed, _ := spawn("example-real-crashed")
	clean, cleanRel := spawn("example-real-clean")
	e.Settle() // discovery: both are alive and watched
	time.Sleep(200 * time.Millisecond)
	_ = os.Remove(e.Path(cleanRel))
	if err := clean.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := crashed.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = crashed.Wait()
	_ = clean.Wait()
	c := e.Dial(context.Background())
	deadline := time.Now().Add(15 * time.Second)
	for {
		e.Settle()
		rows, err := c.Crashed(context.Background(), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			r := rows[0]
			if r.SessionID != "example-real-crashed" || r.Cwd != "/example/work" || r.EndTime == nil || !strings.Contains(r.Resume, "claude --resume example-real-crashed") {
				t.Fatalf("crashed row %+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("crash query rows %+v", rows)
		}
		time.Sleep(100 * time.Millisecond)
	}
	closed, err := c.Conversation(context.Background(), "example-real-clean")
	if err != nil || closed.RegistryPresentAtExit || !closed.ExitObserved || !closed.Ended {
		t.Fatalf("the clean exit: %+v %v", closed, err)
	}
}
