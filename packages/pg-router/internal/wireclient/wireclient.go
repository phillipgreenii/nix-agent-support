// Package wireclient is pg-router's core-side client for INTF-HANDLER's
// `dispatch` message (docs/decisions/wire.md's DEC-WIRE-1: a CLI invocation
// carrying JSON, with coarse exit codes). It replaces the in-process
// executor.For(roleType).Dispatch(...) call docket pg2-oju6w's Task 5.2/5.3
// moved out of this module (that logic now lives in the registered handler
// participant, e.g. packages/pg-router-handlers) with a real message
// crossing the wire — resolving register row INTF-HANDLER's residual ("the
// call itself is still a Go method call, never a message crossing a wire",
// ADR 0065's "Register" section) for real.
//
// A handler participant's own INVOCABLE COMMAND — which binary to run — is
// deliberately NOT resolved here: CommandFor is the injected seam a caller (the
// deployment/wiring layer — cmd/pg-router's bootCore and runRunRole, wired by
// handlerCommandFor, which hands back the role's own handler) supplies to
// answer it, so this package never has to guess the answer for every possible
// deployment. A role's own settings ride on stdin as the request's role object.
package wireclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-router/api/participant"
	"github.com/phillipgreenii/pg-router/api/status"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/schemas"
)

// HandlerClient replaces executor.For/Deps (Task 5.4's Interfaces block):
// send one event to whichever handler participant is registered for role,
// and return its reply unmodified.
//
// PostStartup/PreShutdown (pg2-oju6w.15) are the once-per-process-lifetime
// lifecycle hooks: dispatched once per enabled role, at boot (after bootCore
// succeeds) and at shutdown (where TeardownAll used to fire), over this same
// transport — a different subcommand name and call site, never a different
// mechanism. Unlike Dispatch there is no event to carry and no deferred/busy
// branch: these are not queued dispatches, so ErrBusy has nothing to mean
// here.
type HandlerClient interface {
	Dispatch(ctx context.Context, role roles.Role, evt eventqueue.Event) (Reply, error)
	PostStartup(ctx context.Context, role roles.Role) (Reply, error)
	PreShutdown(ctx context.Context, role roles.Role) (Reply, error)
}

// Reply is handler.dispatch-reply, decoded. Outcome is deliberately opaque —
// internal/complete no longer lives in pg-router to interpret it (that
// package moved out with Task 5.2; docket pg2-oju6w's Task 5.5 finishes
// removing pg-router's own remaining created/closed/handed-back branching
// over it) — Dispatch returns it verbatim, never parsed further.
type Reply struct {
	ID       string // echoes the dsp-<...> tracking id
	Deferred bool
	Outcome  string // opaque; internal/complete no longer lives here to interpret it (Task 5.5)
	// Skipped is the reply's OPTIONAL skipped reason tag: the handler accepted the
	// event and completed it WITHOUT doing any work because it was moot (bead
	// pg2-6t91v7). "" for a real completion. It is relayed verbatim and only
	// used to count the completion apart from a real one; the tag is the
	// handler's own vocabulary and this package interprets nothing about it.
	Skipped string
	// StartedAt is the dispatch's own start as the handler reports it in the
	// reply's OPTIONAL startedAt (DEC-EVENT-5, bead pg2-dm6de): a handler that
	// ADOPTED a dispatch an earlier daemon had started names the instant that
	// dispatch began, so the core can time it from there rather than from the
	// re-offer. It is the zero time when the reply carried none or carried one
	// that is not an RFC 3339 instant (Dispatch logs a warning for the latter and
	// keeps the dispatch's outcome: the field is advisory). This package does not
	// range-check it; the caller, which knows the event and the offer, does.
	StartedAt time.Time
	// Session and Supervisors are the reply's OPTIONAL supervision report: the
	// ccpool session's external id a lease takeover absorbed and the supervisor
	// count that takeover returned. They are set only when BOTH were present and
	// valid (a non-empty string session, an integer supervisors of at least 1);
	// otherwise both stay zero and the reply is used as before.
	Session     string
	Supervisors int
}

// ErrBusy is returned when the invoked participant's dispatch subcommand
// exits busy (code 9 — DEC-WIRE-1's "Coarse exit codes"). A caller (e.g.
// roleListener.Offer) maps this to a pre-accept eventqueue.DeclineBusy,
// exactly as the retired in-process executor.ErrBusy used to.
var ErrBusy = errors.New("wireclient: handler busy")

