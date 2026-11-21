package query

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/procenv"
	"github.com/phillipgreenii/x/cmdrun"
)

// envTestMode is PG_ROUTER_TEST_MODE, set by run-role/run-query in their own
// process env. The strict base environment keeps it by name where set.
const envTestMode = "PG_ROUTER_TEST_MODE"

// cancelGrace is how long a cancelled query's process group has between SIGTERM
// and SIGKILL.
const cancelGrace = 5 * time.Second

// CommandQuery runs an executable and parses its stdout into items.
type CommandQuery struct {
	Meta   `toml:"-"`
	Argv   []string    `toml:"argv"`
	Format QueryFormat `toml:"format"`
	// EnvAllow is the [[query]]-level env_allow list: the names, besides the
	// base environment, that the process receives (for example SSH_AUTH_SOCK).
	// The process always starts from the minimal base environment, so a
	// variable that is not listed is not passed.
	EnvAllow []string `toml:"-"`
	// Settle is the query's settle command (a cursor-feed source's settlement
	// report), run exactly as Argv is, with --lane <lane> --through <token>
	// appended. Empty for every other query. AckHoldLimit is the query's hold
	// limit (config validated > 0; only meaningful with Settle).
	Settle       []string      `toml:"-"`
	AckHoldLimit time.Duration `toml:"-"`
}

// SettleOf returns the settle command and hold limit of a command query that
// declares one.
func SettleOf(q Query) (argv []string, holdLimit time.Duration, ok bool) {
	var cq CommandQuery
	switch v := q.(type) {
	case CommandQuery:
		cq = v
	case *CommandQuery:
		cq = *v
	default:
		return nil, 0, false
	}
	if len(cq.Settle) == 0 {
		return nil, 0, false
	}
	return append([]string(nil), cq.Settle...), cq.AckHoldLimit, true
}

// RunSettle runs q's settle command for one lane, reporting token as the
// frontier. It runs the command exactly as the query's own command is run (same
// commander, same minimal base environment plus env_allow) with
// `--lane <lane> --through <token>` appended.
func RunSettle(ctx context.Context, env Env, q Query, lane, token string) error {
	var cq CommandQuery
	switch v := q.(type) {
	case CommandQuery:
		cq = v
	case *CommandQuery:
		cq = *v
	default:
		return fmt.Errorf("settle: %T is not a command query", q)
	}
	if len(cq.Settle) == 0 {
		return errors.New("settle: query declares no settle command")
	}
	argv := append(append([]string(nil), cq.Settle...), "--lane", lane, "--through", token)
	cmd := env.Cmd
	if cmd == nil {
		cmd = OSCommander{}
	}
	var err error
	if ec, ok := cmd.(EnvCommander); ok {
		_, err = ec.RunEnv(ctx, argv, cq.runEnv())
	} else {
		_, err = cmd.Run(ctx, argv)
	}
	if err != nil {
		return fmt.Errorf("settle %v: %w", argv, err)
	}
	return nil
}

// CommandOf returns the command line of a command query (a copy of its argv: a
// query that wraps its producer writes the wrapper as its own command), its
// and whether q is a command query at all.
func CommandOf(q Query) (argv []string, ok bool) {
	var cq CommandQuery
	switch v := q.(type) {
	case CommandQuery:
		cq = v
	case *CommandQuery:
		cq = *v
	default:
		return nil, false
	}
	return append([]string(nil), cq.Argv...), true
}

// runEnv is the environment the process starts from: always the minimal base
// environment plus PG_ROUTER_TEST_MODE and the query's env_allow names, never
// the daemon's ambient environment.
func (q CommandQuery) runEnv() []string {
	return procenv.BaseEnv(os.Environ(), append([]string{envTestMode}, q.EnvAllow...))
}

