package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/schemas"
)

// `pg-router explain <role>`: the current admission decision of one role.
//
// Exported names: none (package main). Names: runExplain, explain.
//
// It reads the running core's status reply (the same pausedRoles the status text
// prints), so it needs no verb of its own. A paused role prints the paused-role
// line; a role no requirement blocks prints that it is not paused; a name the
// core does not know exits 1 with "unknown role <name>". It never starts a core.

func runExplain(args []string) int {
	fs, socket, token := gateFlags("explain")
	pos, code, ok := parseGateFlags(fs, "explain", args)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "explain: want exactly one <role>")
		return conformance.ExitUsage
	}
	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, "explain", err)
		return conformance.ExitError
	}
	return explain(os.Stdout, os.Stderr, pos[0], ref, time.Now())
}

// explain is runExplain's testable body over an already-resolved core ref.
func explain(stdout, stderr io.Writer, role string, ref core.Ref, now time.Time) int {
	client, err := core.Dial(ref, core.DefaultProbeTimeout)
	if err != nil {
		reportNoCore(stderr, "explain", err)
		return conformance.ExitError
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(context.Background(), core.SubcommandStatus, []byte(`{"schemaVersion":"`+schemas.SchemaVersion+`"}`), core.CallOptions{})
	if err != nil {
		fmt.Fprintf(stderr, "explain: %v\n", err)
		return conformance.ExitError
	}
	if code == conformance.ExitBusy {
		fmt.Fprintf(stderr, "explain: %s\n", busyRefusalMessage(reply))
		return code
	}
	var st statusReply
	if diag := core.DiscriminateReply(reply, core.StatusReplySchema, &st); diag != nil {
		fmt.Fprintf(stderr, "explain: %v\n", diag)
		return conformance.ExitError
	}
	return explainRole(stdout, stderr, role, st, now)
}

// explainRole renders the decision of role from a status reply.
func explainRole(stdout, stderr io.Writer, role string, st statusReply, now time.Time) int {
	for _, p := range st.PausedRoles {
		if p.Role == role {
			fmt.Fprintln(stdout, pausedRoleLine(p, now))
			return exitOK
		}
	}
	for _, l := range st.Listeners {
		if l.Role != role {
			continue
		}
		switch {
		case !l.Enabled:
			fmt.Fprintf(stdout, "%s is not paused by levels (the role is disabled)\n", textsafe.Sanitize(role))
		case l.Excluded:
			fmt.Fprintf(stdout, "%s is not paused by levels (the role is excluded from this run)\n", textsafe.Sanitize(role))
		default:
			fmt.Fprintf(stdout, "%s is not paused by levels\n", textsafe.Sanitize(role))
		}
		return exitOK
	}
	fmt.Fprintf(stderr, "unknown role %s\n", textsafe.Sanitize(role))
	return conformance.ExitError
}
