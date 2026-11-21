// connector.go: this probe's own subprocess wrapper around
// `pg-connector issue ...` — every call here passes
// --backend <instance> (default pg-connector-issue-beads, --connector-backend
// overrides) [design: Purpose, "Bead identity and schema" intro,
// "Architecture" diagram's MUT node] and NEVER sets
// PG_CONNECTOR_ISSUE_BEADS_DIR itself. The tracker is selected by which
// registered pg-connector instance --backend names (the instance's registry
// command carries its own --beads-dir); this binary inherits its environment
// verbatim, so an unsuffixed registration with no --beads-dir still falls
// back to whatever env the caller set [design: "Tracker targeting"
// paragraphs 1-3, as amended by pg2-91y12].
//
// Identical shape to packages/pg-router-probe/cmd/pg-router-probe/connector.go
// (itself adapted from packages/pg-router-source-pg-connector's own
// exec.go) — copied rather than shared, since this repo's own CLAUDE.md
// keeps each package standalone with no compile-time cross-package
// dependency within packages/.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
	"github.com/phillipgreenii/x/cmdrun"
)

// defaultPgConnectorBackend is the backend instance name every call below
// pins to unless --connector-backend overrides it [design: Contract's
// "pg-connector's issue capability" bullet]. pg-connector can register one
// backend binary under several suffixed instance names (pg2-91y12), so a
// deployment that drops the unsuffixed registration names one of them.
const defaultPgConnectorBackend = "pg-connector-issue-beads"

// Every call below takes the backend instance name as an explicit
// parameter and passes it as --backend; the run verb threads it in from
// --connector-backend through runOptions (run.go defaultRunDeps), so there
// is no package-level mutable state.

// connectorRunner runs the pg-connector subprocess. nil means cmdrun.Exec;
// tests swap it for an in-process double (testmain_test.go) instead of
// re-executing the test binary. Kept separate from ccpoolexec.go's own
// ccpoolRunner so a test can independently fake each subprocess's behavior.
var connectorRunner cmdrun.Runner

// callConnector runs one pg-connector verb through the published client over
// the connectorRunner seam, inheriting this process's environment verbatim
// (see this file's own header comment on how the tracker is selected).
// fanOut true runs a fan-out verb ("list"), accepting exits 0 and 5 (degraded
// but usable); false runs a targeted verb, accepting exit 0 only. A failure
// copies pg-connector's own captured stderr (if any) to warn, else the client
// error, and returns errConnectorFailed. The returned bytes are the raw stdout
// for a fan-out call and the envelope's result for a targeted one.
func callConnector(ctx context.Context, args []string, fanOut bool, warn func(string)) ([]byte, error) {
	var stderr []byte
	cl := client.Client{Run: func(ctx context.Context, call cmdrun.Call) cmdrun.Result {
		r := runnerOrExec(connectorRunner)(ctx, call)
		stderr = r.Stderr
		return r
	}}
	var (
		out []byte
		err error
	)
	if fanOut {
		out, err = cl.FanOutStdout(ctx, args...)
	} else {
		out, err = cl.Targeted(ctx, args...)
	}
	if err != nil {
		if len(stderr) > 0 {
			warn(string(stderr))
		} else {
			warn(err.Error())
		}
		return nil, errConnectorFailed
	}
	return out, nil
}

var errConnectorFailed = errors.New("ccpool-probe: pg-connector call failed")

// connectorIssue is the subset of pg-connector's schema.Issue this probe
// needs from "issue create"/"issue list".
type connectorIssue struct {
	ID       string            `json:"id"`
	Labels   []string          `json:"labels"`
	Metadata map[string]string `json:"metadata"`
}

// defaultDedupQuery is the named pg-connector query this probe's dedup
// check runs by default (--dedup-query overrides it). It MUST be a query
// that returns EVERY non-closed escalated bead -- open, in_progress,
// blocked, deferred, and human-labeled -- NOT the triager's own
// dispatch query "escalated-work" ("ready --label escalated
// --exclude-label human"). That dispatch query is a ready-queue view: it
// drops a bead the moment it gains the human label, is claimed
// (in_progress), is deferred, or gains an open blocker. Using it for
// dedup lets the probe file a second bead for a fingerprint a
// human-parked bead already holds (pg2-dvkbh: pg-router-probe filed
// pg2-imr6o as a duplicate of pg2-68005 on 2026-09-29, the tick after
// pg2-68005 gained "human"; pg2-p48mo: same defect here). The deployment
// config (the pg-connector-issue-beads backend's queries block) MUST
// define this name as a bd "list" expression, e.g.
//
//	escalated-all = "list --label escalated --status open,in_progress,blocked,deferred"
const defaultDedupQuery = "escalated-all"

