// Package corpustest is the test fixture of the corpus Monitor and its callers:
// a real recorder (an in-process daemon over a throwaway Claude directory) that
// tests fill by writing transcript, subagent, status and registry files, then
// read through the real Go client. It lives outside the _test files so the
// corpus and poller tests share it.
package corpustest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/recorderfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// StartMs is a fixed start instant for registrations (no wall clock).
const StartMs = int64(1793375000000)

// Env is a started recorder, its Claude directory and a Facts reading it.
type Env struct {
	*clienttest.Env
	Facts *recorderfacts.Facts
	t     *testing.T
}

// Start starts the recorder and a Facts connected to its socket.
func Start(t *testing.T) *Env {
	t.Helper()
	e := clienttest.Start(t)
	return &Env{Env: e, t: t, Facts: recorderfacts.New(client.Options{Socket: e.Socket, Name: "corpustest", ConnectTimeout: time.Second})}
}

// Discovery discovers the sessions registered with Register (no process
// environment is read).
func (e *Env) Discovery() session.Discovery {
	return &session.Discoverer{
		Registry: e.Facts,
		ReadEnv:  func(string, int) (map[string]string, error) { return map[string]string{}, nil },
	}
}

// Slug is the project directory name Claude uses for cwd.
func Slug(cwd string) string {
	return filepath.Base(filepath.Dir((&session.Session{Cwd: cwd}).TranscriptPath("")))
}

// Register writes a session registry file for pid and tells the recorder the
// process is alive (alive) or ended (not alive).
func (e *Env) Register(pid int, sid, cwd, name string, alive bool) {
	e.t.Helper()
	nameField := ""
	if name != "" {
		nameField = fmt.Sprintf(`,"name":%q`, name)
	}
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"startedAt":%d,"procStart":%q,`+
		`"version":"2.1.0","kind":"interactive","entrypoint":"cli","status":"busy","statusUpdatedAt":%d,"updatedAt":%d%s}`,
		pid, sid, cwd, StartMs, time.UnixMilli(StartMs).UTC().Format("Mon Jan _2 15:04:05 2006"), StartMs+5000, StartMs+5000, nameField)
	e.Procs.Add(pid, time.UnixMilli(StartMs), nil)
	e.Write(filepath.Join("sessions", fmt.Sprintf("%d.json", pid)), body)
	e.Settle()
	if !alive {
		e.Procs.Exit(pid, time.UnixMilli(StartMs+9000))
		deadline := time.Now().Add(10 * time.Second)
		for {
			regs, err := e.Facts.Registrations(context.Background())
			if err == nil {
				ended := false
				for _, s := range regs {
					if s.PID == pid && !s.PidAlive {
						ended = true
					}
				}
				if ended {
					return
				}
			}
			if time.Now().After(deadline) {
				e.t.Fatalf("recorder never reported pid %d ended", pid)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// TranscriptRel is the transcript's path under the Claude directory.
func TranscriptRel(cwd, sid string) string {
	return filepath.Join("projects", Slug(cwd), sid+".jsonl")
}

// SubagentRel is a subagent file's path under the Claude directory.
func SubagentRel(cwd, sid, agent string) string {
	return filepath.Join("projects", Slug(cwd), sid, "subagents", "agent-"+agent+".jsonl")
}

// StatusRel is a session's status file's path under the Claude directory.
func StatusRel(cwd, sid string) string {
	return filepath.Join("projects", Slug(cwd), sid+".status.jsonl")
}

// Put replaces the file at rel with lines and lets the recorder see it.
func (e *Env) Put(rel string, lines ...string) {
	e.t.Helper()
	e.Write(rel, strings.Join(lines, "\n")+"\n")
	e.Settle()
}

// Add appends lines to the file at rel and lets the recorder see them.
func (e *Env) Add(rel string, lines ...string) {
	e.t.Helper()
	e.Append(rel, strings.Join(lines, "\n")+"\n")
	e.Settle()
}

// Unreadable makes the Claude directory's projects tree unreadable for the rest
// of the test (restored at cleanup), so anything that still opens a transcript
// file fails.
func (e *Env) Unreadable() {
	e.t.Helper()
	dir := e.Path("projects")
	if err := os.Chmod(dir, 0); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}
