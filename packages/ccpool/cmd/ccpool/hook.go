package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

type hookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
}

// eventState maps the hook subcommand to the state it records.
var eventState = map[string]store.State{
	"start":  store.Ready,
	"stop":   store.Done,
	"fail":   store.Failed,
	"notify": store.NeedsInput,
}

// runHook is the CLI entrypoint. It NEVER returns nonzero on a logic failure —
// a wedged hook must not block Claude. It logs and exits 0 (spec §9/§15).
func runHook(args []string) int {
	// Resolve the log dir independently of config.toml so logging survives a
	// config-load failure (spec §9/§20 — the hook must never lose its diagnostic).
	stateDir := config.StateDirPath()
	if len(args) < 1 {
		logHook(stateDir, "hook: missing event arg")
		return 0
	}
	event := args[0]
	cfg, err := config.Load()
	if err != nil {
		logHook(stateDir, fmt.Sprintf("hook %s: config load: %v", event, err))
		return 0
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		logHook(stateDir, fmt.Sprintf("hook %s: store open: %v", event, err))
		return 0
	}
	defer st.Close()

	if err := handleHook(event, os.Stdin, st, os.Getenv("CCPOOL_NAME")); err != nil {
		logHook(stateDir, fmt.Sprintf("hook %s: %v", event, err))
	}
	return 0
}

// handleHook is the testable core: parse stdin, resolve the row, transition.
func handleHook(event string, stdin io.Reader, st *store.Store, envName string) error {
	to, ok := eventState[event]
	if !ok {
		return fmt.Errorf("unknown hook event %q", event)
	}
	var p hookPayload
	if err := json.NewDecoder(stdin).Decode(&p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	ctx := context.Background()

	name, ok, err := resolveName(ctx, st, p.SessionID, envName)
	if err != nil {
		return err
	}
	if !ok {
		// Unresolvable: not an error (spec §9 — log + exit 0). Caller logs.
		return nil
	}

	if event == "start" {
		if err := st.Upsert(ctx, name, p.SessionID); err != nil {
			return fmt.Errorf("upsert %q: %w", name, err)
		}
	}
	prior, err := st.Transition(ctx, name, to, p.SessionID, p.TranscriptPath)
	if err != nil {
		return fmt.Errorf("transition %q: %w", name, err)
	}
	// Notifier edge-trigger point (real adapters in Plan 4): fire only when
	// crossing INTO a notifying state from a different prior state.
	maybeNotify(prior, to, name)
	return nil
}

// resolveName finds the row's name by uuid==session_id, else by the launch-env
// CCPOOL_NAME. Returns ok=false (not an error) when neither resolves.
func resolveName(ctx context.Context, st *store.Store, sessionID, envName string) (string, bool, error) {
	if sessionID != "" {
		if s, ok, err := st.GetByUUID(ctx, sessionID); err != nil {
			return "", false, err
		} else if ok {
			return s.Name, true, nil
		}
	}
	if envName != "" {
		return envName, true, nil
	}
	return "", false, nil
}

// maybeNotify is a stub in Plan 1; Plan 4 wires the configured notifier here.
func maybeNotify(prior, to store.State, name string) {
	notifying := to == store.NeedsInput || to == store.Failed
	if notifying && prior != to {
		// no-op for now
		_ = name
	}
}

func logHook(stateDir, msg string) {
	_ = os.MkdirAll(stateDir, 0o700)
	f, err := os.OpenFile(filepath.Join(stateDir, "hook.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, msg)
}
