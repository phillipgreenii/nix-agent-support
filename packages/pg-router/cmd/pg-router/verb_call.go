package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
)

// This file holds the helpers the socket-client commands share (status, doctor,
// log compact and the pause / resume and level verbs): the connection flags, the
// flag parser, one socket call. They
// lived in the removed gate commands file and moved here unchanged.

// defaultGateCaller is recorded as the owner/clearer when --owner/--by is
// omitted: the operator at a terminal.
const defaultGateCaller = "operator"

// gateFlags registers the flags every gate verb shares.
func gateFlags(name string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	socket := fs.String("socket", "", "path to the running core's socket (overrides discovery)")
	token := fs.String("token", "", "auth token for the running core (with --socket)")
	return fs, socket, token
}

// parseGateFlags parses args allowing flags anywhere, rendering a usage
// diagnostic itself. ok is false when the caller should return code.
func parseGateFlags(fs *flag.FlagSet, name string, args []string) (pos []string, code int, ok bool) {
	pos, err := parseInterspersed(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println(helpText)
		return nil, exitOK, false
	case err != nil:
		fmt.Fprintln(os.Stderr, name+":", err)
		return nil, conformance.ExitUsage, false
	}
	return pos, exitOK, true
}

// callGateVerb performs one socket call: it dials ref, sends req as the verb's
// JSON payload, discriminates the reply against replySchema and decodes it into
// out. It returns exitOK, or the exit code to report (a diagnostic already
// written to stderr).
func callGateVerb(stderr io.Writer, ref core.Ref, verb, replySchema string, req map[string]any, out any) int {
	return callVerb(stderr, ref, verb, replySchema, req, out, core.CallOptions{})
}

// callVerb is callGateVerb with the call options chosen by the caller: a verb that
// does real file work (log-compact) needs a longer timeout than the 5 s default.
func callVerb(stderr io.Writer, ref core.Ref, verb, replySchema string, req map[string]any, out any, opts core.CallOptions) int {
	payload, err := json.Marshal(req)
	if err != nil { // unreachable: the request holds only JSON-safe scalars
		fmt.Fprintf(stderr, "%s: build request: %v\n", verb, err)
		return exitGeneric
	}
	client, err := core.Dial(ref, core.DefaultProbeTimeout)
	if err != nil {
		reportNoCore(stderr, verb, err)
		return conformance.ExitError
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(context.Background(), verb, payload, opts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", verb, err)
		return conformance.ExitError
	}
	if code == conformance.ExitBusy {
		fmt.Fprintf(stderr, "%s: %s\n", verb, busyRefusalMessage(reply))
		return code
	}
	if diagErr := core.DiscriminateReply(reply, replySchema, out); diagErr != nil {
		fmt.Fprintf(stderr, "%s: %v\n", verb, diagErr)
		return conformance.ExitError
	}
	if code != conformance.ExitOK {
		fmt.Fprintf(stderr, "%s: core exited %d\n", verb, code)
		return code
	}
	return exitOK
}

// shortTime renders an RFC3339 instant as local HH:MM:SS for operator output,
// falling back to the raw string (or "-") when it does not parse.
func shortTime(rfc string) string {
	if rfc == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, rfc)
	if err != nil {
		return textsafe.Sanitize(rfc)
	}
	return t.Local().Format("15:04:05")
}
