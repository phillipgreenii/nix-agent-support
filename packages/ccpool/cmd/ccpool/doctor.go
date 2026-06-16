package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
	"github.com/phillipgreenii/ccpool/internal/trust"
)

// doctorPoolHeader renders the active pool's context for `ccpool doctor`.
func doctorPoolHeader(cfg config.Config) string {
	root := cfg.PoolRoot
	if root == "" {
		root = "default (XDG)"
	}
	return fmt.Sprintf("pool: %s\n  db:     %s\n  socket: %s\n  hook.log: %s\n  events.jsonl: %s\n",
		root, cfg.DBPath, cfg.Tmux.Socket, filepath.Join(cfg.StateDir, "hook.log"), cfg.EventLogPath())
}

func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	_ = fs.Parse(args)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	fmt.Print(doctorPoolHeader(cfg))
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()
	cl := tmux.NewClient(cfg.Tmux.Socket)
	home, _ := os.UserHomeDir()
	claudeJSON := filepath.Join(home, ".claude.json")

	report := func(r store.Session) {
		live := cl.HasSession(cfg.Tmux.Prefix + r.ExternalID)
		// cwd-trust is one of the three hang causes doctor must distinguish (§20):
		// untrusted cwd, dropped send-key, or missing/failed hook.
		trusted := r.CWD != "" && trust.IsTrusted(claudeJSON, r.CWD)
		fmt.Printf("external_id=%s name=%s state=%s live=%v cwd_trusted=%v claude_session_id=%s\n",
			r.ExternalID, r.Name, r.State, live, trusted, r.ClaudeSessionID)
		fmt.Printf("  cwd=%s\n  transcript=%s\n", r.CWD, r.TranscriptPath)
	}

	ctx := context.Background()
	if fs.NArg() >= 1 {
		row, ok, _ := st.GetByExternalID(ctx, fs.Arg(0))
		if !ok {
			fmt.Fprintln(os.Stderr, "no such session")
			return 1
		}
		report(row)
	} else {
		rows, _ := st.List(ctx)
		for _, r := range rows {
			report(r)
		}
	}
	// hook.log tail.
	logPath := filepath.Join(cfg.StateDir, "hook.log")
	if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 {
		fmt.Println("--- recent hook.log ---")
		fmt.Print(string(tailBytes(b, 2000)))
	}
	return 0
}

func tailBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}
