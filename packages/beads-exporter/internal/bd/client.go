package bd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/failure"
)

// supportedSchemaVersion is the only envelope schema_version the exporter
// understands. Anything else is schema skew.
const supportedSchemaVersion = 1

// staleJSONLName is the file whose presence next to the beads store makes a bd
// session import it and clobber newer rows.
const staleJSONLName = "issues.jsonl"

// ClientConfig configures one database's Client.
type ClientConfig struct {
	// BDPath is the absolute path of the bd binary. bd is never found by PATH
	// lookup.
	BDPath string
	// BeadsDir is the database's .beads directory, passed as BEADS_DIR.
	BeadsDir string
	// Home is the HOME value re-asserted on every child.
	Home string
	// ChildPath is the PATH value of every child (bash and coreutils only).
	ChildPath string
	// Timeout bounds each bd call; the process group is killed on expiry.
	Timeout time.Duration
	// Runner executes the child process.
	Runner Runner
}

// Client is the Adapter implementation over a Runner. It is bound to exactly
// one database.
type Client struct {
	cfg ClientConfig
}

// NewClient builds a Client. A nil Runner defaults to ExecRunner.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Runner == nil {
		cfg.Runner = ExecRunner{}
	}
	return &Client{cfg: cfg}
}

var _ Adapter = (*Client)(nil)

// ChildEnv returns the complete environment of every bd child. Nothing is
// inherited from the daemon's own environment.
func (c *Client) ChildEnv() []string {
	return ChildEnv(c.cfg.Home, c.cfg.ChildPath, c.cfg.BeadsDir)
}

// ChildEnv builds the re-asserted bd child environment.
func ChildEnv(home, childPath, beadsDir string) []string {
	return []string{
		"HOME=" + home,
		"PATH=" + childPath,
		"BEADS_DIR=" + beadsDir,
		"BD_JSON_ENVELOPE=1",
		"BEADS_DOLT_AUTO_START=0",
		"BD_BACKUP_ENABLED=0",
	}
}

// CheckStale reports a stale_issues_jsonl failure when the beads directory
// holds an issues.jsonl in ANY form. lstat is used so a file, a directory, a
// symlink and a dangling symlink all count; issues.jsonl.disabled-* does not.
func CheckStale(beadsDir string) error {
	_, err := os.Lstat(filepath.Join(beadsDir, staleJSONLName))
	switch {
	case err == nil:
		return failure.New(failure.StaleIssuesJSONL, "guard", fmt.Errorf("%s exists in %s", staleJSONLName, beadsDir))
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return failure.New(failure.BDError, "guard", err)
	}
}

// invoke runs one bd subcommand and returns its stdout. The stale guard runs
// first: when it fails, bd is not invoked at all.
func (c *Client) invoke(ctx context.Context, op string, argv []string) ([]byte, error) {
	if err := CheckStale(c.cfg.BeadsDir); err != nil {
		return nil, err
	}
	callCtx := ctx
	if c.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}
	res, err := c.cfg.Runner.Run(callCtx, Cmd{Path: c.cfg.BDPath, Args: argv, Env: c.ChildEnv()})
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, failure.New(failure.Timeout, op, err)
		}
		if ctx.Err() != nil {
			// The caller cancelled (shutdown); not a collection failure.
			return nil, ctx.Err()
		}
		return nil, failure.New(failure.BDError, op, err)
	}
	if res.ExitCode != 0 {
		return nil, failure.New(failure.BDError, op,
			fmt.Errorf("exit %d: %s", res.ExitCode, tail(res.Stderr)))
	}
	return res.Stdout, nil
}

// tail returns a short trimmed stderr excerpt for logs.
func tail(b []byte) string {
	const max = 300
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		s = s[len(s)-max:]
	}
	return s
}

type envelope struct {
	Data          json.RawMessage `json:"data"`
	SchemaVersion *int            `json:"schema_version"`
}

// decodeEnvelope validates the {data, schema_version} envelope and decodes
// data into into. A bare array, or a missing/unknown schema_version, is
// schema skew.
func decodeEnvelope(op string, stdout []byte, into any) error {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return failure.New(failure.ParseError, op, errors.New("empty output"))
	}
	if trimmed[0] == '[' {
		return failure.New(failure.SchemaSkew, op, errors.New("bare array, want envelope"))
	}
	var env envelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return failure.New(failure.ParseError, op, err)
	}
	if env.SchemaVersion == nil {
		return failure.New(failure.SchemaSkew, op, errors.New("missing schema_version"))
	}
	if *env.SchemaVersion != supportedSchemaVersion {
		return failure.New(failure.SchemaSkew, op, fmt.Errorf("schema_version %d unsupported", *env.SchemaVersion))
	}
	if len(env.Data) == 0 {
		return failure.New(failure.ParseError, op, errors.New("missing data"))
	}
	if err := json.Unmarshal(env.Data, into); err != nil {
		return failure.New(failure.ParseError, op, err)
	}
	return nil
}

func (c *Client) beads(ctx context.Context, op string, argv []string) ([]Bead, error) {
	out, err := c.invoke(ctx, op, argv)
	if err != nil {
		return nil, err
	}
	var beads []Bead
	if err := decodeEnvelope(op, out, &beads); err != nil {
		return nil, err
	}
	return beads, nil
}

// List implements Adapter.
func (c *Client) List(ctx context.Context, opts ListOpts) ([]Bead, error) {
	return c.beads(ctx, "list", ListArgv(opts))
}

// Ready implements Adapter.
func (c *Client) Ready(ctx context.Context, extra []string) ([]Bead, error) {
	return c.beads(ctx, "ready", ReadyArgv(extra))
}

// Blocked implements Adapter.
func (c *Client) Blocked(ctx context.Context) ([]Bead, error) {
	return c.beads(ctx, "blocked", BlockedArgv())
}

// CountByStatus implements Adapter.
func (c *Client) CountByStatus(ctx context.Context) (map[string]int, error) {
	out, err := c.invoke(ctx, "count", CountByStatusArgv())
	if err != nil {
		return nil, err
	}
	var data struct {
		Groups []struct {
			Group string `json:"group"`
			Count int    `json:"count"`
		} `json:"groups"`
	}
	if err := decodeEnvelope("count", out, &data); err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(data.Groups))
	for _, g := range data.Groups {
		counts[g.Group] += g.Count
	}
	return counts, nil
}

type statusEntry struct {
	Name string `json:"name"`
}

// Statuses implements Adapter.
func (c *Client) Statuses(ctx context.Context) ([]string, error) {
	out, err := c.invoke(ctx, "statuses", StatusesArgv())
	if err != nil {
		return nil, err
	}
	var data struct {
		Built  []statusEntry `json:"built_in_statuses"`
		Custom []statusEntry `json:"custom_statuses"`
	}
	if err := decodeEnvelope("statuses", out, &data); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(data.Built)+len(data.Custom))
	for _, s := range data.Built {
		names = append(names, s.Name)
	}
	for _, s := range data.Custom {
		names = append(names, s.Name)
	}
	return names, nil
}
