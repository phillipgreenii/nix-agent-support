package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// pgConnectorBinary is the ambient $PATH name this package execs — never a
// compile-time import of packages/pg-connector (D10), mirroring
// internal/gather/gather.go's own pgConnectorBinary exactly. Every bead
// write goes through pg-connector issue with the workspace variable set,
// the same rule gather already follows (design sections 7.3, 7.5).
const pgConnectorBinary = "pg-connector"

// execCmdFactory constructs the *exec.Cmd used to invoke pg-connector.
// Production code uses exec.CommandContext; sync_test.go swaps this to
// spawn a reentrant test-helper process, mirroring gather_test.go's own
// wire-double harness exactly (this packet's own Files instruction: "Tests
// against an in-process pg-connector wire double reusing
// pkg/scriptout/conformance").
var execCmdFactory = exec.CommandContext

// issueClient wraps the subset of `pg-connector issue` this package needs:
// create/update/transition (plus the live `issue children` read, which
// handleClosure uses to find children the ledger and work-beads never saw),
// pinned to cfg.AgentTrackerBackend when set
// (design section 7.5: "Sync writes only agent signals, only through
// pg-connector issue, pinned to agent_tracker_backend" — Config.
// AgentTrackerBackend, present but unused before this packet, starts being
// consumed here). `issue list` is deliberately absent: adoption reuses
// gather's own already-fetched Facts.WorkBeads (see adoption.go) rather
// than sync issuing a second `issue list --query work-beads` call.
type issueClient struct {
	cfg *config.Config
}

func newIssueClient(cfg *config.Config) *issueClient { return &issueClient{cfg: cfg} }

// issueResult is the minimal subset of schema.Issue this package decodes
// for itself — mirroring gather.go's prShowFields/workBeadEntity's own
// "hand-decode rather than import pkg/schema" precedent (D10 is about exec,
// not import, but staying off pkg/schema keeps this package decoupled from
// pg-connector's Go API, talking to it only over the CLI/wire surface).
type issueResult struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// createInput is the field set `issue create` accepts, mirroring
// cmd/pg-connector/issue.go's newIssueCreateCmd flags.
type createInput struct {
	Title       string
	IssueType   string
	Description string
	Labels      []string
	Metadata    map[string]string
	Parent      string
}

