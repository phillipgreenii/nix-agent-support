package beadhandler

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

const (
	// connectorBinary is the ambient PATH name this handler execs; there is
	// no compile-time dependency on packages/pg-connector.
	connectorBinary = "pg-connector"
	// defaultBackend is the backend instance name every call is pinned to
	// unless --backend or a --backend-map entry overrides it (pg-connector can
	// register the beads backend under several suffixed names).
	defaultBackend = "pg-connector-issue-beads"

	// EnvTrackerDir tells a beads backend registered WITHOUT its own
	// --beads-dir which tracker to write to; that backend does not fall back to
	// its cwd. An instance registered with --beads-dir ignores it (the flag
	// wins), so it matters only on the generic default-backend path; see
	// trackerFor.
	EnvTrackerDir = "PG_CONNECTOR_ISSUE_BEADS_DIR"
	// envBeadsDir is the variable such a backend falls back to when
	// EnvTrackerDir is unset. The handler removes it from the child's
	// environment when it hands the backend no tracker of its own.
	envBeadsDir = "BEADS_DIR"
	// EnvActor attributes the backend's writes to this handler's run.
	EnvActor = "PG_CONNECTOR_ISSUE_BEADS_ACTOR"

	// metaFingerprint is the metadata key dedup compares on.
	metaFingerprint = "pg_rescue_fingerprint"
)

// child tracks the one pg-connector process that may be running, so the
// orphan watcher can kill it. The child runs in its own process group, so a
// backend's own children die with it.
type child struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	killed bool
}

func (c *child) set(cmd *exec.Cmd) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cmd = cmd
	if c.killed {
		killGroup(cmd)
	}
}

func (c *child) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cmd = nil
}

// kill ends the running child (and anything it started), and makes any child
// started afterwards die at once.
func (c *child) kill() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killed = true
	if c.cmd != nil {
		killGroup(c.cmd)
	}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// connector runs `pg-connector issue ...` against one tracker.
type connector struct {
	env     []string // the child's complete environment
	child   *child
	backend string // the pg-connector backend instance name every call passes as --backend
}

type connectorResult struct {
	stdout, stderr []byte
	exit           int
}

// run execs pg-connector with args. The error is non-nil only when the child
// could not be started or waited on; a non-zero exit is reported in the result.
func (c *connector) run(args []string) (connectorResult, error) {
	cmd := exec.Command(connectorBinary, args...)
	cmd.Env = c.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return connectorResult{}, fmt.Errorf("cannot run %s: %w", connectorBinary, err)
	}
	c.child.set(cmd)
	err := cmd.Wait()
	c.child.clear()
	res := connectorResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.exit = ee.ExitCode()
	default:
		return connectorResult{}, fmt.Errorf("waiting for %s: %w", connectorBinary, err)
	}
	return res, nil
}

// ok runs args and requires exit 0; anything else is an error carrying the
// child's stderr.
func (c *connector) ok(args []string) ([]byte, error) {
	res, err := c.run(args)
	if err != nil {
		return nil, err
	}
	if res.exit != 0 {
		detail := strings.TrimSpace(string(res.stderr))
		if detail == "" {
			detail = "no error output"
		}
		return nil, fmt.Errorf("pg-connector %s exited %d: %s", args[1], res.exit, detail)
	}
	return res.stdout, nil
}

// pinned appends the flags every call carries.
func (c *connector) pinned(args ...string) []string {
	return append(args, "--backend", c.backend, "--output", "json")
}

// issue is the subset of pg-connector's issue wire shape this handler reads.
type issue struct {
	ID       string            `json:"id"`
	State    string            `json:"state"`
	Metadata map[string]string `json:"metadata"`
}

// list runs the named query and returns its items.
func (c *connector) list(query string) ([]issue, error) {
	out, err := c.ok(c.pinned("issue", "list", "--query", query))
	if err != nil {
		return nil, err
	}
	var env struct {
		Entities []issue `json:"entities"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("cannot decode the issue list response: %w", err)
	}
	return env.Entities, nil
}

// createSpec is everything `issue create` needs.
type createSpec struct {
	title, issueType, description string
	priority                      int
	labels                        []string
	metadata                      map[string]string
}

// create files an item and returns its id.
func (c *connector) create(s createSpec) (string, error) {
	args := []string{
		"issue", "create",
		"--title=" + s.title,
		"--issue-type", s.issueType,
		"--priority", fmt.Sprintf("P%d", s.priority),
		"--description", s.description,
	}
	for _, l := range s.labels {
		args = append(args, "--labels", l)
	}
	for k, v := range s.metadata {
		args = append(args, "--metadata", csvPair(k, v))
	}
	out, err := c.ok(c.pinned(args...))
	if err != nil {
		return "", err
	}
	var env struct {
		Result issue `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return "", fmt.Errorf("cannot decode the issue create response: %w", err)
	}
	if env.Result.ID == "" {
		return "", errors.New("the issue create response carries no id")
	}
	return env.Result.ID, nil
}

// comment adds body to item id.
func (c *connector) comment(id, body string) error {
	_, err := c.ok(c.pinned("issue", "comment", id, "--body", body))
	return err
}

// addLabels adds labels to item id.
func (c *connector) addLabels(id string, labels []string) error {
	args := []string{"issue", "update", id}
	for _, l := range labels {
		args = append(args, "--add-label", l)
	}
	_, err := c.ok(c.pinned(args...))
	return err
}

// csvPair renders k=v as one RFC 4180 field. pg-connector parses --metadata
// as a pflag string-to-string map, which splits a value containing "=" on any
// unquoted comma; quoting the whole token keeps it intact. A token with no
// comma, quote or newline comes back unchanged.
func csvPair(k, v string) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{k + "=" + v}); err != nil {
		return k + "=" + v
	}
	w.Flush()
	return strings.TrimRight(buf.String(), "\n")
}
