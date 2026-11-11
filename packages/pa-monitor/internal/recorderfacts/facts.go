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

	// scanMu serialises Registrations and guards the carried discovery state
	// below (never read by GC).
	scanMu      sync.Mutex
	endedSeeded bool
	// endedPending holds ended session ids whose registry lookup failed.
	endedPending map[string]bool
	live, ended  []*session.Session
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

// registryLookupWorkers bounds how many per-session registry lookups one Scan
// has in flight; the recorder serves at most 8 query connections at once.
const registryLookupWorkers = 4

// Registrations implements session.Registry for discovery: one row per session
// the recorder knows a Process for, alive Processes first, with PidAlive from
// the Process facts (no kill -0 probe and no ps start-time guard: the recorder
// keys a Process by pid and start time, so a reused pid is a different
// Process).
//
// Each call reads the alive Processes and then one newest registry version per
// LIVE session (kind, entrypoint, waitingFor and the turn-start marker live
// only in the registry file's bytes). Ended Processes are read once, the first
// time after the recorder is reached (and again after a recorder outage or
// ResyncEnded); after that a session that leaves the alive set is carried
// forward as ended, with the registry fields it last had, so a steady-state
// Scan sends no process.list for ended Processes and no registry read for an
// ended session. GC must not use this snapshot: it asks KnownSessionIDs.
func (f *Facts) Registrations(ctx context.Context) ([]*session.Session, error) {
	f.scanMu.Lock()
	defer f.scanMu.Unlock()
	c, err := f.Client(ctx)
	if err != nil {
		f.resetEnded()
		return nil, err
	}
	yes, no := true, false
	alive, err := c.ProcessList(ctx, client.ProcessFilter{Alive: &yes, Limit: 1000})
	if err != nil {
		return nil, f.wrapScan(err)
	}
	var live []*session.Session
	liveSeen := map[string]bool{}
	for _, p := range alive.Processes {
		if p.SessionID == "" || liveSeen[p.SessionID] {
			continue
		}
		liveSeen[p.SessionID] = true
		live = append(live, fromProcess(p))
	}

	var ended []*session.Session // newest first
	if !f.endedSeeded {
		dead, err := c.ProcessList(ctx, client.ProcessFilter{Alive: &no, Limit: maxDeadProcesses})
		if err != nil {
			return nil, f.wrapScan(err)
		}
		for _, p := range dead.Processes {
			if p.SessionID == "" || liveSeen[p.SessionID] || hasID(ended, p.SessionID) {
				continue
			}
			ended = append(ended, fromProcess(p))
		}
		// One registry read per ended session, once, so their stored rows keep
		// kind and entrypoint across a daemon restart.
		// A lookup that fails (a query timeout, the discovery deadline on a long
		// list) is remembered and retried on later Scans, so a failed read does
		// not leave the stored row blank for the life of the daemon.
		f.endedPending = map[string]bool{}
		for i, err := range f.fillRegistry(ctx, c, ended) {
			if err != nil {
				f.endedPending[ended[i].SessionID] = true
			}
		}
	} else {
		ended = f.ended[:0:0]
		var gone []*session.Session
		for _, s := range f.live {
			if !liveSeen[s.SessionID] {
				gone = append(gone, s)
			}
		}
		for _, s := range gone {
			// Carried forward as ended with the facts it last had: no recorder read.
			d := *s
			d.PidAlive = false
			ended = append(ended, &d)
		}
		for _, s := range f.ended {
			if !liveSeen[s.SessionID] && !hasID(ended, s.SessionID) {
				ended = append(ended, s)
			}
		}
		f.retryEndedPending(ctx, c, ended)
	}
	if len(ended) > maxDeadProcesses {
		ended = ended[:maxDeadProcesses]
	}

	if err := f.fillRegistryLive(ctx, c, live); err != nil {
		return nil, err
	}
	f.live, f.ended, f.endedSeeded = live, ended, true

	out := make([]*session.Session, 0, len(live)+len(ended))
	for _, s := range live {
		cp := *s
		out = append(out, &cp)
	}
	for _, s := range ended {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

// endedRetryBatch bounds how many failed ended-session lookups one Scan retries.
const endedRetryBatch = 25

// retryEndedPending re-reads the registry fields of ended sessions whose
// startup lookup failed, newest first, a bounded batch per Scan. It touches
// nothing when no lookup failed, so a healthy steady-state Scan sends no ended
// read.
func (f *Facts) retryEndedPending(ctx context.Context, c *client.Client, ended []*session.Session) {
	if len(f.endedPending) == 0 {
		return
	}
	var batch []*session.Session
	for _, s := range ended {
		if f.endedPending[s.SessionID] {
			batch = append(batch, s)
			if len(batch) == endedRetryBatch {
				break
			}
		}
	}
	for i, err := range f.fillRegistry(ctx, c, batch) {
		if err == nil {
			delete(f.endedPending, batch[i].SessionID)
		}
	}
}

// ResyncEnded makes the next Registrations read the ended Processes again, for
// a caller that knows a Process came and went between two Scans.
func (f *Facts) ResyncEnded() {
	f.scanMu.Lock()
	f.endedSeeded = false
	f.scanMu.Unlock()
}

func (f *Facts) resetEnded() {
	f.endedSeeded = false
	f.live, f.ended = nil, nil
}

// wrapScan is wrap for a scan: an outage also forgets the carried state.
func (f *Facts) wrapScan(err error) error {
	if isOffline(err) {
		f.resetEnded()
	}
	return f.wrap(err)
}

func isOffline(err error) bool {
	return errors.Is(err, client.ErrDown) || errors.Is(err, client.ErrUnavailable)
}

func hasID(ss []*session.Session, id string) bool {
	for _, s := range ss {
		if s.SessionID == id {
			return true
		}
	}
	return false
}

func fromProcess(p client.ProcessInfo) *session.Session {
	s := &session.Session{PID: int(p.Pid), SessionID: p.SessionID, Cwd: p.Cwd, Name: p.Name, RegistryStatus: p.Status, PidAlive: p.Alive}
	if p.StartedAt != nil {
		s.StartedAt = *p.StartedAt
	}
	return s
}

// KnownSessionIDs is what GC keeps: every session id the recorder knows a
// Process for, alive or ended (ended capped at maxDeadProcesses), read fresh on
// every call. It never reads the discovery snapshot, and a failed or degraded
// answer is an error (so the sweep deletes nothing on it).
func (f *Facts) KnownSessionIDs(ctx context.Context) ([]string, error) {
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
	if alive.Degraded || dead.Degraded {
		return nil, errors.New("recorder process list is degraded; not deciding what to keep from a partial answer")
	}
	seen := map[string]bool{}
	var ids []string
	for _, l := range [][]client.ProcessInfo{alive.Processes, dead.Processes} {
		for _, p := range l {
			if p.SessionID != "" && !seen[p.SessionID] {
				seen[p.SessionID] = true
				ids = append(ids, p.SessionID)
			}
		}
	}
	return ids, nil
}

// fillRegistryLive reads the newest registry version of every live session. A
// failed read for one session keeps what that session last had; only a recorder
// that stopped answering fails the scan.
func (f *Facts) fillRegistryLive(ctx context.Context, c *client.Client, live []*session.Session) error {
	prev := map[string]*session.Session{}
	for _, s := range f.live {
		prev[s.SessionID] = s
	}
	errs := f.fillRegistry(ctx, c, live)
	for i, s := range live {
		if errs[i] == nil {
			continue
		}
		if isOffline(errs[i]) {
			return f.wrapScan(errs[i])
		}
		if p := prev[s.SessionID]; p != nil && p.PID == s.PID {
			s.Kind, s.Entrypoint, s.WaitingFor, s.StatusUpdatedAt = p.Kind, p.Entrypoint, p.WaitingFor, p.StatusUpdatedAt
		}
	}
	return nil
}

// fillRegistry runs one limit-1 registry lookup per session and fills the
// fields only the registry bytes carry. It returns each lookup's error.
func (f *Facts) fillRegistry(ctx context.Context, c *client.Client, ss []*session.Session) []error {
	errs := make([]error, len(ss))
	sem := make(chan struct{}, registryLookupWorkers)
	var wg sync.WaitGroup
	for i, s := range ss {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = registryLookup(ctx, c, s)
		}()
	}
	wg.Wait()
	return errs
}

// registryLookup reads the newest registry version of one session, a point
// lookup the (session, source, time) index serves.
func registryLookup(ctx context.Context, c *client.Client, s *session.Session) error {
	id, _ := json.Marshal(s.SessionID)
	ans, err := c.Query(ctx, client.QueryRequest{
		From: "records",
		Where: &client.Filter{All: []client.Filter{
			{Field: "source", Eq: json.RawMessage(`"session-registry"`)},
			{Field: "session", Eq: id},
		}},
		Select: []string{"session"},
		Order:  []client.Order{{By: "time", Dir: "desc"}},
		Raw:    true,
		Limit:  1,
	})
	if err != nil {
		return err
	}
	if len(ans.Rows) == 0 || len(ans.Raw) == 0 {
		return nil
	}
	var x registryExtra
	if json.Unmarshal([]byte(ans.Raw[0]), &x) != nil || (x.PID != 0 && int64(s.PID) != x.PID) {
		return nil
	}
	s.Kind, s.Entrypoint, s.WaitingFor = x.Kind, x.Entrypoint, x.WaitingFor
	if x.StatusUpdatedAt != nil && *x.StatusUpdatedAt > 0 {
		s.StatusUpdatedAt = time.UnixMilli(int64(*x.StatusUpdatedAt))
	}
	return nil
}
