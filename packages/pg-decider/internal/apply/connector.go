package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// beadsDirEnv is the variable every issue exec carries when a beads dir is
// configured. The argv shapes mirror pg-desk's issue calls (deterministic:
// sorted metadata and labels).
const beadsDirEnv = "PG_CONNECTOR_ISSUE_BEADS_DIR"

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

// MintedByLabel is the coarse label every bead the decider creates carries, so a
// reader that wants the whole decider set can ask for one label instead of
// scanning titles. Per-PR detail stays in metadata. The value is the work-item
// contract's (workitem.MintedByLabel).
const MintedByLabel = workitem.MintedByLabel

// MintedBy returns labels plus MintedByLabel, sorted and de-duplicated.
func MintedBy(labels []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(labels)+1)
	for _, l := range append(append([]string{}, labels...), MintedByLabel) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
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

// seenCall is what the runner observed of the last child: its stderr and exit.
type seenCall struct {
	stderr []byte
	exit   int
}

// connectorClient is the published pg-connector client running through env's
// Runner and carrying the beads-dir variable when configured. seen records the
// child's stderr and exit code.
func connectorClient(env Env, seen *seenCall) client.Client {
	run := env.runner()
	c := client.Client{Run: func(ctx context.Context, call cmdrun.Call) cmdrun.Result {
		res := run(ctx, call)
		seen.stderr, seen.exit = res.Stderr, res.Code
		return res
	}}
	if env.Config != nil && env.Config.BeadsDir != "" {
		c.Env = []string{beadsDirEnv + "=" + env.Config.BeadsDir}
	}
	return c
}

// settle turns a client call's outcome into this package's contract: success
// only on exit 0 (a degraded exit 5 is a failure here), and a failure whose
// stdout said nothing carries the child's stderr as its message.
func settle(args []string, raw []byte, err error, seen *seenCall) error {
	if err == nil && seen.exit != 0 {
		err = client.NewExitError(args, seen.exit, raw)
	}
	var ce *client.Error
	if errors.As(err, &ce) && ce.Message == "" {
		ce.Message = strings.TrimSpace(string(seen.stderr))
	}
	return err
}

// call runs pg-connector with args, carrying the beads-dir variable when
// configured. It returns stdout on exit 0; every other outcome is an error (a
// *client.Error; exit 4 is not found).
func call(ctx context.Context, env Env, args []string) ([]byte, error) {
	var seen seenCall
	raw, err := connectorClient(env, &seen).FanOutStdout(ctx, args...)
	if err := settle(args, raw, err, &seen); err != nil {
		return nil, err
	}
	return raw, nil
}

// callResult runs a single-backend verb and returns its answer envelope's
// result; exit 0 only.
func callResult(ctx context.Context, env Env, args []string) (json.RawMessage, error) {
	var seen seenCall
	res, err := connectorClient(env, &seen).Targeted(ctx, args...)
	if err := settle(args, res, err, &seen); err != nil {
		return nil, err
	}
	return res, nil
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
	args = append(args, "--labels", strings.Join(MintedBy(f.Labels), ","))
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
	out, err := callResult(ctx, env, args)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if len(out) > 0 {
		if err := json.Unmarshal(out, &created); err != nil {
			return "", fmt.Errorf("decode pg-connector %s result: %w", strings.Join(args, " "), err)
		}
	}
	id := created.ID
	if id == "" {
		return "", fmt.Errorf("pg-connector %s returned no id", strings.Join(args, " "))
	}
	return id, nil
}

// updateArgs builds `issue update <id>`; reopen adds the status move and the
// two clears (a reopen MUST clear the previous claimant and any stale
// deferral, or no worker can claim the reopened item). A non-reopen update
// renders Fields.Status as --status <s> and Fields.ClearDefer as
// --clear-defer, in that order and before the metadata flags (a focus hold is
// `issue update <id> --status deferred --metadata focus_hold=struck`, a
// release `issue update <id> --status open --clear-defer --metadata
// focus_hold=released`). A release never clears the assignee.
func updateArgs(env Env, id string, f action.Fields, reopen bool) []string {
	args := []string{"issue", "update", id}
	if reopen {
		args = append(args, "--status", "open", "--clear-assignee", "--clear-defer")
	} else {
		if f.Status != "" {
			args = append(args, "--status", f.Status)
		}
		if f.ClearDefer {
			args = append(args, "--clear-defer")
		}
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

// lookupDedup lists the tracker's work beads and returns the id of one whose
// dedup_key is the same identity as key. A failed or degraded list is an error:
// creating without a trustworthy lookup could duplicate.
func lookupDedup(ctx context.Context, env Env, ref workitem.EntityRef, key string) (string, bool, error) {
	return lookupDedupIn(ctx, env, ref, key, "work-beads", false)
}

// lookupFocusDedup is lookupDedup for the focus-item kind: it lists focus
// beads in EVERY status, closed included, through the named query
// config.focus_beads_query, because the work-beads query lists only
// title-matched anchor and feedback beads and a lookup that omitted a closed
// focus bead would re-mint it. No query configured is an error (the create
// fails closed), never a fallback to work-beads. A truncated listing is an
// error too: the bead could be in the part that was cut off.
func lookupFocusDedup(ctx context.Context, env Env, ref workitem.EntityRef, key string) (string, bool, error) {
	if env.Config == nil || env.Config.FocusBeadsQuery == "" {
		return "", false, fmt.Errorf("dedup lookup: focus_beads_query is not configured, so a focus bead cannot be looked up in every status; nothing was created")
	}
	return lookupDedupIn(ctx, env, ref, key, env.Config.FocusBeadsQuery, true)
}

func lookupDedupIn(ctx context.Context, env Env, ref workitem.EntityRef, key, query string, failOnTruncated bool) (string, bool, error) {
	args := append([]string{"issue", "list", "--query", query}, backendFlag(env)...)
	out, err := call(ctx, env, args)
	if err != nil {
		return "", false, fmt.Errorf("dedup lookup: %w", err)
	}
	var ans client.ListOutcome[listEntity]
	if err := json.Unmarshal(out, &ans); err != nil {
		return "", false, fmt.Errorf("dedup lookup: decode pg-connector %s stdout: %w", strings.Join(args, " "), err)
	}
	ents := ans.Entities
	if failOnTruncated && ans.Truncated {
		return "", false, fmt.Errorf("dedup lookup: pg-connector %s was truncated, so the lookup cannot be trusted", strings.Join(args, " "))
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

// permanentFailure reports whether err is a connector refusal that a repeat of
// the same call cannot clear: the connector answered invalid_argument, which is
// how it reports a request the tracker refuses outright (an item another actor
// holds, a malformed field). A failure with any other code, or none, is not
// permanent, so a retry stays possible.
func permanentFailure(err error) bool {
	var ce *client.Error
	return errors.As(err, &ce) && ce.Code == "invalid_argument"
}
