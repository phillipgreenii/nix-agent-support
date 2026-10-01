package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/schemas"
)

// This file is the operator CLI over the Gate Registry (bead pg2-h63eu):
//
//	pg-router gate set TYPE [--description D] [--owner O] [--ttl DUR]
//	pg-router gate clear (TYPE | --all) [--by WHO]
//	pg-router gate list [--json]
//	pg-router pause   [--description D] [--owner O]     (sugar: gate set SYSTEM_PAUSE)
//	pg-router resume  [--all] [--by WHO]                 (sugar: gate clear SYSTEM_PAUSE)
//
// A gate is what prevents pg-router from routing (see internal/eventqueue/
// gate.go for the semantics). Gates are records in the daemon's event log, so —
// unlike the file-backed gates they replaced — every one of these commands is a
// SOCKET CLIENT: it needs a running core and, like status and push-inject,
// never starts one (ADR 0036). With none running it fails with "no running
// core" (exit 1). A gate set while the daemon is down is therefore not
// possible; the caller retries once the daemon is up (the event log, not a
// file, is the single source of truth, which is what makes lease renewals and
// last-writer-wins race-free).
//
// The caller's identity (--owner on set/pause, --by on clear/resume) is DEBUG
// ONLY: it is recorded and shown, never enforced — ANY caller may clear any
// gate.

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

// runPause implements `pg-router pause`: gate set of SYSTEM_PAUSE.
func runPause(args []string) int {
	fs, socket, token := gateFlags("pause")
	desc := fs.String("description", "", "why the pool is paused (shown in status and the TUI)")
	owner := fs.String("owner", defaultGateCaller, "caller identity recorded as the gate's owner (debug only)")
	pos, code, ok := parseGateFlags(fs, "pause", args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(os.Stderr, "pause: unexpected argument %q — pause takes no gate name any more; it sets %s. Use `pg-router gate set TYPE` for another gate.\n", pos[0], eventqueue.GateSystemPause)
		return conformance.ExitUsage
	}
	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, core.SubcommandPause, err)
		return conformance.ExitError
	}
	return pauseCore(os.Stdout, os.Stderr, ref, *owner, *desc)
}

// pauseCore is runPause's testable body.
func pauseCore(stdout, stderr io.Writer, ref core.Ref, owner, description string) int {
	req := map[string]any{"schemaVersion": schemas.SchemaVersion, "owner": owner}
	if description != "" {
		req["description"] = description
	}
	var out struct {
		Gate    string `json:"gate"`
		SetAt   string `json:"setAt"`
		Renewal bool   `json:"renewal"`
	}
	if code := callGateVerb(stderr, ref, core.SubcommandPause, core.PauseReplySchema, req, &out); code != exitOK {
		return code
	}
	verb := "paused"
	if out.Renewal {
		verb = "pause re-asserted"
	}
	fmt.Fprintf(stdout, "pg-router: %s (%s since %s)\n", verb, textsafe.Sanitize(out.Gate), shortTime(out.SetAt))
	return exitOK
}

// runResume implements `pg-router resume [--all]`: gate clear of SYSTEM_PAUSE,
// or of every active gate with --all.
func runResume(args []string) int {
	fs, socket, token := gateFlags("resume")
	all := fs.Bool("all", false, "clear EVERY active gate, not just "+eventqueue.GateSystemPause)
	by := fs.String("by", defaultGateCaller, "caller identity recorded as the clearer (debug only)")
	pos, code, ok := parseGateFlags(fs, "resume", args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(os.Stderr, "resume: unexpected argument %q — resume takes no gate name any more; it clears %s (or everything with --all). Use `pg-router gate clear TYPE` for another gate.\n", pos[0], eventqueue.GateSystemPause)
		return conformance.ExitUsage
	}
	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, core.SubcommandResume, err)
		return conformance.ExitError
	}
	return resumeCore(os.Stdout, os.Stderr, ref, *all, *by)
}

// resumeCore is runResume's testable body.
func resumeCore(stdout, stderr io.Writer, ref core.Ref, all bool, by string) int {
	req := map[string]any{"schemaVersion": schemas.SchemaVersion, "by": by}
	if all {
		req["all"] = true
	}
	var out struct {
		Cleared []string `json:"cleared"`
	}
	if code := callGateVerb(stderr, ref, core.SubcommandResume, core.ResumeReplySchema, req, &out); code != exitOK {
		return code
	}
	switch {
	case len(out.Cleared) > 0:
		fmt.Fprintf(stdout, "pg-router: resumed (cleared %s)\n", textsafe.Sanitize(strings.Join(out.Cleared, ", ")))
	case all:
		fmt.Fprintln(stdout, "pg-router: already resumed (no gate was set)")
	default:
		fmt.Fprintf(stdout, "pg-router: already resumed (%s was not set)\n", eventqueue.GateSystemPause)
	}
	return exitOK
}