// listEscalated runs `pg-connector issue list --query <query>
// --backend pg-connector-issue-beads --output json` -- the dedup check's
// ONLY allowed data source [Binding decisions: "'Nothing new' rule"
// closing paragraph: "This check MUST run via pg-connector issue list
// ..., never bd search/bd list directly"]. query is the dedup query name
// (defaultDedupQuery unless overridden), see its doc comment for the
// required semantics. issue list's own exit-code
// scheme is the fan-out scheme (0 ok, 5 Degraded but usable), same
// convention pg-router-probe's own connector.go uses for the same reason:
// list is a fan-out op, unlike create/update/comment below.
func listEscalated(ctx context.Context, backend, query string, warn func(string)) ([]connectorIssue, error) {
	out, err := callConnector(ctx, []string{
		"issue", "list",
		"--query", query,
		"--backend", backend,
		"--output", "json",
	}, true, warn)
	if err != nil {
		return nil, err
	}
	var env client.ListOutcome[connectorIssue]
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("ccpool-probe: decode issue list response: %w", err)
	}
	return env.Entities, nil
}

// metadataArgs renders a metadata map as one repeated --metadata k=v flag
// per entry (pg-connector's own StringToStringVar flag accepts either
// form; repeating the flag keeps each value's own `=`/`,` characters from
// needing escaping).
func metadataArgs(metadata map[string]string) []string {
	args := make([]string, 0, len(metadata)*2)
	for k, v := range metadata {
		args = append(args, "--metadata", k+"="+v)
	}
	return args
}

// createIssue runs `pg-connector issue create` with the given title,
// labels, metadata and description (the rendered body template becomes
// the issue's description on create; later updates append via comment,
// never overwrite it — see commentIssue below) [design: "Bead identity
// and schema" throughout; Binding decisions "Body template" closing
// sentence].
func createIssue(ctx context.Context, backend, title string, labels []string, metadata map[string]string, description string, warn func(string)) (connectorIssue, error) {
	args := []string{
		"issue", "create",
		"--title", title,
		"--description", description,
		"--backend", backend,
		"--output", "json",
	}
	for _, l := range labels {
		args = append(args, "--labels", l)
	}
	args = append(args, metadataArgs(metadata)...)

	// create's own exit-code scheme is the targeted-op scheme (0 success;
	// anything else is a failure worth surfacing) -- unlike list above, it
	// never returns a usable "degraded" partial result.
	out, err := callConnector(ctx, args, false, warn)
	if err != nil {
		return connectorIssue{}, err
	}
	var issue connectorIssue
	if err := json.Unmarshal(out, &issue); err != nil {
		return connectorIssue{}, fmt.Errorf("ccpool-probe: decode issue create result: %w", err)
	}
	return issue, nil
}

// updateIssueMetadata runs `pg-connector issue update <id> --metadata
// k=v ...`, merging/setting the given metadata keys on an existing bead —
// used to refresh the tracked comparison fields a later run's dedup
// decision reads back via listEscalated.
func updateIssueMetadata(ctx context.Context, backend, id string, metadata map[string]string, warn func(string)) error {
	args := []string{
		"issue", "update", id,
		"--backend", backend,
		"--output", "json",
	}
	args = append(args, metadataArgs(metadata)...)
	_, err := callConnector(ctx, args, false, warn)
	return err
}

// commentIssue runs `pg-connector issue comment <id> --body <body>` —
// later updates append via comment, never overwrite the body [design:
// "Body template" closing sentence].
func commentIssue(ctx context.Context, backend, id, body string, warn func(string)) error {
	args := []string{
		"issue", "comment", id,
		"--body", body,
		"--backend", backend,
		"--output", "json",
	}
	_, err := callConnector(ctx, args, false, warn)
	return err
}
