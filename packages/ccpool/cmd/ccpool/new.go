package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/launch"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/trust"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

func runNew(args []string) int {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	cwd := fs.String("cwd", "", "project dir (default: current dir)")
	model := fs.String("model", "", "claude model")
	displayName := fs.String("name", "", "optional display label for the session (claude --name; nullable)")
	env := envFlag{}
	fs.Var(env, "env", "extra env KEY=VAL injected into the session (repeatable)")
	meta := metaFlag{}
	fs.Var(meta, "meta", "session metadata KEY=VAL upserted at dispatch (repeatable)")
	label := labelFlag{}
	fs.Var(label, "label", "flag a metadata key (set via --meta in this same call, or previously) as label-eligible (repeatable)")
	permMode := fs.String("permission-mode", "", "claude --permission-mode value: default|acceptEdits|plan|auto|dontAsk|bypassPermissions (workers need bypassPermissions)")
	allowedTools := fs.String("allowed-tools", "", "claude --allowed-tools allowlist forwarded verbatim (comma/space-separated, e.g. \"Bash(git *),Edit\"); empty omits the flag")
	effort := fs.String("effort", "", "claude --effort value (e.g. max)")
	autonomous := fs.Bool("autonomous", false, "autonomous mode: block AskUserQuestion (the hook denies it so a human-less worker never stalls on the picker); injects CCPOOL_AUTONOMOUS into the session")
	pos := parseInterspersed(fs, args) // flags may follow the positional external_id
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool new <external_id> [--name label] [--cwd dir] [--model m] [--env KEY=VAL ...] [--meta KEY=VAL ...] [--label KEY ...] [--permission-mode m] [--allowed-tools list] [--effort v] [--autonomous]")
		return 2
	}
	externalID := pos[0]

	// Validate --permission-mode against the documented set BEFORE any I/O. Empty
	// is allowed (omit the flag); an explicit unknown value is a usage error (2),
	// consistent with the other usage failures above.
	if *permMode != "" && !launch.PermissionMode(*permMode).Valid() {
		fmt.Fprintf(os.Stderr, "ccpool new: invalid --permission-mode %q (want one of: default acceptEdits plan auto dontAsk bypassPermissions)\n", *permMode)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("new: config load failed", "err", err)
		return 1
	}
	dir := *cwd
	if dir == "" {
		if cfg.Claude.DefaultCwd != "" {
			dir = cfg.Claude.DefaultCwd
		} else {
			dir, _ = os.Getwd()
		}
	}
	m := *model
	if m == "" {
		m = cfg.Claude.DefaultModel
	}

	el := openEventLog(cfg)
	st, err := store.Open(cfg.DBPath, clock.Real{}, store.WithEventLog(el))
	if err != nil {
		slog.Error("new: store open failed", "err", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	svc := session.New(newSessionDeps(cfg, st, el))

	h, err := svc.Ensure(context.Background(), externalID, dir, m, session.EnsureOpts{
		Env:            env,
		Name:           *displayName,
		PermissionMode: launch.PermissionMode(*permMode),
		AllowedTools:   *allowedTools,
		Effort:         *effort,
		Autonomous:     *autonomous,
		Meta:           meta,
	})
	if err != nil {
		slog.Error("new: ensure failed", "err", err)
		return 1
	}
	// --label marks each named key as label-eligible in this SAME invocation,
	// after the metadata itself (--meta, applied inside Ensure) has committed
	// (pg2-24f89/pg2-qye99 D8.1).
	if err := applyLabels(st, externalID, label); err != nil {
		slog.Error("new: label failed", "err", err)
		return 1
	}
	// Columns: external_id, name, state, short claude_session_id (ADR 0015).
	fmt.Printf("%s\t%s\t%s\t%s\n", h.ExternalID, h.Name, h.State, shortUUID(h.ClaudeSessionID))
	return 0
}

// envFlag collects repeated `--env KEY=VAL` into a map. Implements flag.Value so
// `ccpool new` can take --env any number of times (pg-router injects one per key).
type envFlag map[string]string

func (e envFlag) String() string { return "" }

func (e envFlag) Set(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("invalid --env %q, want KEY=VAL", kv)
	}
	e[k] = v
	return nil
}

// metaFlag collects repeated `--meta KEY=VAL` into a map (mirrors envFlag). An empty
// value (`--meta k=`) is a valid bare tag. Wired into EnsureOpts.Meta so metadata is
// set atomically as part of `ccpool new`, not a separate `ccpool meta set` call.
type metaFlag map[string]string

func (m metaFlag) String() string { return "" }

func (m metaFlag) Set(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("invalid --meta %q, want KEY=VAL", kv)
	}
	m[k] = v
	return nil
}

// labelFlag collects repeated `--label <key>` into a set (mirrors envFlag/
// metaFlag, but a bare key rather than a KEY=VAL pair). Wired into a
// post-metadata-write MarkAsLabel loop (applyLabels) so `ccpool new` and
// `ccpool meta set` flag a key as label-eligible in the same invocation that
// writes the metadata itself (pg2-24f89/pg2-qye99 D8.1).
type labelFlag map[string]bool

func (l labelFlag) String() string { return "" }

func (l labelFlag) Set(key string) error {
	if key == "" {
		return fmt.Errorf("invalid --label %q: key required", key)
	}
	l[key] = true
	return nil
}

// labelMarker is the minimal store surface applyLabels needs — satisfied by
// *store.Store — kept as a seam so the --label wiring is unit-testable
// without a real SQLite store.
type labelMarker interface {
	MarkAsLabel(externalID, key string) error
}

// applyLabels calls MarkAsLabel(externalID, key) for every key in labels.
// Iteration order over a set is unspecified; the design places no ordering
// requirement on repeated --label flags, so that is fine. Returns the first
// error encountered (e.g. a key never written via SetMeta/--meta).
func applyLabels(m labelMarker, externalID string, labels labelFlag) error {
	for k := range labels {
		if err := m.MarkAsLabel(externalID, k); err != nil {
			return err
		}
	}
	return nil
}

// truster adapts trust.EnsureTrusted to session.Truster (binds the path).
type truster struct{ path string }

func (t truster) EnsureTrusted(cwd string) error { return trust.EnsureTrusted(t.path, cwd) }

// storeWaiter adapts the store + wait poll loop to session.Waiter.
type storeWaiter struct {
	st      *store.Store
	timeout time.Duration
}

func (w storeWaiter) Wait(ctx context.Context, name string, since int64) (wait.Outcome, error) {
	return wait.ForGenerationAdvance(ctx, w.st, name, since, wait.Opts{Timeout: w.timeout})
}
