package apply

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

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// connectorBinary is the ambient $PATH name that is exec'd; this module never
// imports packages/pg-connector. The argv shapes mirror pg-desk's
// internal/sync/connector.go (deterministic: sorted metadata and labels).
const connectorBinary = "pg-connector"

// beadsDirEnv is the variable every issue exec carries when a beads dir is
// configured.
const beadsDirEnv = "PG_CONNECTOR_ISSUE_BEADS_DIR"

// wireEnvelope mirrors pkg/scriptout's response envelope.
type wireEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// connectorError is a failed pg-connector issue call.
type connectorError struct {
	args     []string
	exitCode int
	detail   string
}

func (e *connectorError) Error() string {
	if e.exitCode == 4 {
		return fmt.Sprintf("pg-connector %s: not_found", strings.Join(e.args, " "))
	}
	return fmt.Sprintf("pg-connector %s: exit %d: %s", strings.Join(e.args, " "), e.exitCode, e.detail)
}

// backendFlag pins the call to agent_tracker_backend when configured.
func backendFlag(env Env) []string {
	if env.Config == nil || env.Config.AgentTrackerBackend == "" {
		return nil
	}
	return []string{"--backend", env.Config.AgentTrackerBackend}
}

// metadataFlags renders one --metadata flag per entry, sorted by key. A pair
// whose text holds a comma, double quote or newline is CSV-quoted as ONE flag
// value, because pg-connector parses --metadata as a comma-separated map.
func metadataFlags(md map[string]string) []string {
	if len(md) == 0 {
		return nil
	}
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		pair := k + "=" + md[k]
		if strings.ContainsAny(pair, ",\"\n") {
			pair = `"` + strings.ReplaceAll(pair, `"`, `""`) + `"`
		}
		out = append(out, "--metadata", pair)
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

// call runs pg-connector with args, carrying the beads-dir variable when
// configured. It returns stdout on exit 0; every other outcome is an error
// (exit 4 is not_found).
func call(ctx context.Context, env Env, args []string) ([]byte, error) {
	cmd := env.command()(ctx, connectorBinary, args...)
	if env.Config != nil && env.Config.BeadsDir != "" {
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		cmd.Env = append(append([]string{}, base...), beadsDirEnv+"="+env.Config.BeadsDir)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, fmt.Errorf("exec pg-connector %s: %w", strings.Join(args, " "), err)
		}
		detail := strings.TrimSpace(stdout.String())
		var w wireEnvelope
		if json.Unmarshal(stdout.Bytes(), &w) == nil && w.Error != nil {
			detail = w.Error.Code + ": " + w.Error.Message
		}
		if s := strings.TrimSpace(stderr.String()); s != "" && detail == "" {
			detail = s
		}
		return nil, &connectorError{args: args, exitCode: exitErr.ExitCode(), detail: detail}
	}
	return stdout.Bytes(), nil
}

// resultID decodes a targeted call's {"result":{"id":...}} envelope.
func resultID(stdout []byte, args []string) (string, error) {
	var w wireEnvelope
	if err := json.Unmarshal(stdout, &w); err != nil {
		return "", fmt.Errorf("decode pg-connector %s stdout: %w", strings.Join(args, " "), err)
	}
	var res struct {
		ID string `json:"id"`
	}
	if len(w.Result) > 0 {
		if err := json.Unmarshal(w.Result, &res); err != nil {
			return "", fmt.Errorf("decode pg-connector %s result: %w", strings.Join(args, " "), err)
		}
	}
	return res.ID, nil
}

func createArgs(env Env, f action.Fields) []string {
	args := []string{"issue", "create", "--title", f.Title}
	if f.IssueType != "" {
		args = append(args, "--issue-type", f.IssueType)
	}
	if f.Description != "" {
		args = append(args, "--description", f.Description)
	}
	if f.Priority != "" {
		args = append(args, "--priority", f.Priority)
	}
	if len(f.Labels) > 0 {
		args = append(args, "--labels", strings.Join(sortedCopy(f.Labels), ","))
	}
	if f.Parent != "" {
		args = append(args, "--parent", f.Parent)
	}
	args = append(args, metadataFlags(f.Metadata)...)
	return append(args, backendFlag(env)...)
}

