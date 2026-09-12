package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// queryRequest decodes the source.query wire message (packages/pg-router/
// schemas/source.query.schema.json).
type queryRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Callback      string `json:"callback"`
}

// runQuery implements the `query` INTF-SOURCE subcommand (pull direction):
// reads the source.query request as JSON on stdin, writes source.query-reply
// JSON to stdout. It always answers inline with zero events — the built-in
// beads-backed query source (packages/pg-router/internal/query/beads.go,
// internal/roles/builtin.go's built-in defaults) is docket pg2-oju6w's Task
// 5.8 to move here and wire for real (pg2-oju6w.3's own Produces text:
// "internal/query, query wired for real by Task 5.8"); this packet's own
// move-list never named those files (Files, above), so a real answer is
// deliberately out of scope here. An empty inline reply is schema-legal
// (source.query-reply's "inline events" branch requires only a — possibly
// empty — events array) and honestly reports "nothing to offer yet" rather
// than a synthetic deferral this process would never resolve.
func runQuery(args []string) int {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "query:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "query: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "query takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query: read request from stdin:", err)
		return conformance.ExitError
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		writeErrorReply(os.Stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("source.query", v); err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	var req queryRequest
	_ = json.Unmarshal(raw, &req) // Check above already proved this decodes.

	writeReply(os.Stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"events":        []any{},
	})
	return conformance.ExitOK
}