// RunRaw runs a command query once and returns its raw standard output without
// parsing it into events: the path a level-source query takes, whose output is
// level answers and never items. extraEnv (NAME=value entries) is added to the
// process environment for this one run and bypasses the env_allow list: the
// allowlist narrows what the router passes through, not what the router itself
// sets. The deadline is the caller's context; the process group is killed when
// it ends (OSCommander).
func RunRaw(ctx context.Context, env Env, q Query, extraEnv []string) ([]byte, error) {
	var cq CommandQuery
	switch v := q.(type) {
	case CommandQuery:
		cq = v
	case *CommandQuery:
		cq = *v
	default:
		return nil, fmt.Errorf("a level source must be a command query, got %T", q)
	}
	cmd := env.Cmd
	if cmd == nil {
		cmd = OSCommander{}
	}
	runEnv := append(cq.runEnv(), extraEnv...)
	var out []byte
	var err error
	if ec, ok := cmd.(EnvCommander); ok {
		out, err = ec.RunEnv(ctx, cq.Argv, runEnv)
	} else {
		out, err = cmd.Run(ctx, cq.Argv)
	}
	if err != nil {
		return out, fmt.Errorf("command query %v: %w", cq.Argv, err)
	}
	return out, nil
}

func (q CommandQuery) Validate() error {
	if len(q.Argv) == 0 {
		return fmt.Errorf("command query: argv is required")
	}
	if q.Format != FormatJSONL && q.Format != FormatJSON {
		return fmt.Errorf("command query: format must be jsonl or json, got %q", q.Format)
	}
	return nil
}

// ArgvOf returns a copy of the command line a query shells out to (the argv a
// CommandQuery runs, or a ParticipantQuery's command prefix), or nil for a
// query with no command (an in-process producer such as the timer). It exists
// so a failure log can name what actually ran (bead pg2-zdowv).
func ArgvOf(q Query) []string {
	var argv []string
	switch v := q.(type) {
	case CommandQuery:
		argv = v.Argv
	case *CommandQuery:
		argv = v.Argv
	case ParticipantQuery:
		argv = v.Command
	case *ParticipantQuery:
		argv = v.Command
	}
	if len(argv) == 0 {
		return nil
	}
	return append([]string(nil), argv...)
}

// BackingCommand is argv[0]: the executable this source shells out to.
func (q CommandQuery) BackingCommand() string {
	if len(q.Argv) == 0 {
		return ""
	}
	return q.Argv[0]
}

// rawItem is the per-record shape a command source's stdout decodes into.
// type keeps its pre-existing meaning (item classification, carried through to
// item.Item.Type) untouched. at/expiresAt are OPTIONAL RFC3339 timestamp
// strings, camelCase to match the event wire's own at/expiresAt (Task 1.4);
// when present they are carried onto the produced event's Attributes (the
// general "extra, type-specific fields" seam event.Event already declares),
// and discover.ToQueueEvent copies them onto eventqueue.Event's At /
// ExpiresAt (INV-EVT-1, DEC-RETRY-2). emit is an
// OPTIONAL event-type selector: when present it MUST be one of the query's
// declared Emits() (multi-emit); absent, the record falls back to
// firstEmit(q) — today's behavior, unchanged.
type rawItem struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Metadata  map[string]any `json:"metadata"`
	At        string         `json:"at,omitempty"`
	ExpiresAt string         `json:"expiresAt,omitempty"`
	Emit      string         `json:"emit,omitempty"`
	// Ack is the optional acknowledgement handle: an object with exactly two
	// non-empty string fields, lane and token. Decoded loosely so a malformed
	// one only means the event carries no handle, never a parse failure.
	Ack any `json:"ack,omitempty"`
}

// ackFromAny reads an ack handle from a decoded JSON value, nil unless it is an
// object whose lane and token are both non-empty strings.
func ackFromAny(v any) *event.Ack {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	lane, _ := m["lane"].(string)
	token, _ := m["token"].(string)
	if lane == "" || token == "" {
		return nil
	}
	return &event.Ack{Lane: lane, Token: token}
}

