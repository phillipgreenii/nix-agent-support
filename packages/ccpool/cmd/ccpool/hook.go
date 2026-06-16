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
	"github.com/phillipgreenii/ccpool/internal/notify"
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
	// Wire the append-only event log so hook-driven transitions are recorded in
	// order (nil-safe — a failed Open is non-fatal, mirroring the never-fail
	// policy below).
	el := openEventLog(cfg)
	st, err := store.Open(cfg.DBPath, clock.Real{}, store.WithEventLog(el))
	if err != nil {
		logHook(stateDir, fmt.Sprintf("hook %s: store open: %v", event, err))
		return 0
	}
	defer st.Close()

	n := notify.FromConfig(cfg.Notify.Adapter, cfg.Notify.Command)
	if err := handleHookN(event, os.Stdin, st, os.Getenv("CCPOOL_NAME"), n, cfg.Notify.On); err != nil {
		logHook(stateDir, fmt.Sprintf("hook %s: %v", event, err))
	}
	return 0
}

// handleHook is the production entrypoint (no notifier wired = None). Kept so
// existing Plan 1 tests calling handleHook still compile.
func handleHook(event string, stdin io.Reader, st *store.Store, envName string) error {
	return handleHookN(event, stdin, st, envName, notify.None{}, nil)
}

// handleHookN parses the payload, resolves the row, transitions, and fires the
// notifier on an edge into an On state (spec §9/§10).
func handleHookN(event string, stdin io.Reader, st *store.Store, envName string, n notify.Notifier, on []string) error {
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
	// On a completed turn (Stop → Done), lazily stamp the transcript anchor onto
	// the oldest pending fire-and-forget turn for this session so `ccpool result`
	// can resolve its reply (pg2-12ko). FIFO-pop is the v1 correlation assumption:
	// it breaks if an interactive (blocking) reply's Stop interleaves with a
	// pending fire-and-forget turn, or if a turn ends needs_input rather than Done.
	// Documented known limitation, not a v1 requirement (see ResolveOldestPendingTurn).
	// Best-effort: a turns-resolve error MUST NOT fail the hook nor suppress the
	// notifier below (never-fail policy, spec §9/§15) — ignore it; the transition
	// (the hook's primary job) already landed.
	if event == "stop" {
		_, _, _ = st.ResolveOldestPendingTurn(ctx, name, p.TranscriptPath)
	}
	if notify.ShouldNotify(on, string(prior), string(to)) {
		_ = n.Notify(notify.Event{Name: name, UUID: p.SessionID, State: string(to), CWD: p.CWD})
	}
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

func logHook(stateDir, msg string) {
	_ = os.MkdirAll(stateDir, 0o700)
	f, err := os.OpenFile(filepath.Join(stateDir, "hook.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, msg)
}
