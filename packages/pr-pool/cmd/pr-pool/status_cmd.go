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

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/textsafe"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// runStatus implements `pr-pool status [--json]` (Task 3.8): the operator
// front door onto the INTF-CLI `status` verb — resolved configuration, live
// deliveries, and per-type queue depths (register row bead pg2-xa44k), plus
// the additive field set the wire reply now carries.
//
// It sends NO `since` — that request field is a long-poll affordance for
// Task 4.0's TUI only (Task 3.8 Binding decisions, Step 6); this subcommand
// always asks for the ring's own default window.
//
// EXIT CODES follow every other operator subcommand: 0 ok, 2 usage, 1
// anything else — BUSY sits at 9 and never collides with usage (ADR 0042's
// Decision). With no core running it FAILS with a "no running core"
// diagnostic and the remedy; it never starts one (ADR 0036).
func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	asJSON := fs.Bool("json", false, "emit JSON instead of human-readable text")
	socket := fs.String("socket", "", "path to the running core's socket (overrides discovery)")
	token := fs.String("token", "", "auth token for the running core (with --socket)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println(helpText)
		return exitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "status:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "status: unexpected argument:", fs.Arg(0))
		return conformance.ExitUsage
	}

	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, core.SubcommandStatus, err)
		return conformance.ExitError
	}
	return status(os.Stdout, os.Stderr, *asJSON, ref)
}

// status runs the status call against an already-resolved core ref, so the
// outcome rendering is testable without the process's real stdout/stderr or
// flags.
func status(stdout, stderr io.Writer, asJSON bool, ref core.Ref) int {
	client, err := core.Dial(ref, core.DefaultProbeTimeout)
	if err != nil {
		reportNoCore(stderr, core.SubcommandStatus, err)
		return conformance.ExitError
	}
	defer func() { _ = client.Close() }()

	request := []byte(`{"schemaVersion":"` + schemas.SchemaVersion + `"}`)
	reply, code, err := client.Call(context.Background(), core.SubcommandStatus, request, core.CallOptions{})
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return conformance.ExitError
	}
	if code == conformance.ExitBusy {
		// The core's own admission-control refusal (Task 3.10): a saturated
		// read semaphore declines a status/mon.read call immediately rather
		// than blocking. Unlike a participant's own pre-accept busy decline
		// (a body-less reply), this refusal always carries a human-readable
		// cli.error envelope — render it and preserve the wire's own exit
		// code, rather than falling into discriminateReply's generic
		// "core refused" -> exit 1 mapping below, which would otherwise
		// swallow this call's true exit 9.
		fmt.Fprintf(stderr, "status: %s\n", busyRefusalMessage(reply))
		return code
	}

	var st statusReply
	if diagErr := discriminateReply(reply, core.StatusReplySchema, &st); diagErr != nil {
		fmt.Fprintf(stderr, "status: %v\n", diagErr)
		return conformance.ExitError
	}
	if asJSON {
		fmt.Fprintln(stdout, string(reply))
	} else {
		renderStatusText(stdout, ref.Socket, st)
	}
	return code
}

