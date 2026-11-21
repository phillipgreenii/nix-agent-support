package beadhandler

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
	"github.com/phillipgreenii/x/cmdrun"
)

const (
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

// runner is the cmdrun.Runner the published client runs pg-connector through.
// It keeps this handler's own process management: the child's complete
// environment is c.env (never the parent's), it runs in its own process group,
// and it is registered with c.child so the orphan watcher can kill it. A
// timeout kills the whole group. seen receives the child's captured stderr and
// exit code so a failure can name them and a caller can insist on exit 0.
func (c *connector) runner(seen *seenCall) cmdrun.Runner {
	return func(ctx context.Context, call cmdrun.Call) cmdrun.Result {
		cmd := exec.CommandContext(ctx, call.Name, call.Args...)
		cmd.Cancel = func() error { killGroup(cmd); return nil }
		cmd.Env = c.env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Start(); err != nil {
			return cmdrun.Result{Code: -1, StartErr: fmt.Errorf("cannot run %s: %w", call.Name, err)}
		}
		c.child.set(cmd)
		var timedOut atomic.Bool
		if call.Timeout > 0 {
			t := time.AfterFunc(call.Timeout, func() {
				timedOut.Store(true)
				killGroup(cmd)
			})
			defer t.Stop()
		}
		err := cmd.Wait()
		c.child.clear()
		res := cmdrun.Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
		defer func() { seen.stderr, seen.exit = res.Stderr, res.Code }()
		var ee *exec.ExitError
		switch {
		case err == nil:
		case timedOut.Load():
			res.Code, res.Signal, res.TimedOut, res.CtxErr = -1, "killed", true, context.DeadlineExceeded
		case errors.As(err, &ee) && ee.ExitCode() >= 0:
			res.Code = ee.ExitCode()
		case errors.As(err, &ee):
			res.Code, res.Signal = -1, ee.String()
		default:
			res.Code, res.StartErr = -1, fmt.Errorf("waiting for %s: %w", call.Name, err)
		}
		return res
	}
}

// seenCall is what the runner observed of the last child: its stderr and exit.
type seenCall struct {
	stderr []byte
	exit   int
}

// do runs f against a client wired to this connector's runner. A failed call
// that ended with an exit code is reported with the child's stderr (the
// client's own error carries only stdout); any other failure (the child
// never started, a signal, a timeout) is returned as the client reported it.
func (c *connector) do(verb string, f func(cl client.Client, seen *seenCall) error) error {
	var seen seenCall
	err := f(client.Client{Run: c.runner(&seen)}, &seen)
	if err == nil {
		return nil
	}
	var ce *client.Error
	if errors.As(err, &ce) && ce.ExitCode >= 0 {
		detail := strings.TrimSpace(string(seen.stderr))
		if detail == "" {
			detail = "no error output"
		}
		return fmt.Errorf("pg-connector %s exited %d: %s", verb, ce.ExitCode, detail)
	}
	return err
}

// targeted runs one single-backend verb (args after the verb are pinned) and
// returns the answer envelope's result.
func (c *connector) targeted(args []string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(args[1], func(cl client.Client, _ *seenCall) (err error) {
		out, err = cl.Targeted(context.Background(), c.pinned(args...)...)
		return err
	})
	return out, err
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

// list runs the named query and returns its items. Only exit 0 counts: a
// degraded answer (pg-connector exit 5) is an error here, since dedup must not
// read a partial list as "nothing filed".
func (c *connector) list(query string) ([]issue, error) {
	var out client.ListOutcome[issue]
	err := c.do("list", func(cl client.Client, seen *seenCall) error {
		raw, err := cl.FanOutStdout(context.Background(), c.pinned("issue", "list", "--query", query)...)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("cannot decode the issue list response: %w", err)
		}
		if seen.exit != 0 {
			return client.NewExitError([]string{"issue", "list"}, seen.exit, raw)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out.Entities, nil
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
	out, err := c.targeted(args)
	if err != nil {
		return "", err
	}
	var created issue
	if err := json.Unmarshal(out, &created); err != nil {
		return "", fmt.Errorf("cannot decode the issue create response: %w", err)
	}
	if created.ID == "" {
		return "", errors.New("the issue create response carries no id")
	}
	return created.ID, nil
}

// comment adds body to item id.
func (c *connector) comment(id, body string) error {
	_, err := c.targeted([]string{"issue", "comment", id, "--body", body})
	return err
}

// addLabels adds labels to item id.
func (c *connector) addLabels(id string, labels []string) error {
	args := []string{"issue", "update", id}
	for _, l := range labels {
		args = append(args, "--add-label", l)
	}
	_, err := c.targeted(args)
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