// runGate dispatches `pg-router gate (set|clear|list)`.
func runGate(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "gate: a subcommand is required (set, clear or list)")
		return conformance.ExitUsage
	}
	switch args[0] {
	case "set":
		return runGateSet(args[1:])
	case "clear":
		return runGateClear(args[1:])
	case "list":
		return runGateList(args[1:])
	}
	fmt.Fprintf(os.Stderr, "gate: unknown subcommand %q (want set, clear or list)\n", args[0])
	return conformance.ExitUsage
}

// runGateSet implements `gate set TYPE [--description D] [--owner O] [--ttl DUR]`.
func runGateSet(args []string) int {
	fs, socket, token := gateFlags("gate set")
	desc := fs.String("description", "", "why the gate is set (shown in status and the TUI)")
	owner := fs.String("owner", defaultGateCaller, "caller identity recorded as the gate's owner (debug only; overwritten on re-set)")
	ttl := fs.Duration("ttl", 0, "lease length (e.g. 6m); the owner renews it by setting the gate again. Omitted: the gate lasts until cleared")
	pos, code, ok := parseGateFlags(fs, "gate set", args)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "gate set: exactly one TYPE is required")
		return conformance.ExitUsage
	}
	if err := eventqueue.ValidateGateType(pos[0]); err != nil {
		fmt.Fprintln(os.Stderr, "gate set:", err)
		return conformance.ExitUsage
	}
	if *ttl < 0 {
		fmt.Fprintln(os.Stderr, "gate set: --ttl must not be negative")
		return conformance.ExitUsage
	}
	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, core.SubcommandGateSet, err)
		return conformance.ExitError
	}
	return gateSetCore(os.Stdout, os.Stderr, ref, pos[0], *desc, *owner, *ttl)
}

// gateSetCore is runGateSet's testable body.
func gateSetCore(stdout, stderr io.Writer, ref core.Ref, gateType, description, owner string, ttl time.Duration) int {
	req := map[string]any{"schemaVersion": schemas.SchemaVersion, "type": gateType}
	if description != "" {
		req["description"] = description
	}
	if owner != "" {
		req["owner"] = owner
	}
	if ttl > 0 {
		ms := ttl.Milliseconds()
		if ms < 1 {
			ms = 1
		}
		req["ttlMs"] = ms
	}
	var out struct {
		Type      string `json:"type"`
		SetAt     string `json:"setAt"`
		ExpiresAt string `json:"expiresAt"`
		Renewal   bool   `json:"renewal"`
	}
	if code := callGateVerb(stderr, ref, core.SubcommandGateSet, core.GateSetReplySchema, req, &out); code != exitOK {
		return code
	}
	verb := "set"
	if out.Renewal {
		verb = "renewed"
	}
	line := fmt.Sprintf("pg-router: gate %s %s (since %s", textsafe.Sanitize(out.Type), verb, shortTime(out.SetAt))
	if out.ExpiresAt != "" {
		line += ", lease until " + shortTime(out.ExpiresAt)
	}
	fmt.Fprintln(stdout, line+")")
	return exitOK
}

// runGateClear implements `gate clear (TYPE | --all) [--by WHO]`.
func runGateClear(args []string) int {
	fs, socket, token := gateFlags("gate clear")
	all := fs.Bool("all", false, "clear EVERY active gate")
	by := fs.String("by", defaultGateCaller, "caller identity recorded as the clearer (debug only)")
	pos, code, ok := parseGateFlags(fs, "gate clear", args)
	if !ok {
		return code
	}
	switch {
	case *all && len(pos) > 0:
		fmt.Fprintln(os.Stderr, "gate clear: --all takes no TYPE")
		return conformance.ExitUsage
	case !*all && len(pos) != 1:
		fmt.Fprintln(os.Stderr, "gate clear: exactly one TYPE (or --all) is required")
		return conformance.ExitUsage
	}
	if !*all {
		if err := eventqueue.ValidateGateType(pos[0]); err != nil {
			fmt.Fprintln(os.Stderr, "gate clear:", err)
			return conformance.ExitUsage
		}
	}
	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, core.SubcommandGateClear, err)
		return conformance.ExitError
	}
	gateType := ""
	if !*all {
		gateType = pos[0]
	}
	return gateClearCore(os.Stdout, os.Stderr, ref, gateType, *all, *by)
}