// BusyDecline refines the bare ErrBusy sentinel with the OPTIONAL reason tag
// a participant's exit-9 reply body MAY carry (bead pg2-j4uwg; DEC-WIRE-1 /
// interfaces.md's "Coarse outcome, rich reply": "a reply body, where the
// participant supplies one, still carries which case applies"). Reason is
// OPAQUE to this package — exactly like Reply.Outcome, never interpreted
// here — and is "" when the participant supplied no body at all (DEC-WIRE-1:
// "no body required"), which stays a fully legal decline.
//
// Every Dispatch on exit 9 now returns a *BusyDecline (never the bare
// ErrBusy value) so Reason is always reachable via errors.As, but
// errors.Is(err, ErrBusy) keeps matching it via the Is method below — no
// EXISTING caller (roleListener.Offer, the only wireclient.ErrBusy check in
// this module today) needs to change unless it wants Reason too.
//
// Kind and Detail are the body's two OPTIONAL refinements (the decline-body
// branch of handler.dispatch-reply.schema.json). Kind is the raw text the body
// sent ("busy", "fault" or "neutral" from a conforming participant), "" when
// absent; this package does not map absent or unknown to busy, that is the
// consumer's job. Detail is bounded, redacted free text for observability only.
type BusyDecline struct {
	Reason string
	Kind   string
	Detail string
}

func (e *BusyDecline) Error() string {
	if e.Reason == "" {
		return ErrBusy.Error()
	}
	return ErrBusy.Error() + ": " + e.Reason
}

// Is makes errors.Is(err, ErrBusy) match a *BusyDecline exactly as it
// matched the bare sentinel before this type existed.
func (e *BusyDecline) Is(target error) bool { return target == ErrBusy }

// Runner executes one dispatch subcommand invocation: argv (the handler's
// own CommandFor-resolved command, with "dispatch" appended) fed stdin,
// returning its stdout and coarse exit code. The production default
// (OSRunner) execs a real subprocess; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, argv []string, stdin []byte) (stdout []byte, exitCode int, err error)
}

// OSRunner is the production Runner: a real subprocess, matching DEC-WIRE-1's
// default transport (a CLI invocation carrying JSON on stdin/stdout).
//
// The handler's stderr is FORWARDED to Stderr (default: this process's own
// os.Stderr), never discarded. exec.Cmd with a nil Stderr wires the child's
// fd 2 to /dev/null, which silently dropped every slog line the handler
// emits (e.g. "dispatch: worktree removed" / "worktree cleanup deferred",
// bead pg2-7pp2m). Inheriting os.Stderr lands them in the daemon's own
// launchd StandardErrorPath log.
type OSRunner struct {
	// Stderr receives the handler subprocess's stderr. nil means os.Stderr.
	Stderr io.Writer
}

// runWaitDelay is cmd.WaitDelay for OSRunner.Run. It bounds two things: how long
// a handler that was sent SIGTERM on cancel has to exit before it is SIGKILLed,
// and how long Run waits for the stdout copy after the handler has exited.
//
// It MUST exceed the command adapter's own payload cancel grace (5 s, SIGTERM to
// the payload group and then SIGKILL) plus a margin for the adapter to write its
// reply, or the SIGKILL would land while the adapter is still stopping its
// payload and leave that payload running. 8 s is that 5 s plus about 3 s.
//
// A package var so tests can lower it; production never reassigns it.
var runWaitDelay = 8 * time.Second

// ParentDeathFDEnv names the environment variable that carries the file
// descriptor number of the parent-death pipe's read end (always 3: the first
// cmd.ExtraFiles entry). The pipe reads EOF when the pg-router process dies.
const ParentDeathFDEnv = "PG_ROUTER_PARENT_DEATH_FD"

type noParentDeathKey struct{}

// WithoutParentDeath returns a context under which OSRunner.Run passes no
// parent-death pipe: the handler's dispatch is meant to outlive the daemon
// (a survives_shutdown role, ADR 0085).
func WithoutParentDeath(ctx context.Context) context.Context {
	return context.WithValue(ctx, noParentDeathKey{}, true)
}

func parentDeathDisabled(ctx context.Context) bool {
	v, _ := ctx.Value(noParentDeathKey{}).(bool)
	return v
}

// run is the Runner call every subcommand shares: a survives_shutdown role's
// call carries no parent-death pipe.
func (c *Client) run(ctx context.Context, role roles.Role, argv []string, body []byte) ([]byte, int, error) {
	rctx := ctx
	if role.SurvivesShutdown {
		rctx = WithoutParentDeath(ctx)
	}
	return c.runner().Run(rctx, argv, body)
}

