package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// envSocket/envToken name the SAME environment variables pg-router's own
// cmd/pg-router (cmd/pg-router/ingest_event.go) uses for an injected core
// reference — this process dials pg-router's core, so it names the core's
// socket the same way pg-router's own CLI does, not a module-private
// convention.
const (
	envSocket = "PG_ROUTER_SOCKET"
	envToken  = "PG_ROUTER_TOKEN"
)

// coreRef identifies a running pg-router core to dial: the socket path and
// the auth token DEC-WIRE-2 says arrives with a core-issued callback command
// already baked in. This module never assembles or discovers a socket on
// its own initiative (a handler is always handed one) — see resolveRef.
type coreRef struct {
	Socket string
	Token  string
}

// resolveRef resolves the core to dial from --socket/--token, falling back
// to the PG_ROUTER_SOCKET/PG_ROUTER_TOKEN env vars pg-router's own CLI
// callbacks use (mirrors cmd/pg-router/push_inject.go's injectedRef). Unlike
// pg-router's own CLI, this module does NOT also fall back to discovering a
// core via its on-disk record: every caller of register/self-status is a
// core-issued callback or an explicitly-injected launch argument, per
// DEC-WIRE-2's "the core hands the participant this callback command with
// its address and token already baked in" — there is no operator-invoked
// path here that would need LogDir-style discovery.
func resolveRef(socket, token string) (coreRef, error) {
	if socket == "" {
		socket = os.Getenv(envSocket)
		if socket != "" && token == "" {
			token = os.Getenv(envToken)
		}
	}
	if socket == "" {
		return coreRef{}, errors.New("no core socket given (--socket, or " + envSocket + ")")
	}
	return coreRef{Socket: socket, Token: token}, nil
}

// wireRequest/wireResponse mirror packages/pg-router/internal/core (package
// core, unexported types of the same name) BYTE-FOR-BYTE on the wire — that
// package is unreachable from here (Go's internal-package visibility rule,
// docs/adr/0065's Addendum), so this is a from-scratch client speaking the
// SAME transport framing, read from its source rather than imported, so a
// real pg-router core (once docket pg2-oju6w's Task 5.4 lands a listener)
// understands this client without either side changing. DEC-WIRE-1/-2 name
// this a transport-contract choice, not a message-schema one, and leave the
// concrete socket framing to the implementation (DEC-WIRE-2's "Not decided
// here").
type wireRequest struct {
	Token      string          `json:"token"`
	Subcommand string          `json:"subcommand"`
	Payload    json.RawMessage `json:"payload"`
}

type wireResponse struct {
	ExitCode int             `json:"exitCode"`
	Reply    json.RawMessage `json:"reply"`
}

// dialTimeout bounds the connect attempt to pg-router's core socket — a
// local unix-domain socket connect is effectively instantaneous, so this
// stays short rather than pinning a caller on a dead/absent core (mirrors
// packages/pg-router/internal/core.DefaultProbeTimeout's own rationale).
const dialTimeout = 1 * time.Second

// callTimeout bounds one full request/reply round trip once connected
// (mirrors packages/pg-router/internal/core.DefaultCallTimeout).
const callTimeout = 5 * time.Second

// callCore sends one subcommand request to ref over the socket and returns
// the reply body plus the core's own coarse exit code — the client half of
// the same wire framing pg-router's core (internal/core.Client.Call) speaks.
func callCore(ref coreRef, subcommand string, payload []byte) ([]byte, int, error) {
	conn, err := net.DialTimeout("unix", ref.Socket, dialTimeout)
	if err != nil {
		return nil, 0, fmt.Errorf("dial core socket %s: %w", ref.Socket, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(callTimeout)); err != nil {
		return nil, 0, fmt.Errorf("set deadline: %w", err)
	}
	body := json.RawMessage(payload)
	if len(body) == 0 {
		body = json.RawMessage("null")
	}
	req := wireRequest{Token: ref.Token, Subcommand: subcommand, Payload: body}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, 0, fmt.Errorf("send %s request: %w", subcommand, err)
	}
	var resp wireResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, 0, fmt.Errorf("read %s reply: %w", subcommand, err)
	}
	if string(resp.Reply) == "null" {
		return nil, resp.ExitCode, nil
	}
	return resp.Reply, resp.ExitCode, nil
}

// runRegister implements the `register` subcommand (interfaces.md's
// "Lifecycle"; DEC-WIRE-1's illustrative register message): it sends
// {schemaVersion, id, kind: "handler", self} to the core over the injected
// socket at process start, and relays the core's cli.register-reply (or
// protocol-level cli.error) plus its coarse exit code.
func runRegister(args []string) int {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "path to pg-router's core socket (or "+envSocket+")")
	token := fs.String("token", "", "auth token for pg-router's core (or "+envToken+")")
	id := fs.String("id", "", "this participant's own chosen registration id (required)")
	self := fs.String("self", "healthy", "this participant's own health: healthy|degraded|unavailable")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "register:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "register: unexpected argument:", fs.Arg(0))
		return conformance.ExitUsage
	}
	if *id == "" {
		fmt.Fprintln(os.Stderr, "register: --id is required")
		return conformance.ExitUsage
	}
	ref, err := resolveRef(*socket, *token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "register:", err)
		return conformance.ExitError
	}
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            *id,
		"kind":          "handler",
		"self":          *self,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "register: build request:", err)
		return conformance.ExitError
	}
	return relay(os.Stdout, os.Stderr, ref, "register", payload)
}

// runSelfStatus implements the `self-status` INTF-CLI callback subcommand,
// common to every participant kind (interfaces.md's "Self-status"). It
// reads the cli.self-status request as JSON on stdin — the core hands EVERY
// registered participant this command with --socket/--token already baked
// in, so this process never composes the request itself, mirroring
// cmd/pg-router/self_status.go's own reference implementation exactly.
func runSelfStatus(args []string) int {
	fs := flag.NewFlagSet("self-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "path to pg-router's core socket (or "+envSocket+")")
	token := fs.String("token", "", "auth token for pg-router's core (or "+envToken+")")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "self-status:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "self-status: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "self-status takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}
	ref, err := resolveRef(*socket, *token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "self-status:", err)
		return conformance.ExitError
	}
	request, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "self-status: read request from stdin:", err)
		return conformance.ExitError
	}
	return relay(os.Stdout, os.Stderr, ref, "self-status", request)
}

// relay sends payload to ref over subcommand and writes the core's reply
// verbatim to stdout, returning the core's own coarse exit code — the
// manager-callback wire contract (interfaces.md), unaltered.
func relay(stdout, stderr io.Writer, ref coreRef, subcommand string, payload []byte) int {
	reply, code, err := callCore(ref, subcommand, payload)
	if err != nil {
		fmt.Fprintln(stderr, subcommand+": no running core:", err)
		return conformance.ExitError
	}
	if len(reply) > 0 {
		_, _ = stdout.Write(reply)
	}
	return code
}
