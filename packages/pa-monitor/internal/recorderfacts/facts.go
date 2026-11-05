// Package recorderfacts is pa-monitor's one reader of Claude Code's facts: the
// registrations of the running Claude processes, whether each is alive, and the
// sessions and search results the CLIs print. It reads them through the
// recorder's Go client package and never from Claude's files; while the recorder
// is not answering it reports session.ErrDaemonOffline ("the recorder is not
// available") and does not fall back to guessing from files.
package recorderfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// MinPollInterval is the fastest pa-monitor asks the recorder for facts: the
// design polls the change feed every 5 to 10 seconds (Push or poll).
const MinPollInterval = 5 * time.Second

// maxDeadProcesses bounds how many ended Processes one discovery reads.
const maxDeadProcesses = 500

// Facts reads the recorder. It dials lazily and is safe for concurrent use.
type Facts struct {
	opts client.Options

	mu sync.Mutex
	c  *client.Client
}

// New returns a reader that discovers the daemon's socket from opts (the
// environment variable, then the default path, when opts.Socket is empty).
func New(opts client.Options) *Facts {
	if opts.Name == "" {
		opts.Name = "pa-monitor"
	}
	return &Facts{opts: opts}
}

// Client returns the connected recorder client, dialing when not yet connected.
// A daemon that is not answering is session.ErrDaemonOffline.
func (f *Facts) Client(ctx context.Context) (*client.Client, error) {
	f.mu.Lock()
	if f.c != nil {
		c := f.c
		f.mu.Unlock()
		return c, nil
	}
	f.mu.Unlock()
	c, err := client.Dial(ctx, f.opts)
	if err != nil {
		return nil, f.wrap(err)
	}
	f.mu.Lock()
	f.c = c
	f.mu.Unlock()
	return c, nil
}

// wrap turns a transport-level "not answering" into session.ErrDaemonOffline and
// drops the cached connection so the next call dials again.
func (f *Facts) wrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, client.ErrDown) || errors.Is(err, client.ErrUnavailable) {
		f.mu.Lock()
		f.c = nil
		f.mu.Unlock()
		return session.ErrDaemonOffline
	}
	return err
}

// Socket is the recorder socket this reader dials: the explicit option, else the
// environment variable or default path the client would discover.
func (f *Facts) Socket() string {
	if f.opts.Socket != "" {
		return f.opts.Socket
	}
	return client.DefaultSocket(f.opts.Getenv)
}

// Unavailable words a failed recorder-backed command: when err says the
// recorder is not answering it names what could not be done and the socket,
// "cannot <action>: the recorder is not available (socket <path>)"; any other
// error is returned unchanged.
func (f *Facts) Unavailable(action string, err error) error {
	if errors.Is(err, session.ErrDaemonOffline) {
		return fmt.Errorf("cannot %s: %w (socket %s)", action, err, f.Socket())
	}
	return err
}

// Wrap is wrap for callers that run their own operations on Client.
func (f *Facts) Wrap(err error) error { return f.wrap(err) }

// registryExtra is what only the registry file's own bytes carry.
type registryExtra struct {
	PID             int64    `json:"pid"`
	Kind            string   `json:"kind"`
	Entrypoint      string   `json:"entrypoint"`
	WaitingFor      string   `json:"waitingFor"`
	StatusUpdatedAt *float64 `json:"statusUpdatedAt"`
}

// Registrations implements session.Registry: one row per session the recorder
// knows a Process for, alive Processes first, with PidAlive from the Process
// facts (no kill -0 probe and no ps start-time guard: the recorder keys a
// Process by pid and start time, so a reused pid is a different Process).
func (f *Facts) Registrations(ctx context.Context) ([]*session.Session, error) {
	c, err := f.Client(ctx)
	if err != nil {
		return nil, err
	}
	yes, no := true, false
	alive, err := c.ProcessList(ctx, client.ProcessFilter{Alive: &yes, Limit: 1000})
	if err != nil {
		return nil, f.wrap(err)
	}
	dead, err := c.ProcessList(ctx, client.ProcessFilter{Alive: &no, Limit: maxDeadProcesses})
	if err != nil {
		return nil, f.wrap(err)
	}
	var out []*session.Session
	seen := map[string]bool{}
	var ids []any
	add := func(alive bool, pid int64, sid, name, cwd, status string, started *time.Time) {
		if sid == "" || seen[sid] {
			return
		}
		seen[sid] = true
		s := &session.Session{PID: int(pid), SessionID: sid, Cwd: cwd, Name: name, RegistryStatus: status, PidAlive: alive}
		if started != nil {
			s.StartedAt = *started
		}
		out = append(out, s)
		ids = append(ids, sid)
	}
	for _, p := range alive.Processes {
		add(p.Alive, p.Pid, p.SessionID, p.Name, p.Cwd, p.Status, p.StartedAt)
	}
	for _, p := range dead.Processes {
		add(p.Alive, p.Pid, p.SessionID, p.Name, p.Cwd, p.Status, p.StartedAt)
	}
	if len(ids) == 0 {
		return out, nil
	}
	// Kind, entrypoint, waitingFor and the turn-start marker live only in the
	// registry file's bytes: read the newest versions of the sessions found.
	ans, err := c.Query(ctx, client.QueryRequest{
		From: "records",
		Where: &client.Filter{All: []client.Filter{
			{Field: "source", Eq: json.RawMessage(`"session-registry"`)},
			{Field: "session", In: ids},
		}},
		Select: []string{"session"},
		Order:  []client.Order{{By: "time", Dir: "desc"}},
		Raw:    true,
		Limit:  5000,
	})
	if err != nil {
		return nil, f.wrap(err)
	}
	done := map[string]bool{}
	for i, row := range ans.Rows {
		if i >= len(ans.Raw) || len(row) == 0 {
			continue
		}
		sid, _ := row[0].(string)
		if done[sid] {
			continue
		}
		var x registryExtra
		if json.Unmarshal([]byte(ans.Raw[i]), &x) != nil {
			continue
		}
		for _, s := range out {
			if s.SessionID != sid || (x.PID != 0 && int64(s.PID) != x.PID) {
				continue
			}
			done[sid] = true
			s.Kind, s.Entrypoint, s.WaitingFor = x.Kind, x.Entrypoint, x.WaitingFor
			if x.StatusUpdatedAt != nil && *x.StatusUpdatedAt > 0 {
				s.StatusUpdatedAt = time.UnixMilli(int64(*x.StatusUpdatedAt))
			}
		}
	}
	return out, nil
}