func (r OSRunner) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, int, error) {
	if len(argv) == 0 {
		return nil, 0, errors.New("wireclient: empty argv")
	}
	// Not routed through x/cmdrun, on purpose: the handler needs the parent-death
	// pipe as inherited fd 3 (cmd.ExtraFiles, below), which cmdrun.Call cannot
	// carry, and its stderr is forwarded live to r.Stderr rather than captured and
	// replayed after exit. The call is bounded by ctx (the dispatch deadline) and
	// by runWaitDelay after a cancel; the SIGTERM-on-cancel behaviour is pinned by
	// runner_cancel_integration_test.go.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	if r.Stderr != nil {
		cmd.Stderr = r.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}
	// A cancelled context sends the handler SIGTERM, not the SIGKILL that
	// exec.CommandContext sends by default: the command adapter's only cleanup is
	// its SIGTERM/SIGINT handler, which stops its payload's process group (the
	// payload runs in a group of its own, so it would otherwise outlive the
	// adapter). Only the adapter's pid is signalled, never a group: the handler
	// shares this process's group. WaitDelay then escalates to SIGKILL.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	// WaitDelay bounds how long Run waits, after the handler process exits or is
	// killed on the context's deadline, for the stdout copy to finish. Without it a
	// child the handler left running with the pipe open (a handler that hangs, or
	// forks and leaves) keeps cmd.Run blocked until that child exits, so a deadline
	// never arrives (docket pg2-12gr4.8, the validate call's "handler that hangs"
	// case).
	cmd.WaitDelay = runWaitDelay
	// Parent-death pipe: the handler gets the read end as fd 3 and its number in
	// ParentDeathFDEnv; this process keeps the write end open until the call
	// returns. If this process dies without a graceful cancel (SIGKILL, the
	// watchdog's hard exit, a crash) the kernel closes the write end and the
	// handler reads EOF, so a handler that wants to (the command adapter) can stop
	// its payload group. A survives_shutdown role opts out (WithoutParentDeath),
	// and an adapter that never reads the variable (ccpool) is unaffected.
	if !parentDeathDisabled(ctx) {
		pr, pw, perr := os.Pipe()
		if perr != nil {
			return nil, 0, fmt.Errorf("wireclient: parent-death pipe: %w", perr)
		}
		defer func() { _ = pw.Close() }()
		defer func() { _ = pr.Close() }()
		cmd.ExtraFiles = []*os.File{pr}
		cmd.Env = append(os.Environ(), ParentDeathFDEnv+"=3")
	}
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), 0, nil
	case errors.As(err, &exitErr):
		// The reply body (if any) is still on stdout even on a non-zero exit
		// (DEC-WIRE-1: "the rich outcome is in the JSON reply" — a degraded
		// participant MAY return an exit code only). Return it alongside the
		// exit code; Dispatch below decides what, if anything, it means.
		return out.Bytes(), exitErr.ExitCode(), nil
	default:
		return nil, 0, fmt.Errorf("wireclient: run %v: %w", argv, err)
	}
}

// CommandFor resolves the FULL argv — including subcommand — to invoke for
// role's own registered handler participant: a deployment concern (see this
// package's doc comment), never authored in roles.Role or resolved by this
// package on its own initiative.
//
// subcommand ("dispatch"/"postStartup"/"preShutdown"/"validate") is passed IN
// rather than appended by this package's own callers: a participant's own CLI
// expects the subcommand as its FIRST argument, with any flags after it — so a
// CommandFor that adds trailing tokens needs to know where the subcommand
// belongs in the argv it returns, not have it appended after those tokens where
// the participant's own arg parser would reject it as an unknown subcommand.
type CommandFor func(role roles.Role, subcommand string) ([]string, error)

// Client is the production HandlerClient.
type Client struct {
	// Runner defaults to OSRunner{} when nil.
	Runner Runner
	// Command resolves each dispatch's argv prefix. Required — Dispatch
	// errors immediately if it is nil, rather than panicking or guessing.
	Command CommandFor
}

// New returns a Client with the given command resolver and the production
// OSRunner.
func New(command CommandFor) *Client {
	return &Client{Runner: OSRunner{}, Command: command}
}

func (c *Client) runner() Runner {
	if c.Runner != nil {
		return c.Runner
	}
	return OSRunner{}
}