// busyRefusalMessage extracts the human-readable text from the core's
// cli.error envelope on an exit-9 admission refusal (Task 3.10 Binding
// decisions, Step 5), falling back to a generic message if the reply is
// somehow empty or not that shape — defensive; the core always sends the
// envelope for this refusal.
func busyRefusalMessage(reply []byte) string {
	const fallback = "too many concurrent status/mon.read calls in flight; retry"
	if len(reply) == 0 {
		return fallback
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(reply, &errBody); err != nil || errBody.Error == "" {
		return fallback
	}
	return errBody.Error
}

// statusReply is the cli.status-reply shape, as a CONSUMER (the human
// renderer) reads it — mirrors internal/core's composeStatusReply output
// (schemas/cli.status-reply.schema.json).
type statusReply struct {
	Deliveries []struct {
		ID      string `json:"id"`
		Handler string `json:"handler"`
		Event   string `json:"event"`
	} `json:"deliveries"`
	Queues []struct {
		Type  string `json:"type"`
		Depth int    `json:"depth"`
	} `json:"queues"`
	Core *struct {
		State      string `json:"state"`
		Version    string `json:"version"`
		PID        int    `json:"pid"`
		StartedAt  string `json:"startedAt"`
		ConfigPath string `json:"configPath"`
	} `json:"core"`
	Mode           string `json:"mode"`
	ResolvedConfig *struct {
		RepoRoot       string `json:"repoRoot"`
		BeadsPrefix    string `json:"beadsPrefix"`
		PollIntervalMs int    `json:"pollIntervalMs"`
		ActiveRoles    int    `json:"activeRoles"`
		ActiveQueries  int    `json:"activeQueries"`
	} `json:"resolvedConfig"`
	Gates []struct {
		Name  string `json:"name"`
		Set   bool   `json:"set"`
		Mtime string `json:"mtime"`
		Owner string `json:"owner"`
	} `json:"gates"`
	GatesObservedAt string             `json:"gatesObservedAt"`
	Listeners       []registrationView `json:"listeners"`
	Sources         []struct {
		Name     string `json:"name"`
		Rejected int    `json:"rejected"`
	} `json:"sources"`
	UnmatchedBindings []string `json:"unmatchedBindings"`
	Activity          []struct {
		Seq       uint64 `json:"seq"`
		StartedAt string `json:"startedAt"`
		Type      string `json:"type"`
		Outcome   string `json:"outcome"`
	} `json:"activity"`
	// ActivityDropped reports whether entries strictly between the request's
	// `since` cursor and what the ring now retains were already evicted
	// before this read (internal/activity.Ring.Read's own doc; bead
	// pg2-vtuou). This subcommand never sends a nonzero `since` (see
	// runStatus's doc), so it is always false through this CLI today — the
	// field exists for a future since-cursor caller (Task 4.0's TUI) and for
	// parity with the wire contract.
	ActivityDropped bool   `json:"activityDropped"`
	LastTickAt      string `json:"lastTickAt"`
	TickIntervalMs  int    `json:"tickIntervalMs"`
}

type registrationView struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Self  string `json:"self"`
}

// activityRenderLimit is the human-form "ACTIVITY (last 10)" cap (Task 3.8
// Binding decisions, the operator's 2026-09-01 ruling) — the wire reply
// itself carries the ring's own, larger default window; this is a rendering
// choice of the human-output path only, layered on top of the same read.
const activityRenderLimit = 10