// Create execs `pg-connector issue create ...` and returns the newly
// created issue's id.
func (c *issueClient) Create(ctx context.Context, in createInput) (string, error) {
	args := []string{"issue", "create", "--title", in.Title}
	if in.IssueType != "" {
		args = append(args, "--issue-type", in.IssueType)
	}
	if in.Description != "" {
		args = append(args, "--description", in.Description)
	}
	if len(in.Labels) > 0 {
		args = append(args, "--labels", strings.Join(in.Labels, ","))
	}
	if in.Parent != "" {
		args = append(args, "--parent", in.Parent)
	}
	args = append(args, metadataFlags(in.Metadata)...)
	args = append(args, c.backendFlag()...)

	res, err := c.targetedCall(ctx, args)
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

// updateInput is the field set `issue update` accepts, mirroring
// cmd/pg-connector/issue.go's newIssueUpdateCmd flags. Every field is
// optional and applied together in one call — a zero-value updateInput
// (Metadata nil, AddLabels/RemoveLabels nil, Priority "") issues no
// mutating flags at all; callers are expected to skip the call entirely
// rather than issue a no-op update.
type updateInput struct {
	Metadata     map[string]string
	AddLabels    []string
	RemoveLabels []string
	Priority     string
	// Status moves the issue to this state in the same call (e.g. "open" to
	// reopen a closed issue); "" leaves the state alone.
	Status string
	// ClearAssignee clears the issue's assignee in the same call. A reopen
	// MUST set it together with Status, or the previous claimant stays on the
	// reopened issue and no worker can claim it (pg2-1pt7r).
	ClearAssignee bool
	// ClearDefer clears the issue's deferral in the same call. A review
	// request reopened on head advance MUST set it: the review worker releases
	// a blocked review with a deferral (so it is not redispatched at once), and
	// a reopen that keeps the old defer_until hides the NEW head from every
	// ready query until that deferral expires (pg2-vhs3e).
	ClearDefer bool
}

// Update execs `pg-connector issue update <id> ...`.
func (c *issueClient) Update(ctx context.Context, id string, in updateInput) error {
	args := []string{"issue", "update", id}
	args = append(args, metadataFlags(in.Metadata)...)
	for _, l := range sortedCopy(in.AddLabels) {
		args = append(args, "--add-label", l)
	}
	for _, l := range sortedCopy(in.RemoveLabels) {
		args = append(args, "--remove-label", l)
	}
	if in.Priority != "" {
		args = append(args, "--priority", in.Priority)
	}
	if in.Status != "" {
		args = append(args, "--status", in.Status)
	}
	if in.ClearAssignee {
		args = append(args, "--clear-assignee")
	}
	if in.ClearDefer {
		args = append(args, "--clear-defer")
	}
	args = append(args, c.backendFlag()...)

	_, err := c.targetedCall(ctx, args)
	return err
}

// Show execs `pg-connector issue show <id>` and returns the issue's
// metadata (the only field the closed-anchor audit reads).
func (c *issueClient) Show(ctx context.Context, id string) (issueResult, error) {
	args := []string{"issue", "show", id}
	args = append(args, c.backendFlag()...)
	return c.targetedCall(ctx, args)
}

// Transition execs `pg-connector issue transition <id> --state <state>`.
func (c *issueClient) Transition(ctx context.Context, id, state string) error {
	args := []string{"issue", "transition", id, "--state", state}
	args = append(args, c.backendFlag()...)
	_, err := c.targetedCall(ctx, args)
	return err
}

// liveChild is the subset of schema.Issue `issue children` entries this
// package reads.
type liveChild struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Parent string `json:"parent"`
}

// Children execs `pg-connector issue children <id>`: a LIVE read of id's
// non-closed direct children (never the entity cache, so a stale cache cannot
// make a bead look childless). handleClosure uses it to find children that
// neither the ledger nor Facts.WorkBeads know about (bead pg2-ubvmh). A
// failure is returned as an error, never as "no children"; the caller decides
// whether it can proceed without the read.
func (c *issueClient) Children(ctx context.Context, id string) ([]liveChild, error) {
	args := []string{"issue", "children", id}
	args = append(args, c.backendFlag()...)
	raw, err := c.targetedRaw(ctx, args)
	if err != nil {
		return nil, err
	}
	var res struct {
		Children []liveChild `json:"children"`
	}
	if len(raw) > 0 {
		if decErr := json.Unmarshal(raw, &res); decErr != nil {
			return nil, fmt.Errorf("sync: decode pg-connector %v result: %w", args, decErr)
		}
	}
	return res.Children, nil
}

// backendFlag pins dispatch to cfg.AgentTrackerBackend when configured —
// design section 7.5's "pinned to agent_tracker_backend".
func (c *issueClient) backendFlag() []string {
	if c.cfg == nil || c.cfg.AgentTrackerBackend == "" {
		return nil
	}
	return []string{"--backend", c.cfg.AgentTrackerBackend}
}

