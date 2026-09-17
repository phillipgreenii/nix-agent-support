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

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// itemFromPayload reconstructs an item.Item from a dispatch event's opaque
// payload object — this module's own local replacement for
// packages/pg-router/internal/discover.ItemFromPayload, which is
// unreachable from here (Go's internal-package visibility rule) and, as of
// docket pg2-oju6w's Task 5.6, deleted entirely: the core-side
// discover.ToQueueEvent now writes the item's fields directly at Payload's
// top level (no wrapping key), and this function reads that same flat
// shape — the SAME field names ItemFromPayload used to read, just no
// longer nested under an "item" key (Task 5.6's "same shape, different
// side" framing; docs/adr/0065's Addendum). A payload missing the expected
// fields yields a zero item.Item rather than an error, matching
// ItemFromPayload's own "absent path is a non-match, not an error" posture.
func itemFromPayload(payload map[string]any) item.Item {
	var it item.Item
	if v, ok := payload["id"].(string); ok {
		it.ID = v
	}
	if v, ok := payload["type"].(string); ok {
		it.Type = v
	}
	if v, ok := payload["title"].(string); ok {
		it.Title = v
	}
	if v, ok := payload["metadata"].(map[string]any); ok {
		it.Metadata = v
	}
	return it
}

// dispatchRequest/dispatchEvent decode the handler.dispatch wire message
// (packages/pg-router/schemas/handler.dispatch.schema.json /
// .../event.schema.json).
type dispatchRequest struct {
	SchemaVersion string        `json:"schemaVersion"`
	ID            string        `json:"id"`
	Event         dispatchEvent `json:"event"`
}

type dispatchEvent struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// runDispatch implements the `dispatch` INTF-HANDLER subcommand
// (interfaces.md's "Dispatch (core -> handler)"): reads the request as JSON
// on stdin, branches internally on this process's own configured role kind
// (ccpool or command — never exposed to pg-router's core, ADR 0065's "Open
// question resolved" section), runs the moved executor logic unchanged, and
// writes handler.dispatch-reply JSON to stdout with a coarse exit code.
//
// This packet's own scope stops at making both reply forms schema-legal;
// dispatch replies INLINE for every dispatch today (the moved
// ccpoolRun/commandRun logic runs to completion synchronously) — a
// long-running ccpool session correctly holding the call open for its
// duration is a legal choice under DEC-WIRE-1 ("a reply is either an inline
// result or a deferral"), not a violation of it. Actually detaching a
// long-running ccpool session from this call (so the process can reply
// deferred and exit while the session keeps running) is a daemonization
// design this packet does not build — it depends on how docket pg2-oju6w's
// Task 5.4 wire client invokes this subcommand, which is that task's own
// call to make.
func runDispatch(args []string) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	roleConfig := fs.String("role-config", os.Getenv(envRoleConfig), "path to this process's own role config JSON (or "+envRoleConfig+")")
	cfgPath := fs.String("config", os.Getenv(envConfig), "path to this process's own launch config JSON (or "+envConfig+")")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "dispatch: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "dispatch takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch: read request from stdin:", err)
		return conformance.ExitError
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		writeErrorReply(os.Stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("handler.dispatch", v); err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	// Check already proved raw decodes AND matches handler.dispatch's
	// schema, so this second Unmarshal (into the typed shape) cannot fail —
	// mirrors packages/pg-router/internal/core.DiscriminateReply's own
	// "validate first, decode second" ordering.
	var req dispatchRequest
	_ = json.Unmarshal(raw, &req)

	role, err := loadRole(*roleConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return conformance.ExitError
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return conformance.ExitError
	}
	overlayBudgetThresholds(role, cfg)

	dctx := executor.DispatchContext{Role: role, Item: itemFromPayload(req.Event.Payload)}
	deps := buildDeps(cfg)
	// Stamp a fresh per-attempt ExternalID here — the call the old monolithic
	// internal/orchestrator made before invoking the ccpool executor
	// in-process, and which the Phase 5 participant extraction (docket
	// pg2-oju6w) never replaced (pg2-nmpvs): executor.Deps.ExternalID's own
	// doc comment says "resolved ONCE by the orchestrator", and this dispatch
	// subcommand IS that orchestrator now. Role.ExternalID's own doc comment
	// ("the stamp makes it unique per attempt") is why this is stamped fresh
	// per call rather than cached on cfg/role — a redelivered/retried dispatch
	// of the same (role, bead) must not reuse a prior attempt's id (the
	// "reuse existing session" path in internal/executor/ccpool.go's
	// absorbDuplicate re-derives its own id from the EXISTING session's own
	// ExternalID instead, so it never reads this fresh stamp). The clock is
	// deps.Now — the SAME seam ccpoolRun's own wait/watchdog logic reads
	// (internal/executor/executor.go) — rather than an independent time.Now()
	// call, so a caller that fakes Deps.Now for a deterministic test gets one
	// consistent clock, not two.
	deps.ExternalID = stampExternalID(role, cfg.SessionPrefix, dctx.Item.ID, deps.Now)
	result, err := executor.For(role.Type).Dispatch(context.Background(), dctx, deps)
	if err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	// outcome is an opaque STRING on the wire (packages/pg-router's
	// docket pg2-oju6w Task 5.4 retypes handler.dispatch-reply.schema.json's
	// outcome property from object -> string, matching wireclient.
	// Reply.Outcome's Go type — encoding/json cannot unmarshal an object
	// into a string field). result.Fields() still carries the full
	// structured actions/refs shape (report.go's own doc: "an opaque
	// string [object] the core stores"); JSON-encoding it into a string is
	// this module's own minimal fix to stay wire-legal without losing any
	// information the core never interpreted anyway.
	//
	// This is also this module's own INV-CCH-3 obligation (docs/behavior/
	// invariants.md): a post-accept outcome — retryable/resource-limit/
	// critical, mapped by ccpool.go's waitFailureResult into the Unclaimed/
	// Escalated verbs result.Fields() carries here — crosses back to the core
	// as nothing but this one opaque completion-outcome string, never as a
	// distinguishable failure class.
	outcomeJSON, err := json.Marshal(result.Fields())
	if err != nil {
		writeErrorReply(os.Stdout, "encode outcome: "+err.Error())
		return conformance.ExitError
	}
	writeReply(os.Stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       string(outcomeJSON),
	})
	return conformance.ExitOK
}

