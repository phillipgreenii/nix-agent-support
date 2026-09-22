// connector.go: this probe's own subprocess wrapper around
// `pg-connector issue ...` — every call here passes
// --backend pg-connector-issue-beads [design: Purpose, "Bead identity and
// schema" intro, "Architecture" diagram's MUT node] and NEVER sets
// PG_CONNECTOR_ISSUE_BEADS_DIR itself: this binary inherits its
// environment verbatim, and the caller (a sibling ziprecruiter-repo
// packet wrapping this role's command.argv) is responsible for setting
// that env var [design: "Tracker targeting" paragraphs 1-3].
//
// Identical shape to packages/pg-router-probe/cmd/pg-router-probe/connector.go
// (itself adapted from packages/pg-router-source-pg-connector's own
// exec.go) — copied rather than shared, since this repo's own CLAUDE.md
// keeps each package standalone with no compile-time cross-package
// dependency within packages/.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// pgConnectorBinary is the ambient $PATH name this probe execs. Never a
// compile-time import of packages/pg-connector.
const pgConnectorBinary = "pg-connector"

// pgConnectorBackend is the one, fixed backend every call below pins to
// [design: Contract's "pg-connector's issue capability" bullet].
const pgConnectorBackend = "pg-connector-issue-beads"

// execCmdFactory constructs the *exec.Cmd used to invoke pg-connector.
// Production code uses exec.CommandContext; tests swap this to spawn a
// reentrant test-helper process (testmain_test.go). Kept separate from
// ccpoolexec.go's own ccpoolExecCmdFactory so a test can independently
// fake each subprocess's behavior.
var execCmdFactory = exec.CommandContext

type pgConnectorResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

// runPgConnector execs pg-connector with args, inheriting this process's
// environment verbatim (see this file's own header comment on
// PG_CONNECTOR_ISSUE_BEADS_DIR). The returned error is non-nil only when
// the child could never even be started.
func runPgConnector(ctx context.Context, args []string) (pgConnectorResult, error) {
	cmd := execCmdFactory(ctx, pgConnectorBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	res := pgConnectorResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}

	if runErr == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.exitCode = exitErr.ExitCode()
		return res, nil
	}
	return pgConnectorResult{}, fmt.Errorf("pg-connector: exec %v: %w", args, runErr)
}

// invokeOrFail runs pg-connector with args and treats any exit code in
// okCodes as success; anything else copies pg-connector's own captured
// stderr (if any) to stderr and returns errConnectorFailed.
func invokeOrFail(ctx context.Context, args []string, okCodes map[int]bool, warn func(string)) ([]byte, error) {
	res, err := runPgConnector(ctx, args)
	if err != nil {
		warn(err.Error())
		return nil, errConnectorFailed
	}
	if !okCodes[res.exitCode] {
		if len(res.stderr) > 0 {
			warn(string(res.stderr))
		} else {
			warn(fmt.Sprintf("pg-connector: exit %d", res.exitCode))
		}
		return nil, errConnectorFailed
	}
	return res.stdout, nil
}

var errConnectorFailed = errors.New("ccpool-probe: pg-connector call failed")

// connectorEnvelope is the minimal subset of pg-connector's own targeted-
// op wire envelope this probe decodes: {"result": ...} on success.
type connectorEnvelope struct {
	Result json.RawMessage `json:"result"`
}

// connectorIssue is the subset of pg-connector's schema.Issue this probe
// needs from "issue create"/"issue list".
type connectorIssue struct {
	ID       string            `json:"id"`
	Labels   []string          `json:"labels"`
	Metadata map[string]string `json:"metadata"`
}

// connectorIssueListEnvelope mirrors pg-connector's own "issue list" JSON
// wire shape (cmd/pg-connector/issue.go's issueListOutcome) — decoded
// generically since this probe has no compile-time dependency on
// packages/pg-connector.
type connectorIssueListEnvelope struct {
	Entities []connectorIssue `json:"entities"`
}

// listEscalated runs `pg-connector issue list --query escalated-work
// --backend pg-connector-issue-beads --output json` — the dedup check's
// ONLY allowed data source [Binding decisions: "'Nothing new' rule"
// closing paragraph: "This check MUST run via pg-connector issue list
// ..., never bd search/bd list directly"]. issue list's own exit-code
// scheme is the fan-out scheme (0 ok, 2 degraded-but-usable), same
// convention pg-router-probe's own connector.go uses for the same reason:
// list is a fan-out op, unlike create/update/comment below.
func listEscalated(ctx context.Context, warn func(string)) ([]connectorIssue, error) {
	out, err := invokeOrFail(ctx, []string{
		"issue", "list",
		"--query", "escalated-work",
		"--backend", pgConnectorBackend,
		"--output", "json",
	}, map[int]bool{0: true, 2: true}, warn)
	if err != nil {
		return nil, err
	}
	var env connectorIssueListEnvelope
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
func createIssue(ctx context.Context, title string, labels []string, metadata map[string]string, description string, warn func(string)) (connectorIssue, error) {
	args := []string{
		"issue", "create",
		"--title", title,
		"--description", description,
		"--backend", pgConnectorBackend,
		"--output", "json",
	}
	for _, l := range labels {
		args = append(args, "--labels", l)
	}
	args = append(args, metadataArgs(metadata)...)

	// create's own exit-code scheme is the targeted-op scheme (0 success;
	// anything else is a failure worth surfacing) -- unlike list above, it
	// never returns a usable "degraded" partial result.
	out, err := invokeOrFail(ctx, args, map[int]bool{0: true}, warn)
	if err != nil {
		return connectorIssue{}, err
	}
	var env connectorEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return connectorIssue{}, fmt.Errorf("ccpool-probe: decode issue create response: %w", err)
	}
	var issue connectorIssue
	if err := json.Unmarshal(env.Result, &issue); err != nil {
		return connectorIssue{}, fmt.Errorf("ccpool-probe: decode issue create result: %w", err)
	}
	return issue, nil
}

// updateIssueMetadata runs `pg-connector issue update <id> --metadata
// k=v ...`, merging/setting the given metadata keys on an existing bead —
// used to refresh the tracked comparison fields a later run's dedup
// decision reads back via listEscalated.
func updateIssueMetadata(ctx context.Context, id string, metadata map[string]string, warn func(string)) error {
	args := []string{
		"issue", "update", id,
		"--backend", pgConnectorBackend,
		"--output", "json",
	}
	args = append(args, metadataArgs(metadata)...)
	_, err := invokeOrFail(ctx, args, map[int]bool{0: true}, warn)
	return err
}

// commentIssue runs `pg-connector issue comment <id> --body <body>` —
// later updates append via comment, never overwrite the body [design:
// "Body template" closing sentence].
func commentIssue(ctx context.Context, id, body string, warn func(string)) error {
	args := []string{
		"issue", "comment", id,
		"--body", body,
		"--backend", pgConnectorBackend,
		"--output", "json",
	}
	_, err := invokeOrFail(ctx, args, map[int]bool{0: true}, warn)
	return err
}
