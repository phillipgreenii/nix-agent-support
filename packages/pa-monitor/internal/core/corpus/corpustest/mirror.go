package corpustest

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Mirror is a recorder fed from fixture directories written the old way: a
// sessions directory of registry files and a Claude home of projects/**. Tests
// keep building their fixtures on disk and call Sync after changing them; the
// recorder sees the same bytes a Claude Code would have written.
type Mirror struct {
	*Env
	sessionsDir string
	claudeHome  string
	alive       func(pid int) bool
	known       map[int]bool
}

// StartMirror starts a recorder over the fixtures and syncs them once. alive
// says which registered pids are running (nil: all).
func StartMirror(t *testing.T, sessionsDir, claudeHome string, alive func(pid int) bool) *Mirror {
	t.Helper()
	if alive == nil {
		alive = func(int) bool { return true }
	}
	m := &Mirror{Env: Start(t), sessionsDir: sessionsDir, claudeHome: claudeHome, alive: alive, known: map[int]bool{}}
	m.Sync()
	return m
}

// Sync copies every fixture file that is new or changed into the recorder's
// Claude directory and lets the recorder see it.
func (m *Mirror) Sync() {
	m.t.Helper()
	if m.sessionsDir != "" {
		ents, _ := os.ReadDir(m.sessionsDir)
		for _, de := range ents {
			name := de.Name()
			if de.IsDir() || !strings.HasSuffix(name, ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(m.sessionsDir, name))
			if err != nil {
				m.t.Fatal(err)
			}
			var head struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(raw, &head) != nil || head.PID == 0 {
				continue
			}
			pid := head.PID
			m.putIfChanged(filepath.Join("sessions", strconv.Itoa(pid)+".json"), m.registry(raw))
			if !m.known[pid] {
				m.known[pid] = true
				m.Procs.Add(pid, time.UnixMilli(StartMs), nil)
				if !m.alive(pid) {
					m.Procs.Exit(pid, time.UnixMilli(StartMs+9000))
				}
			}
		}
	}
	if m.claudeHome != "" {
		root := filepath.Join(m.claudeHome, "projects")
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(m.claudeHome, p)
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				m.t.Fatal(rerr)
			}
			m.putIfChanged(rel, b)
			return nil
		})
	}
	m.Settle()
}

// registry completes a fixture's registry file with the process start the
// recorder links a registration to its process by.
func (m *Mirror) registry(raw []byte) []byte {
	var x map[string]any
	if json.Unmarshal(raw, &x) != nil {
		return raw
	}
	x["startedAt"] = StartMs
	x["procStart"] = time.UnixMilli(StartMs).UTC().Format("Mon Jan _2 15:04:05 2006")
	if _, ok := x["updatedAt"]; !ok {
		x["updatedAt"] = StartMs
	}
	out, _ := json.Marshal(x)
	return out
}

func (m *Mirror) putIfChanged(rel string, content []byte) {
	dst := m.Path(rel)
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, content) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(dst, content, 0o600); err != nil {
		m.t.Fatal(err)
	}
}
