package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// ToolName/ToolInput are populated for the PreToolUse `ask` event
	// (tool_name=="AskUserQuestion"); empty/absent for the other hook events.
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// askToolInput is the AskUserQuestion tool_input shape (claude 2.1.177): a list of
// questions, each carrying the prompt text we surface as the pending question.
type askToolInput struct {
	Questions []struct {
		Question    string `json:"question"`
		Header      string `json:"header"`
		MultiSelect bool   `json:"multiSelect"`
		Options     []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

// eventState maps the hook subcommand to the observed session FACT it records
// (ADR 0015): start→ready, stop→idle (Claude Stop = turn ended), fail→errored
// (Claude StopFailure = API error), notify→needs_input. None is a work judgment.
var eventState = map[string]store.State{
	"start":  store.Ready,
	"stop":   store.Idle,
	"fail":   store.Errored,
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
	// Build the transient-retry actuator (StopFailure in-place retry). It is
	// best-effort: a nil actuator (or any retry-path error) falls back to today's
	// `errored` transition (never-fail policy, spec §9/§15).
	ra := &retryActuator{
		cfg:    cfg.Retry,
		store:  st,
		nudger: newTmuxNudger(cfg.Tmux.Socket),
		now:    time.Now,
		sleep:  time.Sleep,
	}
	if err := handleHookN(event, os.Stdin, st, os.Getenv("CCPOOL_EXTERNAL_ID"), n, cfg.Notify.On, ra); err != nil {
		logHook(stateDir, fmt.Sprintf("hook %s: %v", event, err))
	}
	return 0
}

// handleHook is the production entrypoint (no notifier wired = None). Kept so
// existing tests calling handleHook still compile. envExternalID is the
// launch-injected CCPOOL_EXTERNAL_ID fallback (ADR 0015).
func handleHook(event string, stdin io.Reader, st *store.Store, envExternalID string) error {
	return handleHookN(event, stdin, st, envExternalID, notify.None{}, nil, nil)
}

// handleHookN parses the payload, resolves the row by claude_session_id (else the
// launch-env external_id), transitions, and fires the notifier on an edge into an
// On state (spec §9/§10).
func handleHookN(event string, stdin io.Reader, st *store.Store, envExternalID string, n notify.Notifier, on []string, ra *retryActuator) error {
	// The `ask` event (PreToolUse/AskUserQuestion) is NOT a plain eventState entry —
	// it must additionally parse the question text — so it is handled separately.
	if event == "ask" {
		return handleAskHook(stdin, st, envExternalID, n, on)
	}
	to, ok := eventState[event]
	if !ok {
		return fmt.Errorf("unknown hook event %q", event)
	}
	var p hookPayload
	if err := json.NewDecoder(stdin).Decode(&p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	ctx := context.Background()
	externalID, ok, err := resolveExternalID(ctx, st, p.SessionID, envExternalID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if event == "start" {
		if err := st.Upsert(ctx, externalID, p.SessionID, ""); err != nil {
			return fmt.Errorf("upsert %q: %w", externalID, err)
		}
	}
	// StopFailure (`fail`) transient-retry: before recording `errored`, try to
	// resume the SAME Claude session in place when the error is transient and
	// budget remains. On a successful retry the row stays out of `errored`
	// (maybeRetry returns it to `working`); the hook is done. Any retry-path
	// error or a non-retry decision falls through to today's `errored`
	// transition (never-fail policy, spec §9/§15).
	if event == "fail" && ra != nil {
		if retried := tryRetryOnFail(ctx, ra, st, externalID, p.TranscriptPath); retried {
			return nil
		}
	}
	prior, err := st.Transition(ctx, externalID, to, p.SessionID, p.TranscriptPath)
	if err != nil {
		return fmt.Errorf("transition %q: %w", externalID, err)
	}
	// A `notify` needs_input edge (permission_prompt/idle_prompt) is NOT an
	// AskUserQuestion, so clear any stale AskUserQuestion text the row may still
	// carry from a prior turn — only the `ask` path sets a question, and Transition
	// preserves it on the way INTO NeedsInput. Best-effort (never-fail).
	if event == "notify" {
		_ = st.SetPendingQuestion(ctx, externalID, "")
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
		_, _, _ = st.ResolveOldestPendingTurn(ctx, externalID, p.TranscriptPath)
		// A successful turn ended: reset the transient-retry budget so a later,
		// unrelated transient error gets a fresh attempt count rather than
		// inheriting an old one (spec §"Counter resets on a successful turn").
		// Best-effort — a reset failure must not fail the hook (never-fail).
		_ = st.ResetRetry(ctx, externalID)
	}
	if notify.ShouldNotify(on, string(prior), string(to)) {
		_ = n.Notify(notify.Event{Name: externalID, UUID: p.SessionID, State: string(to), CWD: p.CWD})
	}
	return nil
}

// handleAskHook handles the PreToolUse/AskUserQuestion `ask` event: it records the
// deterministic needs_input edge the instant the model invokes the tool (claude
// 2.1.177), persists the question text, and fires the notifier on the edge — all
// NON-BLOCKING (the caller exits 0) so the picker still renders for a human to
// attend (pg2-7a5b, pg2-r0zz). It resolves the session the same way the other
// events do (by claude_session_id, else CCPOOL_EXTERNAL_ID).
func handleAskHook(stdin io.Reader, st *store.Store, envExternalID string, n notify.Notifier, on []string) error {
	var p hookPayload
	if err := json.NewDecoder(stdin).Decode(&p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	// Defensive: the hooks.json matcher already restricts this to AskUserQuestion,
	// but guard a broader/misconfigured wiring so we never record needs_input for a
	// different tool. An empty tool_name (e.g. a minimal test payload) is tolerated.
	if p.ToolName != "" && p.ToolName != "AskUserQuestion" {
		return nil
	}
	ctx := context.Background()
	externalID, ok, err := resolveExternalID(ctx, st, p.SessionID, envExternalID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	// Record the deterministic needs_input edge. Transition leaves pending_question
	// untouched on the way INTO NeedsInput, so the SetPendingQuestion below survives.
	prior, err := st.Transition(ctx, externalID, store.NeedsInput, p.SessionID, p.TranscriptPath)
	if err != nil {
		return fmt.Errorf("transition %q: %w", externalID, err)
	}
	if err := st.SetPendingQuestion(ctx, externalID, askQuestionText(p.ToolInput)); err != nil {
		return fmt.Errorf("set pending question %q: %w", externalID, err)
	}
	// Fire the notifier on the working→needs_input edge, exactly like the existing
	// needs_input path (spec §10).
	if notify.ShouldNotify(on, string(prior), string(store.NeedsInput)) {
		_ = n.Notify(notify.Event{Name: externalID, UUID: p.SessionID, State: string(store.NeedsInput), CWD: p.CWD})
	}
	return nil
}

// askQuestionText extracts the human-facing question text from an AskUserQuestion
// tool_input. It uses questions[0].question; when there are multiple questions it
// joins their text with "; ". A malformed/empty input yields "" (best-effort —
// the never-fail policy means a parse miss must not block the picker).
func askQuestionText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in askToolInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	parts := make([]string, 0, len(in.Questions))
	for _, q := range in.Questions {
		if q.Question != "" {
			parts = append(parts, q.Question)
		}
	}
	return strings.Join(parts, "; ")
}

// resolveExternalID finds the row's external_id by claude_session_id==session_id,
// else falls back to the launch-env CCPOOL_EXTERNAL_ID (ADR 0015). Returns
// ok=false (not an error) when neither resolves.
func resolveExternalID(ctx context.Context, st *store.Store, sessionID, envExternalID string) (string, bool, error) {
	if sessionID != "" {
		if s, ok, err := st.GetByClaudeSessionID(ctx, sessionID); err != nil {
			return "", false, err
		} else if ok {
			return s.ExternalID, true, nil
		}
	}
	if envExternalID != "" {
		return envExternalID, true, nil
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