// externalIDStampLayout is the per-attempt ccpool ExternalID's timestamp
// layout. This module's own internal/dtest.TestStamp fixture
// ("20260616T010203") already reserves the second-precision "20060102T150405"
// layout as this codebase's stamp-generation convention (matching
// internal/ccpool/cli_test.go's "pg-router-worker-zr-1-20260616T010203"
// fixture) — reused here rather than inventing an unrelated format — with
// nanosecond precision appended so two attempts stamped within the same
// wall-clock second still mint distinct ids; a bare second-resolution stamp
// cannot promise that on its own.
const externalIDStampLayout = "20060102T150405.000000000"

// stampExternalID builds role's per-attempt ccpool ExternalID
// (roles.Role.ExternalID's own documented <prefix><name>-<beadid>-<stamp>
// shape), stamping it fresh from now on every call (pg2-nmpvs). now nil
// (buildDeps never sets Deps.Now in production) defaults to time.Now,
// mirroring executor.Deps.Now's own "clock seam; nil ⇒ time.Now" doc
// comment — Deps' own clock() method implementing that default is
// unexported, so this package (main) cannot call it directly and
// re-implements the same nil-check here instead of inventing a second,
// independent clock seam.
func stampExternalID(role roles.Role, prefix, beadID string, now func() time.Time) string {
	if now == nil {
		now = time.Now
	}
	return role.ExternalID(prefix, beadID, now().UTC().Format(externalIDStampLayout))
}

// buildDeps wires the executor.Deps seam bag for production use from cfg:
// a real ccpool.CLIRunner and beads.CLIRunner, everything else left at its
// own nil-safe default (Deps.git/gitOpener/clock/reader/commander/waitPoll —
// internal/executor/executor.go).
func buildDeps(cfg config.Config) executor.Deps {
	return executor.Deps{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  beads.NewCLIRunnerForRepo(cfg.RepoRoot),
		Cfg: cfg,
	}
}

func writeReply(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}

func writeErrorReply(w io.Writer, msg string) {
	writeReply(w, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": msg})
}