// SentRole is the role object core puts in every handler request for role, in
// the JSON shape it is sent in ({"name": ..., "config": {...}}), and false for a
// role built in code without handler (whose requests carry no role object). `pg-router config
// --show` prints this value, so what an operator reads is exactly what is sent.
func SentRole(role roles.Role) (json.RawMessage, bool) {
	ro := roleFor(role)
	if ro == nil {
		return nil, false
	}
	b, err := json.Marshal(ro)
	if err != nil {
		// Config came from a TOML decode, so it always marshals; keep the
		// signature total anyway.
		return nil, false
	}
	return b, true
}

// roleFor builds the request's role object, nil for a role built in code
// without handler.
func roleFor(role roles.Role) *participant.Role {
	if role.Handler == "" {
		return nil
	}
	cfg := role.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return &participant.Role{Name: role.Name, Config: cfg}
}

// SchemaVersion is the wire envelope version this client stamps on every
// request it builds (DEC-WIRE-1's "schemaVersion").
const SchemaVersion = status.SchemaVersion

// Dispatch implements HandlerClient: it sends handler.dispatch over the
// registered participant's own invoked command (DEC-WIRE-1's default CLI
// transport), using a freshly minted dsp-<...> tracking id — the SAME
// "dsp-" + 12-hex-chars-of-crypto/rand-entropy shape
// internal/eventqueue's Task 2.2 minting already uses (newDispatchID is
// unexported there, and this call's tracking id is not the same value as
// any queue-side Offering.ID passed in — see this package's doc comment) —
// and returns Reply UNMODIFIED: no interpretation of Outcome.
func (c *Client) Dispatch(ctx context.Context, role roles.Role, evt eventqueue.Event) (Reply, error) {
	if c.Command == nil {
		return Reply{}, errors.New("wireclient: no CommandFor configured")
	}
	argv, err := c.Command(role, "dispatch")
	if err != nil {
		return Reply{}, fmt.Errorf("wireclient: resolve command for role %q: %w", role.Name, err)
	}
	if len(argv) == 0 {
		return Reply{}, fmt.Errorf("wireclient: role %q resolved an empty command", role.Name)
	}
	id, err := newDispatchID()
	if err != nil {
		return Reply{}, fmt.Errorf("wireclient: mint dispatch id: %w", err)
	}
	req := participant.DispatchRequest{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Event: participant.DispatchEvent{
			ID:      evt.ID,
			Type:    evt.Type,
			Payload: evt.Payload,
		},
		Role: roleFor(role),
	}
	if !evt.ExpiresAt.IsZero() {
		req.Event.ExpiresAt = evt.ExpiresAt.Format(rfc3339)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Reply{}, fmt.Errorf("wireclient: encode dispatch request: %w", err)
	}

	stdout, code, err := c.run(ctx, role, argv, body)
	if err != nil {
		return Reply{}, err
	}
	switch code {
	case 0:
		var reply participant.DispatchReply
		if len(stdout) == 0 {
			return Reply{}, fmt.Errorf("wireclient: role %q: empty reply on exit 0", role.Name)
		}
		if err := json.Unmarshal(stdout, &reply); err != nil {
			return Reply{}, fmt.Errorf("wireclient: role %q: decode reply: %w", role.Name, err)
		}
		if reply.Error != "" {
			return Reply{}, fmt.Errorf("wireclient: role %q: %s", role.Name, reply.Error)
		}
		session, supervisors := reply.ReportedSupervision()
		return Reply{ID: reply.ID, Deferred: reply.Deferred, Outcome: reply.Outcome, Skipped: reply.Skipped, StartedAt: reportedStart(role.Name, reply), Session: session, Supervisors: supervisors}, nil
	case 9: // DEC-WIRE-1's "Coarse exit codes": 9 is busy.
		// The OPTIONAL busy-decline reply body (bead pg2-j4uwg): read
		// opportunistically, exactly the same "peek at stdout on a non-zero
		// exit" pattern the default branch below already uses for its own
		// Error field. An absent/empty/malformed body still declines with
		// every field "" — DEC-WIRE-1's "no body required" stays fully legal,
		// and an unknown or wrong-typed field never fails the decline.
		d := participant.DecodeDecline(stdout)
		return Reply{}, &BusyDecline{Reason: d.Reason, Kind: d.Kind, Detail: d.Detail}
	default:
		if len(stdout) > 0 {
			var reply participant.DispatchReply
			if err := json.Unmarshal(stdout, &reply); err == nil && reply.Error != "" {
				return Reply{}, &ExitError{Role: role.Name, Code: code, Detail: reply.Error}
			}
		}
		return Reply{}, &ExitError{Role: role.Name, Code: code}
	}
}

