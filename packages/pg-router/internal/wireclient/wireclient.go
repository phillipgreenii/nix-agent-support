// Package wireclient is pg-router's core-side client for INTF-HANDLER's
// `dispatch` message (docs/decisions/wire.md's DEC-WIRE-1: a CLI invocation
// carrying JSON, with coarse exit codes). It replaces the in-process
// executor.For(roleType).Dispatch(...) call docket pg2-oju6w's Task 5.2/5.3
// moved out of this module (that logic now lives in the registered handler
// participant, e.g. packages/pg-router-ccpool-handler) with a real message
// crossing the wire — resolving register row INTF-HANDLER's residual ("the
// call itself is still a Go method call, never a message crossing a wire",
// ADR 0065's "Register" section) for real.
//
// A handler participant's own INVOCABLE COMMAND — which binary to run, and
// with what launch-time arguments (--role-config/--config, matching
// packages/pg-router-ccpool-handler/cmd/pg-router-ccpool-handler/dispatch.go's
// own flags) — is deliberately NOT resolved here, and not carried by
// roles.Role either (Task 5.4 deletes Type/CCPoolConfig/CommandConfig from
// Role outright; internal/core's Registry documents that "the core reaches
// a participant by running its configured command" but holds no transport
// handle itself). dispatch.go's own doc comment defers that decision
// explicitly to this task ("it depends on how docket pg2-oju6w's Task 5.4
// wire client invokes this subcommand, which is that task's own call to
// make"): CommandFor is the injected seam a caller (the deployment/wiring
// layer, e.g. cmd/pg-router's bootCore once it is rewired by a sibling task)
// supplies to answer it, so this package never has to guess the answer for
// every possible deployment.
package wireclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
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
}

// ErrBusy is returned when the invoked participant's dispatch subcommand
// exits busy (code 9 — DEC-WIRE-1's "Coarse exit codes"). A caller (e.g.
// roleListener.Offer) maps this to a pre-accept eventqueue.DeclineBusy,
// exactly as the retired in-process executor.ErrBusy used to.
var ErrBusy = errors.New("wireclient: handler busy")

// Runner executes one dispatch subcommand invocation: argv (the handler's
// own CommandFor-resolved command, with "dispatch" appended) fed stdin,
// returning its stdout and coarse exit code. The production default
// (OSRunner) execs a real subprocess; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, argv []string, stdin []byte) (stdout []byte, exitCode int, err error)
}

// OSRunner is the production Runner: a real subprocess, matching DEC-WIRE-1's
// default transport (a CLI invocation carrying JSON on stdin/stdout).
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, int, error) {
	if len(argv) == 0 {
		return nil, 0, errors.New("wireclient: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
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

// CommandFor resolves the argv PREFIX (before "dispatch" is appended) to
// invoke for role's own registered handler participant — a deployment
// concern (see this package's doc comment), never authored in roles.Role or
// resolved by this package on its own initiative.
type CommandFor func(role roles.Role) ([]string, error)

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

// dispatchRequest / dispatchReply mirror
// packages/pg-router/schemas/handler.dispatch{,-reply}.schema.json —
// docs/decisions/wire.md's illustrative shapes, realized here as the
// concrete Go types this client encodes/decodes.
type dispatchRequest struct {
	SchemaVersion string        `json:"schemaVersion"`
	ID            string        `json:"id"`
	Event         dispatchEvent `json:"event"`
}

type dispatchEvent struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	ExpiresAt string         `json:"expiresAt,omitempty"`
	Payload   map[string]any `json:"payload"`
}

type dispatchReply struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Deferred      bool   `json:"deferred,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Error         string `json:"error,omitempty"`
}

// SchemaVersion is the wire envelope version this client stamps on every
// request it builds (DEC-WIRE-1's "schemaVersion").
const SchemaVersion = "1"

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
	argv, err := c.Command(role)
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
	req := dispatchRequest{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Event: dispatchEvent{
			ID:      evt.ID,
			Type:    evt.Type,
			Payload: evt.Payload,
		},
	}
	if !evt.ExpiresAt.IsZero() {
		req.Event.ExpiresAt = evt.ExpiresAt.Format(rfc3339)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Reply{}, fmt.Errorf("wireclient: encode dispatch request: %w", err)
	}

	stdout, code, err := c.runner().Run(ctx, append(argv, "dispatch"), body)
	if err != nil {
		return Reply{}, err
	}
	switch code {
	case 0:
		var reply dispatchReply
		if len(stdout) == 0 {
			return Reply{}, fmt.Errorf("wireclient: role %q: empty reply on exit 0", role.Name)
		}
		if err := json.Unmarshal(stdout, &reply); err != nil {
			return Reply{}, fmt.Errorf("wireclient: role %q: decode reply: %w", role.Name, err)
		}
		if reply.Error != "" {
			return Reply{}, fmt.Errorf("wireclient: role %q: %s", role.Name, reply.Error)
		}
		return Reply{ID: reply.ID, Deferred: reply.Deferred, Outcome: reply.Outcome}, nil
	case 9: // DEC-WIRE-1's "Coarse exit codes": 9 is busy.
		return Reply{}, ErrBusy
	default:
		if len(stdout) > 0 {
			var reply dispatchReply
			if err := json.Unmarshal(stdout, &reply); err == nil && reply.Error != "" {
				return Reply{}, fmt.Errorf("wireclient: role %q exited %d: %s", role.Name, code, reply.Error)
			}
		}
		return Reply{}, fmt.Errorf("wireclient: role %q exited %d", role.Name, code)
	}
}

// lifecycleRequest / lifecycleReply mirror
// packages/pg-router/schemas/handler.postStartup{,-reply}.schema.json and
// .../handler.preShutdown{,-reply}.schema.json — both hooks share one wire
// shape (no event, no deferred branch), so one pair of types serves both.
type lifecycleRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
}

type lifecycleReply struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Outcome       string `json:"outcome,omitempty"`
	Error         string `json:"error,omitempty"`
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
	argv, err := c.Command(role)
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
	body, err := json.Marshal(lifecycleRequest{SchemaVersion: SchemaVersion, ID: id})
	if err != nil {
		return Reply{}, fmt.Errorf("wireclient: encode %s request: %w", subcommand, err)
	}

	stdout, code, err := c.runner().Run(ctx, append(argv, subcommand), body)
	if err != nil {
		return Reply{}, err
	}
	if code != 0 {
		if len(stdout) > 0 {
			var reply lifecycleReply
			if err := json.Unmarshal(stdout, &reply); err == nil && reply.Error != "" {
				return Reply{}, fmt.Errorf("wireclient: role %q %s exited %d: %s", role.Name, subcommand, code, reply.Error)
			}
		}
		return Reply{}, fmt.Errorf("wireclient: role %q %s exited %d", role.Name, subcommand, code)
	}
	if len(stdout) == 0 {
		return Reply{}, fmt.Errorf("wireclient: role %q: empty reply on exit 0", role.Name)
	}
	var reply lifecycleReply
	if err := json.Unmarshal(stdout, &reply); err != nil {
		return Reply{}, fmt.Errorf("wireclient: role %q: decode %s reply: %w", role.Name, subcommand, err)
	}
	if reply.Error != "" {
		return Reply{}, fmt.Errorf("wireclient: role %q: %s", role.Name, reply.Error)
	}
	return Reply{ID: reply.ID, Outcome: reply.Outcome}, nil
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