// metadataFlags renders one `--metadata key=value` flag per entry, sorted
// by key for deterministic argv (map iteration order is not stable, and
// this package's own plan/apply parity relies on deterministic argv/hash
// construction).
func metadataFlags(metadata map[string]string) []string {
	if len(metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		out = append(out, "--metadata", k+"="+metadata[k])
	}
	return out
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// issueBeadsDirEnv builds the extraEnv slice every issue exec carries:
// PG_CONNECTOR_ISSUE_BEADS_DIR=<the configured repo's beads_dir>, and
// nothing at all when it is unset — mirroring internal/gather/gather.go's
// own issueBeadsDirEnv exactly (design sections 7.3, 7.5: "the workspace
// variable rule gather already follows").
func (c *issueClient) issueBeadsDirEnv() []string {
	if c.cfg == nil || len(c.cfg.Repos) == 0 {
		return nil
	}
	dir := c.cfg.Repos[0].BeadsDir
	if dir == "" {
		return nil
	}
	return []string{"PG_CONNECTOR_ISSUE_BEADS_DIR=" + dir}
}

// wireEnvelope/wireError mirror internal/gather/gather.go's own decode
// shapes for pkg/scriptout's Response envelope.
type wireEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// targetedCall runs a targeted `pg-connector issue` verb (create/update/
// transition) and classifies its outcome per pg-connector's own 0/4/1
// targeted-op exit-code scheme (cmd/pg-connector's outcome.go
// TargetedExitCode) — mirroring internal/gather/gather.go's own
// targetedCall exactly, one layer up (issue writes rather than PR/CI
// reads). exit 4 (not_found) is surfaced as an error here: none of
// create/update/transition has a meaningful "not found is fine" case the
// way gather's removed re-read does.
func (c *issueClient) targetedCall(ctx context.Context, args []string) (issueResult, error) {
	raw, err := c.targetedRaw(ctx, args)
	if err != nil {
		return issueResult{}, err
	}
	var res issueResult
	if len(raw) > 0 {
		if decErr := json.Unmarshal(raw, &res); decErr != nil {
			return issueResult{}, fmt.Errorf("sync: decode pg-connector %v result: %w", args, decErr)
		}
	}
	return res, nil
}

// targetedRaw is targetedCall's transport: it runs the verb, classifies the
// exit code, and returns the envelope's undecoded result payload.
func (c *issueClient) targetedRaw(ctx context.Context, args []string) (json.RawMessage, error) {
	cmd := execCmdFactory(ctx, pgConnectorBinary, args...)
	if env := c.issueBeadsDirEnv(); len(env) > 0 {
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		cmd.Env = append(append([]string{}, base...), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("sync: exec pg-connector %v: %w", args, runErr)
		}
	}

	switch exitCode {
	case 0:
		var env wireEnvelope
		if decErr := json.Unmarshal(stdout.Bytes(), &env); decErr != nil {
			return nil, fmt.Errorf("sync: decode pg-connector %v stdout: %w", args, decErr)
		}
		return env.Result, nil
	case 4:
		return nil, &ConnectorError{Args: args, ExitCode: exitCode, Code: "not_found"}
	default:
		code, detail := wireErrorDetail(stdout.Bytes())
		return nil, &ConnectorError{Args: args, ExitCode: exitCode, Code: code, Detail: detail}
	}
}

// ConnectorError is a failed targeted `pg-connector issue` call (bead
// pg2-xb6fs): it keeps the wire-taxonomy error code pg-connector reported
// (pkg/scriptout's closed set — not_found, unauthenticated, unavailable,
// unknown_op, version_mismatch, invalid_argument, query_not_recognized) so
// Classify can tell an environmental failure from one that needs a person
// without matching message text. Error() renders exactly the text this
// package produced before the type existed, so recorded sync_error strings
// do not change shape.
type ConnectorError struct {
	Args     []string
	ExitCode int
	// Code is the wire error code, or "" when stdout carried no error
	// envelope (then Detail is the raw, trimmed stdout).
	Code string
	// Detail is "<code>: <message>" from the envelope, or the raw stdout.
	Detail string
}

func (e *ConnectorError) Error() string {
	if e.ExitCode == 4 {
		return fmt.Sprintf("sync: pg-connector %v: not_found", e.Args)
	}
	return fmt.Sprintf("sync: pg-connector %v: exit %d: %s", e.Args, e.ExitCode, e.Detail)
}

// wireErrorDetail returns the envelope's error code and its
// "<code>: <message>" rendering, or ("", trimmed stdout) when stdout is not
// an error envelope.
func wireErrorDetail(stdout []byte) (code, detail string) {
	var env wireEnvelope
	if err := json.Unmarshal(stdout, &env); err == nil && env.Error != nil {
		return env.Error.Code, env.Error.Code + ": " + env.Error.Message
	}
	return "", strings.TrimSpace(string(stdout))
}
