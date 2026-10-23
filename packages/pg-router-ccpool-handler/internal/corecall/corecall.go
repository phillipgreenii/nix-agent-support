// Package corecall is the OUTBOUND client of pg-router's core socket
// (DEC-WIRE-2): the register/self-status transport shared by every adapter
// binary of this module (bead pg2-12gr4.10.3). Moved verbatim out of
// package main of cmd/pg-router-ccpool-handler; no behavior change.
package corecall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

// EnvSocket/EnvToken name the SAME environment variables pg-router's own
// cmd/pg-router (cmd/pg-router/ingest_event.go) uses for an injected core
// reference — this process dials pg-router's core, so it names the core's
// socket the same way pg-router's own CLI does, not a module-private
// convention.
const (
	EnvSocket = "PG_ROUTER_SOCKET"
	EnvToken  = "PG_ROUTER_TOKEN"
)

// Ref identifies a running pg-router core to dial: the socket path and
// the auth token DEC-WIRE-2 says arrives with a core-issued callback command
// already baked in. This module never assembles or discovers a socket on
// its own initiative (a handler is always handed one) — see ResolveRef.
type Ref struct {
	Socket string
	Token  string
}

// ResolveRef resolves the core to dial from --socket/--token, falling back
// to the PG_ROUTER_SOCKET/PG_ROUTER_TOKEN env vars pg-router's own CLI
// callbacks use (mirrors cmd/pg-router/push_inject.go's injectedRef). Unlike
// pg-router's own CLI, this module does NOT also fall back to discovering a
// core via its on-disk record: every caller of register/self-status is a
// core-issued callback or an explicitly-injected launch argument, per
// DEC-WIRE-2's "the core hands the participant this callback command with
// its address and token already baked in" — there is no operator-invoked
// path here that would need LogDir-style discovery.
func ResolveRef(socket, token string) (Ref, error) {
	if socket == "" {
		socket = os.Getenv(EnvSocket)
		if socket != "" && token == "" {
			token = os.Getenv(EnvToken)
		}
	}
	if socket == "" {
		return Ref{}, errors.New("no core socket given (--socket, or " + EnvSocket + ")")
	}
	return Ref{Socket: socket, Token: token}, nil
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

// Call sends one subcommand request to ref over the socket and returns
// the reply body plus the core's own coarse exit code — the client half of
// the same wire framing pg-router's core (internal/core.Client.Call) speaks.
func Call(ref Ref, subcommand string, payload []byte) ([]byte, int, error) {
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

// Relay sends payload to ref over subcommand and writes the core's reply
// verbatim to stdout, returning the core's own coarse exit code — the
// manager-callback wire contract (interfaces.md), unaltered.
func Relay(stdout, stderr io.Writer, ref Ref, subcommand string, payload []byte) int {
	reply, code, err := Call(ref, subcommand, payload)
	if err != nil {
		fmt.Fprintln(stderr, subcommand+": no running core:", err)
		return conformance.ExitError
	}
	if len(reply) > 0 {
		_, _ = stdout.Write(reply)
	}
	return code
}