func (q CommandQuery) Run(ctx context.Context, env Env) ([]event.Event, error) {
	cmd := env.Cmd
	if cmd == nil {
		cmd = OSCommander{}
	}
	var out []byte
	var err error
	if ec, ok := cmd.(EnvCommander); ok {
		out, err = ec.RunEnv(ctx, q.Argv, q.runEnv())
	} else {
		out, err = cmd.Run(ctx, q.Argv)
	}
	if err != nil {
		return nil, fmt.Errorf("command query %v: %w", q.Argv, err)
	}
	var raws []rawItem
	switch q.Format {
	case FormatJSON:
		if len(bytes.TrimSpace(out)) == 0 {
			return nil, nil
		}
		if err := json.Unmarshal(out, &raws); err != nil {
			return nil, fmt.Errorf("command query: parse json: %w", err)
		}
	default: // jsonl
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var r rawItem
			if err := json.Unmarshal(line, &r); err != nil {
				return nil, fmt.Errorf("command query: parse jsonl line: %w", err)
			}
			raws = append(raws, r)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("command query: read output: %w", err)
		}
	}
	def := firstEmit(q)
	events := make([]event.Event, 0, len(raws))
	var errs []error
	for _, r := range raws {
		if r.ID == "" {
			return nil, fmt.Errorf("command query: record missing required \"id\"")
		}
		emitType := def
		if r.Emit != "" {
			if !declaresEmit(q, r.Emit) {
				errs = append(errs, fmt.Errorf("command query: record %q: emit %q is not among the declared emits %v", r.ID, r.Emit, q.Emits()))
				continue
			}
			emitType = r.Emit
		}
		evt := event.NewItemEvent(emitType, "", item.Item{ID: r.ID, Type: r.Type, Title: r.Title, Metadata: r.Metadata})
		evt.Ack = ackFromAny(r.Ack)
		if r.At != "" || r.ExpiresAt != "" {
			attrs := make(map[string]any, 2)
			if r.At != "" {
				at, perr := time.Parse(time.RFC3339, r.At)
				if perr != nil {
					errs = append(errs, fmt.Errorf("command query: record %q: parse \"at\": %w", r.ID, perr))
					continue
				}
				attrs["at"] = at
			}
			if r.ExpiresAt != "" {
				expiresAt, perr := time.Parse(time.RFC3339, r.ExpiresAt)
				if perr != nil {
					errs = append(errs, fmt.Errorf("command query: record %q: parse \"expiresAt\": %w", r.ID, perr))
					continue
				}
				attrs["expiresAt"] = expiresAt
			}
			evt.Attributes = attrs
		}
		events = append(events, evt)
	}
	if len(errs) > 0 {
		return events, errors.Join(errs...)
	}
	return events, nil
}

// commandTimeout is the backstop bound on one command-query run. The caller's
// context (the per-pull deadline, DefaultDeadline unless configured) is the
// normal limit; this bounds a call whose caller passed a context with none.
const commandTimeout = 5 * time.Minute

// ExitCodeError is the error a command that ran but did not succeed produces:
// it exited non-zero (Code) or was ended by a signal (Signal, Code -1). Stderr
// is the child's trimmed stderr, folded into the message so the reason a
// source's backing command wrote reaches the producer-tick log.
type ExitCodeError struct {
	Code   int
	Signal string
	Stderr string
}

func (e *ExitCodeError) Error() string {
	msg := fmt.Sprintf("exit status %d", e.Code)
	if e.Signal != "" {
		msg = "signal: " + e.Signal
	}
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// OSCommander is the default Commander: it runs the command through
// x/cmdrun.
type OSCommander struct {
	// Exec runs the call; nil means cmdrun.Exec. Tests inject cmdruntest.Fake.
	Exec cmdrun.Runner
}

func (o OSCommander) Run(ctx context.Context, argv []string) ([]byte, error) {
	return o.RunEnv(ctx, argv, nil)
}

// RunEnv runs argv with env (nil inherits the process environment; non-nil is
// the child's whole environment) in its own process group: a context cancel
// sends SIGTERM to the whole group, then SIGKILL after cancelGrace, so a
// non-exec wrapper cannot orphan the payload.
func (o OSCommander) RunEnv(ctx context.Context, argv []string, env []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	run := o.Exec
	if run == nil {
		run = cmdrun.Exec
	}
	call := cmdrun.Call{
		Name:         argv[0],
		Args:         argv[1:],
		OwnProcGroup: true,
		TermGrace:    cancelGrace,
		Timeout:      commandTimeout,
	}
	if env != nil {
		call.Env, call.EnvMode = env, cmdrun.EnvExact
	}
	res := run(ctx, call)
	switch {
	case res.StartErr != nil:
		return nil, res.StartErr
	case res.TimedOut:
		return res.Stdout, fmt.Errorf("timed out after %s", commandTimeout)
	case res.CtxErr != nil:
		return res.Stdout, res.CtxErr
	case res.Signal != "" || res.Code != 0:
		// The *ExitCodeError carries the child's own stderr: the bare "exit
		// status N" never names the cause a source's backing command wrote.
		return res.Stdout, &ExitCodeError{Code: res.Code, Signal: res.Signal, Stderr: strings.TrimSpace(string(res.Stderr))}
	}
	return res.Stdout, nil
}
