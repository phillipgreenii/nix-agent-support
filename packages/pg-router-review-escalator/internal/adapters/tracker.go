package adapters

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-router-review-escalator/internal/escalate"
)

// DefaultConnectorBinary is the ambient $PATH name execed for every tracker
// and submit call. It is never a compile-time import of packages/pg-connector.
const DefaultConnectorBinary = "pg-connector"

// DefaultTrackerBackend is the pg-connector issue backend the tracker pins
// every call to.
const DefaultTrackerBackend = "pg-connector-issue-beads"

// DefaultListQuery is the named pg-connector query that lists every OPEN
// escalation bead. The deployment MUST define it in the issue backend's
// queries, in every non-closed state (open, in_progress, blocked, deferred),
// and human-labeled beads included, for example:
//
//	pending-review-escalations = "list --label pending-review-escalation --status open,in_progress,blocked,deferred"
//
// A ready-queue view would drop a bead the moment a person claims it, and the
// escalation would be duplicated.
const DefaultListQuery = "pending-review-escalations"

// ConnectorTracker is an escalate.Tracker backed by `pg-connector issue ...`.
// Tracker writes go through pg-connector, never `bd` directly, and the tracker
// the writes land in is chosen by the caller's environment, not by this tool.
type ConnectorTracker struct {
	Runner    Runner
	Binary    string
	Backend   string
	ListQuery string
}

func (t ConnectorTracker) binary() string {
	if t.Binary == "" {
		return DefaultConnectorBinary
	}
	return t.Binary
}

func (t ConnectorTracker) backend() string {
	if t.Backend == "" {
		return DefaultTrackerBackend
	}
	return t.Backend
}

func (t ConnectorTracker) query() string {
	if t.ListQuery == "" {
		return DefaultListQuery
	}
	return t.ListQuery
}

// call runs pg-connector with args and returns stdout. Only exit 0 is success:
// the degraded exit (2) of a fan-out op is NOT accepted, because an incomplete
// list would let a duplicate escalation be created.
func (t ConnectorTracker) call(ctx context.Context, args []string) ([]byte, error) {
	res, err := t.Runner.Run(ctx, t.binary(), args, nil, nil)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(res.Stdout))
		}
		return nil, fmt.Errorf("pg-connector %s: exit %d: %s", args[0:2], res.ExitCode, msg)
	}
	return res.Stdout, nil
}

type connectorIssue struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Labels   []string          `json:"labels"`
	Metadata map[string]string `json:"metadata"`
}

// ListOpen implements escalate.Tracker.
func (t ConnectorTracker) ListOpen(ctx context.Context) ([]escalate.Issue, error) {
	out, err := t.call(ctx, []string{"issue", "list", "--query", t.query(), "--backend", t.backend(), "--output", "json"})
	if err != nil {
		return nil, err
	}
	var env struct {
		Entities []connectorIssue `json:"entities"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("decode issue list: %w", err)
	}
	issues := make([]escalate.Issue, 0, len(env.Entities))
	for _, e := range env.Entities {
		issues = append(issues, escalate.Issue{ID: e.ID, Title: e.Title, Labels: e.Labels, Metadata: e.Metadata})
	}
	return issues, nil
}

// Create implements escalate.Tracker.
func (t ConnectorTracker) Create(ctx context.Context, in escalate.NewIssue) (escalate.Issue, error) {
	args := []string{"issue", "create", "--title", in.Title, "--description", in.Description, "--backend", t.backend(), "--output", "json"}
	if in.Priority != "" {
		args = append(args, "--priority", in.Priority)
	}
	for _, l := range in.Labels {
		args = append(args, "--labels", l)
	}
	args = append(args, metadataArgs(in.Metadata)...)
	out, err := t.call(ctx, args)
	if err != nil {
		return escalate.Issue{}, err
	}
	var env struct {
		Result connectorIssue `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return escalate.Issue{}, fmt.Errorf("decode issue create: %w", err)
	}
	if env.Result.ID == "" {
		return escalate.Issue{}, fmt.Errorf("decode issue create: the answer carries no issue id")
	}
	return escalate.Issue{ID: env.Result.ID, Title: env.Result.Title, Labels: env.Result.Labels, Metadata: env.Result.Metadata}, nil
}

// Comment implements escalate.Tracker.
func (t ConnectorTracker) Comment(ctx context.Context, id, body string) error {
	_, err := t.call(ctx, []string{"issue", "comment", id, "--body", body, "--backend", t.backend(), "--output", "json"})
	return err
}

// SetMetadata implements escalate.Tracker.
func (t ConnectorTracker) SetMetadata(ctx context.Context, id string, md map[string]string) error {
	args := []string{"issue", "update", id, "--backend", t.backend(), "--output", "json"}
	args = append(args, metadataArgs(md)...)
	_, err := t.call(ctx, args)
	return err
}

// Close implements escalate.Tracker.
func (t ConnectorTracker) Close(ctx context.Context, id, reason string) error {
	_, err := t.call(ctx, []string{"issue", "close", id, "--reason", reason, "--backend", t.backend(), "--output", "json"})
	return err
}

// metadataArgs renders a metadata map as one repeated --metadata flag per
// entry, in key order. pg-connector's --metadata is a pflag StringToString: a
// value that itself contains "=" is re-parsed as a CSV row and split on any
// unquoted comma, so each "k=v" token is CSV-encoded as a single field. A
// token with no comma, quote or newline comes back unchanged.
func metadataArgs(md map[string]string) []string {
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		args = append(args, "--metadata", csvField(k+"="+md[k]))
	}
	return args
}

func csvField(s string) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{s}); err != nil {
		return s
	}
	w.Flush()
	return strings.TrimRight(buf.String(), "\n")
}