// renderStatusText writes the human-readable status view: header
// (core/socket/config/gates/mode), then QUEUES, DELIVERIES (live), ACTIVITY
// (last 10), LISTENERS, SOURCES, UNMATCHED BINDINGS — the operator's
// 2026-09-01 ruling reordering the frozen text for incident scanning (Task
// 3.8 Binding decisions, superseding both "Flagged for operator"
// questions). No box drawing. Every section other than gates/QUEUES
// (rendered unconditionally) prints an explicit empty marker instead of
// vanishing when it has nothing to say, so "nothing configured" reads
// differently from "the composer silently failed to enumerate this". Every
// rendered dynamic string is routed through textsafe.Sanitize (Task 3.7)
// before it is written; "-" stands in for an absent individual field.
func renderStatusText(w io.Writer, socket string, st statusReply) {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return textsafe.Sanitize(s)
	}

	fmt.Fprintf(w, "socket: %s\n", dash(socket))
	if st.Core != nil {
		fmt.Fprintf(w, "core: state=%s version=%s pid=%d startedAt=%s configPath=%s\n",
			dash(st.Core.State), dash(st.Core.Version), st.Core.PID, dash(st.Core.StartedAt), dash(st.Core.ConfigPath))
	} else {
		fmt.Fprintln(w, "core: -")
	}
	fmt.Fprintf(w, "mode: %s\n", dash(st.Mode))
	if rc := st.ResolvedConfig; rc != nil {
		poll := "-"
		if rc.PollIntervalMs > 0 {
			poll = fmt.Sprintf("%dms", rc.PollIntervalMs)
		}
		fmt.Fprintf(w, "config: repoRoot=%s beadsPrefix=%s pollInterval=%s activeRoles=%d activeQueries=%d\n",
			dash(rc.RepoRoot), dash(rc.BeadsPrefix), poll, rc.ActiveRoles, rc.ActiveQueries)
	} else {
		fmt.Fprintln(w, "config: -")
	}

	staleSuffix := ""
	if gatesAreStale(st) {
		staleSuffix = " (stale — pending next tick)"
	}
	fmt.Fprintf(w, "GATES (observedAt=%s%s):\n", dash(st.GatesObservedAt), staleSuffix)
	if len(st.Gates) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, g := range st.Gates {
		line := fmt.Sprintf("  %s: set=%t", dash(g.Name), g.Set)
		if g.Mtime != "" {
			line += " mtime=" + dash(g.Mtime)
		}
		if g.Owner != "" {
			line += " owner=" + dash(g.Owner)
		}
		fmt.Fprintln(w, line)
	}

	fmt.Fprintln(w, "QUEUES:")
	if len(st.Queues) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, q := range st.Queues {
		fmt.Fprintf(w, "  %s: depth=%d\n", dash(q.Type), q.Depth)
	}

	renderSection(w, "DELIVERIES (live)", len(st.Deliveries), func() {
		for _, d := range st.Deliveries {
			fmt.Fprintf(w, "  %s: handler=%s event=%s\n", dash(d.ID), dash(d.Handler), dash(d.Event))
		}
	})

	activityHeader := "ACTIVITY (last 10)"
	if st.ActivityDropped {
		activityHeader += " (dropped: entries evicted since your last read)"
	}
	renderSection(w, activityHeader, len(st.Activity), func() {
		start := 0
		if n := len(st.Activity); n > activityRenderLimit {
			start = n - activityRenderLimit
		}
		for _, a := range st.Activity[start:] {
			fmt.Fprintf(w, "  #%d %s: %s\n", a.Seq, dash(a.Type), dash(a.Outcome))
		}
	})

	renderSection(w, "LISTENERS", len(st.Listeners), func() {
		for _, l := range st.Listeners {
			fmt.Fprintf(w, "  %s: state=%s self=%s\n", dash(l.ID), dash(l.State), dash(l.Self))
		}
	})

	renderSection(w, "SOURCES", len(st.Sources), func() {
		for _, s := range st.Sources {
			fmt.Fprintf(w, "  %s: rejected=%d\n", dash(s.Name), s.Rejected)
		}
	})

	renderSection(w, "UNMATCHED BINDINGS", len(st.UnmatchedBindings), func() {
		for _, b := range st.UnmatchedBindings {
			fmt.Fprintf(w, "  %s\n", dash(b))
		}
	})
}

// renderSection prints "NAME:" then either "  (none)" or body() — the
// explicit-empty-marker half of the operator's 2026-09-01 ruling: every
// section other than gates/QUEUES (rendered unconditionally in
// renderStatusText) is silent about NOTHING, never silent by omission.
func renderSection(w io.Writer, name string, n int, body func()) {
	fmt.Fprintln(w, name+":")
	if n == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	body()
}

// gatesAreStale reports whether the reply's ONE gatesObservedAt timestamp
// predates lastTickAt by more than one tick interval (Task 3.8 Binding
// decisions, Step 9) — the signal that the drive loop has ticked at least
// once since the gate cell was last refreshed, so the rendered gate state
// may be behind. A run-until-idle pass or the boot window (no tickIntervalMs
// at all) never reports stale: there is no periodic tick to be behind.
func gatesAreStale(st statusReply) bool {
	if st.GatesObservedAt == "" || st.LastTickAt == "" || st.TickIntervalMs <= 0 {
		return false
	}
	observedAt, err1 := time.Parse(time.RFC3339Nano, st.GatesObservedAt)
	lastTick, err2 := time.Parse(time.RFC3339Nano, st.LastTickAt)
	if err1 != nil || err2 != nil {
		return false
	}
	return lastTick.Sub(observedAt) > time.Duration(st.TickIntervalMs)*time.Millisecond
}