// reportedStart decodes a reply's optional startedAt (DEC-EVENT-5). A field that
// is not a JSON string holding an RFC 3339 instant is ignored with a warning,
// because it is an advisory field and a handler's slip in it must not fail, and
// so risk re-running, a dispatch that worked.
func reportedStart(role string, reply participant.DispatchReply) time.Time {
	t, err := reply.ReportedStart()
	if err != nil {
		slog.Warn("wireclient: ignoring a startedAt in a dispatch reply", "role", role, "startedAt", string(reply.StartedAt), "err", err)
		return time.Time{}
	}
	return t
}

// ExitError is Dispatch's error for a handler subprocess that exited with a
// non-zero, non-busy code (bead pg2-yu5y2). It exists so a caller can
// classify the failure structurally (Code -1 is how os/exec reports a child
// killed by a signal) instead of parsing the message; Error() renders the
// exact text Dispatch always produced for this case, so every existing
// string match is unaffected.
type ExitError struct {
	Role string
	// Code is the subprocess's exit code (-1 when it was killed by a signal).
	Code int
	// Detail is the reply body's own error text, "" when the handler gave none.
	Detail string
}

func (e *ExitError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("wireclient: role %q exited %d: %s", e.Role, e.Code, e.Detail)
	}
	return fmt.Sprintf("wireclient: role %q exited %d", e.Role, e.Code)
}

// PostStartup implements HandlerClient: it sends handler.postStartup to
// role's registered handler participant, once per process lifetime, right
// after bootCore succeeds (cmd/pg-router/run.go). Built for symmetry with
// PreShutdown (decision #2) — nothing consumes its outcome today.
func (c *Client) PostStartup(ctx context.Context, role roles.Role) (Reply, error) {
	return c.lifecycleCall(ctx, "postStartup", role)
}

// PreShutdown implements HandlerClient: it sends handler.preShutdown to
// role's registered handler participant, once per process lifetime, at the
// same point TeardownAll used to fire (cmd/pg-router/run.go) — a
// zero-behavior-change relocation of that sweep into the handler's own
// process, not a redesign.
func (c *Client) PreShutdown(ctx context.Context, role roles.Role) (Reply, error) {
	return c.lifecycleCall(ctx, "preShutdown", role)
}

// lifecycleCall is PostStartup/PreShutdown's shared body: mint a fresh
// tracking id, invoke <role's command> <subcommand> with a
// lifecycleRequest on stdin, and decode the lifecycleReply. There is no
// busy/exit-9 branch here (unlike Dispatch): no existing precedent covers a
// busy lifecycle hook, and with no caller-side retry (the run.go call sites
// log-and-continue on error) a busy signal would have nowhere to be
// honored — this packet's own decision, not something wire.md already
// dictates.
func (c *Client) lifecycleCall(ctx context.Context, subcommand string, role roles.Role) (Reply, error) {
	if c.Command == nil {
		return Reply{}, errors.New("wireclient: no CommandFor configured")
	}
	argv, err := c.Command(role, subcommand)
	if err != nil {
		return Reply{}, fmt.Errorf("wireclient: resolve command for role %q: %w", role.Name, err)
	}
	if len(argv) == 0 {
		return Reply{}, fmt.Errorf("wireclient: role %q resolved an empty command", role.Name)
	}
	id, err := newDispatchID()
	if err != nil {
		return Reply{}, fmt.Errorf("wireclient: mint %s id: %w", subcommand, err)
	}
	body, err := json.Marshal(participant.LifecycleRequest{SchemaVersion: SchemaVersion, ID: id, Role: roleFor(role)})
	if err != nil {
		return Reply{}, fmt.Errorf("wireclient: encode %s request: %w", subcommand, err)
	}

	stdout, code, err := c.run(ctx, role, argv, body)
	if err != nil {
		return Reply{}, err
	}
	if code != 0 {
		if len(stdout) > 0 {
			var reply participant.LifecycleReply
			if err := json.Unmarshal(stdout, &reply); err == nil && reply.Error != "" {
				return Reply{}, fmt.Errorf("wireclient: role %q %s exited %d: %s", role.Name, subcommand, code, reply.Error)
			}
		}
		return Reply{}, fmt.Errorf("wireclient: role %q %s exited %d", role.Name, subcommand, code)
	}
	if len(stdout) == 0 {
		return Reply{}, fmt.Errorf("wireclient: role %q: empty reply on exit 0", role.Name)
	}
	var reply participant.LifecycleReply
	if err := json.Unmarshal(stdout, &reply); err != nil {
		return Reply{}, fmt.Errorf("wireclient: role %q: decode %s reply: %w", role.Name, subcommand, err)
	}
	if reply.Error != "" {
		return Reply{}, fmt.Errorf("wireclient: role %q: %s", role.Name, reply.Error)
	}
	return Reply{ID: reply.ID, Outcome: reply.Outcome}, nil
}

