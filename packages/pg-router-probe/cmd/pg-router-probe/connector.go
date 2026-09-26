// connector.go: this probe's own subprocess wrapper around
// `pg-connector issue ...` — every call here passes
// --backend pg-connector-issue-beads [design: Purpose, "Bead identity and
// schema" intro, "Architecture" diagram's MUT node] and NEVER sets
// PG_CONNECTOR_ISSUE_BEADS_DIR itself: this binary inherits its
// environment verbatim, and the caller (a sibling ziprecruiter-repo
// packet wrapping this role's command.argv) is responsible for setting
// that env var [design: "Tracker targeting" paragraphs 1-3].
//
// This is the same "one-shot exec, capture stdout/stderr separately,
// classify by exit code" shape as
// packages/pg-router-source-pg-connector/cmd/pg-router-source-pg-connector/exec.go,
// adapted from that adapter's read verbs (changes/sweep/list) to this
// probe's own write verbs (create/update/comment) plus its one read verb
// (list, used only for the dedup query [Binding decisions: "'Nothing
// new' rule" closing paragraph]).
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// pgConnectorBinary is the ambient $PATH name this probe execs. Never a
// compile-time import of packages/pg-connector.
const pgConnectorBinary = "pg-connector"

// pgConnectorBackend is the one, fixed backend every call below pins to
// [design: Contract's "pg-connector's issue capability" bullet].
const pgConnectorBackend = "pg-connector-issue-beads"

// execCmdFactory constructs the *exec.Cmd used to invoke pg-connector.
// Production code uses exec.CommandContext; tests swap this to spawn a
// reentrant test-helper process (testmain_test.go), mirroring
// pg-router-source-pg-connector's own exec.go execCmdFactory pattern.
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

var errConnectorFailed = errors.New("pg-router-probe: pg-connector call failed")

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
// scheme is the fan-out scheme (0 ok, 2 degraded-but-usable) — mirroring
// pg-router-source-pg-connector's own classifyExit for the same reason:
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
		return nil, fmt.Errorf("pg-router-probe: decode issue list response: %w", err)
	}
	return env.Entities, nil
}

// metadataArgs renders a metadata map as one repeated --metadata k=v flag
// per entry.
//
// pg-connector's own --metadata flag (packages/pg-connector/cmd/pg-connector
// /issue.go) is pflag's StringToStringVar. Its Set() counts every "=" in the
// WHOLE flag value: with exactly one "=" it keeps the value verbatim, but
// with two or more (i.e. whenever the value ITSELF contains an "=", which
// every k=v pair here does once you count the flag's own leading "k=") it
// re-parses the whole value as one CSV row and splits on any UNQUOTED
// comma. grafanaAlertFingerprint's own wire format is
// "<rule-uid>|k1=v1,k2=v2,..." [fingerprint.go], so any finding with more
// than one label (in particular the "type" label that distinguishes
// pg-router-queue-depth-growing's pr.reconcile vs pr.changed instances)
// pushes the "=" count to 2+ and hits that CSV path -- pg-connector then
// shreds the fingerprint at its first comma and spills the remaining
// "key=value" fragments as bogus top-level metadata keys. Every affected
// bead's stored fingerprint collapses to just "<rule-uid>|__alert_rule_uid__
// =<rule-uid>" (that key always sorts first and its value always equals
// the rule uid), so two genuinely different alert instances (different
// "type") collapse to the SAME stored fingerprint and dedup.go's
// matchExisting can never tell them apart -- this is pg2-0gn0u's actual
// root cause: a metadata-transport bug, not an omission in fingerprint
// computation (grafanaAlertFingerprint already hashes every label it is
// given, "type" included; see fingerprint_test.go's
// TestGrafanaAlertFingerprintDistinguishesTypeLabel).
//
// The fix: CSV-encode the whole "k=v" token as a single field before
// handing it to pg-connector. A field with no comma/quote/newline comes
// back byte-for-byte unchanged (csv.Writer only quotes when needed), so
// this is a no-op for every existing simple case; a field containing
// commas gets wrapped in a quoted CSV field starting at position 0, which
// pflag's own csv.NewReader on the receiving end reads back as a single
// unsplit element -- reconstructing the original value exactly.
func metadataArgs(metadata map[string]string) []string {
	args := make([]string, 0, len(metadata)*2)
	for k, v := range metadata {
		args = append(args, "--metadata", csvEncodeMetadataPair(k, v))
	}
	return args
}

// csvEncodeMetadataPair renders "k=v" as a single RFC 4180 CSV field via
// encoding/csv, so it round-trips intact through pg-connector's pflag
// StringToStringVar parser (see metadataArgs' own doc comment above).
func csvEncodeMetadataPair(k, v string) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{k + "=" + v}); err != nil {
		// A single well-formed Go string can only fail here on a writer
		// I/O error, which an in-memory bytes.Buffer never returns in
		// practice; fall back to the unencoded form rather than losing
		// the argument entirely.
		return k + "=" + v
	}
	w.Flush()
	return strings.TrimRight(buf.String(), "\n")
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
		return connectorIssue{}, fmt.Errorf("pg-router-probe: decode issue create response: %w", err)
	}
	var issue connectorIssue
	if err := json.Unmarshal(env.Result, &issue); err != nil {
		return connectorIssue{}, fmt.Errorf("pg-router-probe: decode issue create result: %w", err)
	}
	return issue, nil
}

// updateIssueMetadata runs `pg-connector issue update <id> --metadata
// k=v ...`, merging/setting the given metadata keys on an existing bead —
// used to refresh the tracked comparison fields (state/severity/hash) a
// later run's dedup decision reads back via listEscalated.
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
