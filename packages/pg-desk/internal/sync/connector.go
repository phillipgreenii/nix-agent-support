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
// create/update/transition, pinned to cfg.AgentTrackerBackend when set
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
	ID string `json:"id"`
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
	args = append(args, c.backendFlag()...)

	_, err := c.targetedCall(ctx, args)
	return err
}

// Transition execs `pg-connector issue transition <id> --state <state>`.
func (c *issueClient) Transition(ctx context.Context, id, state string) error {
	args := []string{"issue", "transition", id, "--state", state}
	args = append(args, c.backendFlag()...)
	_, err := c.targetedCall(ctx, args)
	return err
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
			return issueResult{}, fmt.Errorf("sync: exec pg-connector %v: %w", args, runErr)
		}
	}

	switch exitCode {
	case 0:
		var env wireEnvelope
		if decErr := json.Unmarshal(stdout.Bytes(), &env); decErr != nil {
			return issueResult{}, fmt.Errorf("sync: decode pg-connector %v stdout: %w", args, decErr)
		}
		var res issueResult
		if len(env.Result) > 0 {
			if decErr := json.Unmarshal(env.Result, &res); decErr != nil {
				return issueResult{}, fmt.Errorf("sync: decode pg-connector %v result: %w", args, decErr)
			}
		}
		return res, nil
	case 4:
		return issueResult{}, fmt.Errorf("sync: pg-connector %v: not_found", args)
	default:
		return issueResult{}, fmt.Errorf("sync: pg-connector %v: exit %d: %s", args, exitCode, wireErrorMessage(stdout.Bytes()))
	}
}

func wireErrorMessage(stdout []byte) string {
	var env wireEnvelope
	if err := json.Unmarshal(stdout, &env); err == nil && env.Error != nil {
		return env.Error.Code + ": " + env.Error.Message
	}
	return strings.TrimSpace(string(stdout))
}
