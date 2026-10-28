package session

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// fileState is what the no-settings-write contract compares: content and
// mtime of every file under a directory.
type fileState struct {
	content string
	mtime   time.Time
}

// snapshotDir records every file under dir (relative path -> content + mtime).
// A directory that does not exist snapshots as an empty map, so a created
// directory with files in it shows up as a difference.
func snapshotDir(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = fileState{content: string(b), mtime: info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

// TestEnsure_writesNoSettingsFileUnderCwd pins that a launch (new AND resume)
// leaves <cwd>/.claude exactly as it found it, even when the cwd's .mcp.json
// declares a server that no settings file classifies. Keeping such a server
// from prompting an unattended session is the deployment's job
// (--strict-mcp-config); ccpool MUST NOT write settings.local.json to do it
// (docs/adr/0052-ccpool-mcp-consent-canonical-decisions-consultation.md).
func TestEnsure_writesNoSettingsFileUnderCwd(t *testing.T) {
	for _, route := range []string{"new", "resume"} {
		for _, seeded := range []bool{false, true} {
			name := route + "/no-claude-dir"
			if seeded {
				name = route + "/existing-settings"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				// A real directory; EvalSymlinks inside Ensure resolves it, so
				// compute the snapshot dir from the same resolved path.
				cwd, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				mcp := `{"mcpServers":{"classified-nowhere":{"command":"true"},"other":{"command":"true"}}}`
				if err := os.WriteFile(filepath.Join(cwd, ".mcp.json"), []byte(mcp), 0o644); err != nil {
					t.Fatal(err)
				}
				claudeDir := filepath.Join(cwd, ".claude")
				if seeded {
					if err := os.MkdirAll(claudeDir, 0o755); err != nil {
						t.Fatal(err)
					}
					old := time.Unix(1_600_000_000, 0)
					for f, body := range map[string]string{
						"settings.json":       `{"enabledMcpjsonServers":["other"]}`,
						"settings.local.json": `{"disabledMcpjsonServers":["something-else"]}`,
					} {
						p := filepath.Join(claudeDir, f)
						if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
							t.Fatal(err)
						}
						// An old mtime makes any rewrite visible.
						if err := os.Chtimes(p, old, old); err != nil {
							t.Fatal(err)
						}
					}
				}
				before := snapshotDir(t, claudeDir)

				st := newMemStore(t)
				resumable := false
				if route == "resume" {
					resumable = true
					if err := st.Insert(ctx, store.Session{ExternalID: "ext-mcp", ClaudeSessionID: "csid-1", CWD: cwd, TranscriptPath: "/p/ext-mcp.jsonl", State: store.Idle, TmuxSession: "cc-ext-mcp"}); err != nil {
						t.Fatalf("seed row: %v", err)
					}
				}
				ft := &fakeTmux{live: map[string]bool{}}
				waiter := waitFunc(func(_ context.Context, externalID string, _ int64) (wait.Outcome, error) {
					_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
					return wait.Outcome{State: store.Ready}, nil
				})
				s := New(Deps{
					Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter, Exister: fakeExister{ok: resumable},
					Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
					NewUUID: func() string { return "csid-new" },
					Now:     func() time.Time { return time.Unix(100, 0) },
				})
				if _, err := s.Ensure(ctx, "ext-mcp", cwd, "", EnsureOpts{}); err != nil {
					t.Fatalf("Ensure (%s): %v", route, err)
				}
				if len(ft.newCalls) != 1 {
					t.Fatalf("NewSession calls = %d, want 1 (launch must have happened)", len(ft.newCalls))
				}
				argv := ft.newCalls[0].argv
				if route == "resume" && !contains(argv, "--resume") {
					t.Fatalf("resume route did not resume: %v", argv)
				}
				if route == "new" && !contains(argv, "--session-id") {
					t.Fatalf("new route did not start a fresh session: %v", argv)
				}

				after := snapshotDir(t, claudeDir)
				if !reflect.DeepEqual(before, after) {
					t.Errorf("launch changed files under %s\nbefore: %v\nafter:  %v", claudeDir, before, after)
				}
			})
		}
	}
}

func contains(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}