// CreateIssue execs `pg-connector issue create` with the fields and returns the
// new work item's id. It does no dedup lookup; Run does that before create.
func CreateIssue(ctx context.Context, env Env, f action.Fields) (string, error) {
	args := createArgs(env, f)
	out, err := call(ctx, env, args)
	if err != nil {
		return "", err
	}
	id, err := resultID(out, args)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("pg-connector %s returned no id", strings.Join(args, " "))
	}
	return id, nil
}

// updateArgs builds `issue update <id>`; reopen adds the status move and the
// two clears (a reopen MUST clear the previous claimant and any stale
// deferral, or no worker can claim the reopened item).
func updateArgs(env Env, id string, f action.Fields, reopen bool) []string {
	args := []string{"issue", "update", id}
	if reopen {
		args = append(args, "--status", "open", "--clear-assignee", "--clear-defer")
	}
	args = append(args, metadataFlags(f.Metadata)...)
	for _, l := range sortedCopy(f.AddLabels) {
		args = append(args, "--add-label", l)
	}
	for _, l := range sortedCopy(f.RemoveLabels) {
		args = append(args, "--remove-label", l)
	}
	if f.Priority != "" {
		args = append(args, "--priority", f.Priority)
	}
	if f.Title != "" {
		args = append(args, "--title", f.Title)
	}
	if f.Description != "" {
		args = append(args, "--description", f.Description)
	}
	return append(args, backendFlag(env)...)
}

// closeArgs builds `issue close <id> --reason "<rule id>: <summary>"`.
func closeArgs(env Env, id string, a action.Action) []string {
	summary := a.Fields.Description
	if summary == "" {
		summary = a.Fields.Title
	}
	if summary == "" {
		summary = "close work item"
		if a.Kind != "" {
			summary = "close " + a.Kind + " work item"
		}
	}
	args := []string{"issue", "close", id, "--reason", a.Rule + ": " + summary}
	return append(args, backendFlag(env)...)
}

// Comment execs `pg-connector issue comment`.
func Comment(ctx context.Context, env Env, workItemID, body string) error {
	args := append([]string{"issue", "comment", workItemID, "--body", body}, backendFlag(env)...)
	_, err := call(ctx, env, args)
	return err
}

type listEntity struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
}

// decodeList decodes `issue list --query work-beads` the way pg-desk's gather
// does ({"entities":[...]} printed directly by the fan-out), and also accepts
// the same members under a wire envelope's "result".
func decodeList(stdout []byte) ([]listEntity, error) {
	var top struct {
		Entities []listEntity    `json:"entities"`
		Result   json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(stdout, &top); err != nil {
		return nil, err
	}
	if top.Entities != nil || len(top.Result) == 0 {
		return top.Entities, nil
	}
	var inner struct {
		Entities []listEntity `json:"entities"`
	}
	if err := json.Unmarshal(top.Result, &inner); err == nil && inner.Entities != nil {
		return inner.Entities, nil
	}
	var arr []listEntity
	if err := json.Unmarshal(top.Result, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

// lookupDedup lists the tracker's work beads and returns the id of one whose
// dedup_key is the same identity as key. A failed or degraded list is an error:
// creating without a trustworthy lookup could duplicate.
func lookupDedup(ctx context.Context, env Env, ref workitem.EntityRef, key string) (string, bool, error) {
	args := append([]string{"issue", "list", "--query", "work-beads"}, backendFlag(env)...)
	out, err := call(ctx, env, args)
	if err != nil {
		return "", false, fmt.Errorf("dedup lookup: %w", err)
	}
	ents, err := decodeList(out)
	if err != nil {
		return "", false, fmt.Errorf("dedup lookup: decode pg-connector %s stdout: %w", strings.Join(args, " "), err)
	}
	for _, e := range ents {
		if k := e.Metadata["dedup_key"]; k != "" && sameKey(ref, key, k) {
			return e.ID, true, nil
		}
	}
	return "", false, nil
}

// sameKey reports whether two dedup keys name the same work item: equal, or
// equal except that one carries the entity's id and the other its node_id.
func sameKey(ref workitem.EntityRef, a, b string) bool {
	if a == b {
		return true
	}
	ta, ia, ka, sa, oka := workitem.ParseKey(a)
	tb, ib, kb, sb, okb := workitem.ParseKey(b)
	if !oka || !okb || ta != tb || ka != kb || sa != sb {
		return false
	}
	if ia == ib {
		return true
	}
	if ref.NodeID == "" {
		return false
	}
	return (ia == ref.ID && ib == ref.NodeID) || (ia == ref.NodeID && ib == ref.ID)
}
