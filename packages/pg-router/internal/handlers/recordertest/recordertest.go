// Package recordertest starts an in-process recorder (the real daemon over
// throwaway directories, no started process) at the DEFAULT socket path of a
// sandbox's XDG_STATE_HOME, so a real ccpool binary driven by a hermetic test
// finds it the way it finds the production recorder. Test support only.
package recordertest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// Recorder is a started in-process recorder.
type Recorder struct {
	t   *testing.T
	Env *clienttest.Env
}

// Start starts the recorder and links stateHome/pg-claude-code-recorder/
// recorder.sock to its socket (a unix socket connects through a symlink).
func Start(t *testing.T, stateHome string) *Recorder {
	t.Helper()
	e := clienttest.Start(t)
	dir := filepath.Join(stateHome, "pg-claude-code-recorder")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.Socket, filepath.Join(dir, "recorder.sock")); err != nil {
		t.Fatal(err)
	}
	return &Recorder{t: t, Env: e}
}

// Know makes the recorder know a transcript a fake claude wrote: the file is
// copied under the recorder's Claude directory, and a registry file names the
// transcript's base name (without .jsonl) as a session held by a live process,
// so the Conversation exists and its originals are present.
func (r *Recorder) Know(transcriptPath string) {
	r.t.Helper()
	body, err := os.ReadFile(transcriptPath)
	if err != nil {
		r.t.Fatal(err)
	}
	base := filepath.Base(transcriptPath)
	id := base[:len(base)-len(filepath.Ext(base))]
	r.Env.Write(filepath.Join("projects", r.Env.Project, base), string(body))
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	pid := 40000 + int(start.UnixNano()%1000)
	r.Env.Procs.Add(pid, start, nil)
	r.Env.Write(fmt.Sprintf("sessions/%d.json", pid), fmt.Sprintf(
		`{"pid":%d,"sessionId":%q,"cwd":"/example/work","startedAt":%d,"procStart":%q,"kind":"interactive","status":"idle","updatedAt":%d}`,
		pid, id, start.UnixMilli(), start.UTC().Format("Mon Jan _2 15:04:05 2006"), start.UnixMilli(),
	))
	r.Env.Settle()
}