// Validator is the OPTIONAL validate half of the handler contract (docket
// pg2-12gr4.8). It is a separate interface from HandlerClient so a client that
// does not implement it (a fake, an unwired handler) simply cannot be asked, which
// the caller reads like a handler without the subcommand: not supported.
type Validator interface {
	Validate(ctx context.Context, role roles.Role) (ValidateResult, error)
}

// ValidateResult is the decoded handler.validate-reply.
type ValidateResult struct {
	// Supported is false when the handler answered the usage exit (code 2): it has
	// no validate subcommand, which is not a failed validation.
	Supported bool
	// Problems fail validation; Warnings are reported but never fail it.
	Problems, Warnings []string
}

// validateReply mirrors schemas/handler.validate-reply.schema.json.
type validateReply struct {
	SchemaVersion string   `json:"schemaVersion"`
	Problems      []string `json:"problems"`
	Warnings      []string `json:"warnings"`
}

// Validate sends handler.validate to role's handler participant:
// `<handler> validate`, the lifecycle request on stdin.
// Exit 2 (usage) means the participant has no validate subcommand: Supported
// false, nil error. Exit 0 with a well-formed reply gives Supported true and the
// problem and warning lists (an empty problem list means valid). Anything else
// (another exit code, an unparsable or schema-invalid reply, or ctx's deadline)
// is an error: validation could not run, which is not a verdict. The caller sets
// the deadline; OSRunner's WaitDelay makes it arrive even when the handler's own
// children keep its stdout open.
func (c *Client) Validate(ctx context.Context, role roles.Role) (ValidateResult, error) {
	if c.Command == nil {
		return ValidateResult{}, errors.New("wireclient: no CommandFor configured")
	}
	argv, err := c.Command(role, "validate")
	if err != nil {
		return ValidateResult{}, fmt.Errorf("wireclient: resolve command for role %q: %w", role.Name, err)
	}
	if len(argv) == 0 {
		return ValidateResult{}, fmt.Errorf("wireclient: role %q resolved an empty command", role.Name)
	}
	id, err := newDispatchID()
	if err != nil {
		return ValidateResult{}, fmt.Errorf("wireclient: mint validate id: %w", err)
	}
	body, err := json.Marshal(participant.LifecycleRequest{SchemaVersion: SchemaVersion, ID: id, Role: roleFor(role)})
	if err != nil {
		return ValidateResult{}, fmt.Errorf("wireclient: encode validate request: %w", err)
	}
	stdout, code, err := c.run(ctx, role, argv, body)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ValidateResult{}, fmt.Errorf("wireclient: role %q validate: %w", role.Name, ctxErr)
	}
	if err != nil {
		return ValidateResult{}, err
	}
	switch code {
	case participant.ExitUsage:
		return ValidateResult{Supported: false}, nil
	case participant.ExitOK:
	default:
		return ValidateResult{}, fmt.Errorf("wireclient: role %q validate exited %d", role.Name, code)
	}
	if err := schemas.CheckBytes("handler.validate-reply", stdout); err != nil {
		return ValidateResult{}, fmt.Errorf("wireclient: role %q: validate reply: %w", role.Name, err)
	}
	var reply validateReply
	if err := json.Unmarshal(stdout, &reply); err != nil {
		return ValidateResult{}, fmt.Errorf("wireclient: role %q: decode validate reply: %w", role.Name, err)
	}
	return ValidateResult{Supported: true, Problems: reply.Problems, Warnings: reply.Warnings}, nil
}

const rfc3339 = "2006-01-02T15:04:05Z07:00"

// newDispatchID mints a fresh dispatch tracking id, matching
// internal/eventqueue's Task 2.2 "dsp-" + 12-hex-chars-of-crypto/rand-entropy
// shape byte-for-byte (that package's own newDispatchID is unexported, so
// this is the same pattern re-implemented here, not a shared call).
func newDispatchID() (string, error) {
	b := make([]byte, 6) // 6 bytes -> 12 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "dsp-" + hex.EncodeToString(b), nil
}