// gateClearCore is runGateClear's testable body.
func gateClearCore(stdout, stderr io.Writer, ref core.Ref, gateType string, all bool, by string) int {
	req := map[string]any{"schemaVersion": schemas.SchemaVersion}
	if all {
		req["all"] = true
	} else {
		req["type"] = gateType
	}
	if by != "" {
		req["by"] = by
	}
	var out struct {
		Cleared []string `json:"cleared"`
	}
	if code := callGateVerb(stderr, ref, core.SubcommandGateClear, core.GateClearReplySchema, req, &out); code != exitOK {
		return code
	}
	switch {
	case len(out.Cleared) > 0:
		fmt.Fprintf(stdout, "pg-router: gate cleared (%s)\n", textsafe.Sanitize(strings.Join(out.Cleared, ", ")))
	case all:
		fmt.Fprintln(stdout, "pg-router: no gate was set")
	default:
		fmt.Fprintf(stdout, "pg-router: gate %s was not set\n", textsafe.Sanitize(gateType))
	}
	return exitOK
}

// runGateList implements `gate list [--json]`: the active gates, read from the
// status verb.
func runGateList(args []string) int {
	fs, socket, token := gateFlags("gate list")
	asJSON := fs.Bool("json", false, "emit the gates as a JSON array")
	pos, code, ok := parseGateFlags(fs, "gate list", args)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintln(os.Stderr, "gate list: unexpected argument:", pos[0])
		return conformance.ExitUsage
	}
	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, "gate list", err)
		return conformance.ExitError
	}
	return gateListCore(os.Stdout, os.Stderr, ref, *asJSON)
}

// gateListCore is runGateList's testable body.
func gateListCore(stdout, stderr io.Writer, ref core.Ref, asJSON bool) int {
	var st struct {
		Gates []statusGate `json:"gates"`
	}
	req := map[string]any{"schemaVersion": schemas.SchemaVersion}
	if code := callGateVerb(stderr, ref, core.SubcommandStatus, core.StatusReplySchema, req, &st); code != exitOK {
		return code
	}
	if asJSON {
		gates := st.Gates
		if gates == nil {
			gates = []statusGate{}
		}
		b, err := json.Marshal(gates)
		if err != nil { // unreachable: plain strings and an int
			fmt.Fprintln(stderr, "gate list:", err)
			return exitGeneric
		}
		fmt.Fprintln(stdout, string(b))
		return exitOK
	}
	renderGates(stdout, st.Gates)
	return exitOK
}

// statusGate is one entry of the status reply's `gates` array, as a CONSUMER
// reads it (schemas/cli.status-reply.schema.json).
type statusGate struct {
	Type           string `json:"type"`
	Description    string `json:"description,omitempty"`
	Owner          string `json:"owner,omitempty"`
	SetAt          string `json:"setAt"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	TTLRemainingMs int64  `json:"ttlRemainingMs,omitempty"`
}

// renderGates writes the human view of the active gates: one line per gate,
// sorted by TYPE, with an explicit "(none)" when nothing is gated.
func renderGates(w io.Writer, gates []statusGate) {
	if len(gates) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	sorted := append([]statusGate(nil), gates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Type < sorted[j].Type })
	for _, g := range sorted {
		line := "  " + textsafe.Sanitize(g.Type) + ": setAt=" + shortTime(g.SetAt)
		if g.Owner != "" {
			line += " owner=" + textsafe.Sanitize(g.Owner)
		}
		if g.ExpiresAt != "" {
			line += fmt.Sprintf(" expiresAt=%s ttlRemaining=%s", shortTime(g.ExpiresAt),
				(time.Duration(g.TTLRemainingMs) * time.Millisecond).Round(time.Second))
		}
		if g.Description != "" {
			line += " description=" + fmt.Sprintf("%q", textsafe.Sanitize(g.Description))
		}
		fmt.Fprintln(w, line)
	}
}

// callGateVerb performs one socket call: it dials ref, sends req as the verb's
// JSON payload, discriminates the reply against replySchema and decodes it into
// out. It returns exitOK, or the exit code to report (a diagnostic already
// written to stderr).
func callGateVerb(stderr io.Writer, ref core.Ref, verb, replySchema string, req map[string]any, out any) int {
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
	reply, code, err := client.Call(context.Background(), verb, payload, core.CallOptions{})
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
