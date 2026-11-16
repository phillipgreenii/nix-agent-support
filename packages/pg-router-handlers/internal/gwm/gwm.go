// Package gwm is the handler's client for the pg-gwm worktree manager. It wraps
// the pg-gwm CLI (lease, renew, release, expire) over x/cmdrun, passes
// --repo <RepoRoot> --format=json on every call, and turns a refusal, read from
// the JSON error envelope on stdout, into an error that matches one of the Err*
// sentinels (errors.Is) and carries the envelope's message, hint and details.
// pg-gwm's standard error is written to the handler log.
package gwm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// Key names a lease: by its id, or by its name when LeaseID is empty.
type Key struct{ LeaseID, Name string }

// LeaseRequest is one pg-gwm lease call. Branch with Create passes -b Branch;
// Branch with Detach passes --detach Branch; Detach alone passes --detach; a
// Branch alone is the commit-ish to check out.
type LeaseRequest struct {
	Repo, Name, Owner string
	TTL               time.Duration
	Metadata          map[string]string
	Branch            string
	Create, Detach    bool
}

// Lease is the record pg-gwm lease returned. Head is the worktree's commit at
// the time of the call (the lease's starting commit on a first lease).
type Lease struct {
	ID, LeaseID, Path, Branch, Name, Head string
	Renewed                               bool
}

// Client is the handler's view of pg-gwm.
type Client interface {
	Lease(ctx context.Context, r LeaseRequest) (Lease, error)
	Renew(ctx context.Context, leaseID string, ttl time.Duration) error
	Release(ctx context.Context, k Key) error
	Expire(ctx context.Context, k Key) error
}

// The refusals pg-gwm answers with, one per exit code.
var (
	ErrMaxReached      = errors.New("pg-gwm: max worktrees reached") // 20
	ErrNameHeld        = errors.New("pg-gwm: lease name held")       // 21
	ErrBranchInUse     = errors.New("pg-gwm: branch in use")         // 22
	ErrDirty           = errors.New("pg-gwm: worktree dirty")        // 23
	ErrLeaseReleased   = errors.New("pg-gwm: lease released")        // 24
	ErrLeaseLost       = errors.New("pg-gwm: lease lost")            // 25
	ErrRefUnresolvable = errors.New("pg-gwm: ref unresolvable")      // 26
	ErrBroken          = errors.New("pg-gwm: worktree broken")       // 28
	ErrLowDisk         = errors.New("pg-gwm: low disk")              // 29
	ErrBusy            = errors.New("pg-gwm: busy")                  // 9
	ErrNotFound        = errors.New("pg-gwm: not found")             // 4
	ErrUsage           = errors.New("pg-gwm: usage")                 // 2
	errOther           = errors.New("pg-gwm: failed")                // anything else
)

var byCode = map[int]error{
	20: ErrMaxReached, 21: ErrNameHeld, 22: ErrBranchInUse, 23: ErrDirty,
	24: ErrLeaseReleased, 25: ErrLeaseLost, 26: ErrRefUnresolvable, 28: ErrBroken,
	29: ErrLowDisk, 9: ErrBusy, 4: ErrNotFound, 2: ErrUsage,
}

// Error is a refusal (or other failure) of one pg-gwm call.
type Error struct {
	Sentinel error
	Code     int
	// Name, Message, Hint and Details are the JSON error envelope's fields.
	Name, Message, Hint string
	Details             map[string]any
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s (exit %d)", e.Sentinel.Error(), e.Code)
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Hint != "" {
		s += " (hint: " + e.Hint + ")"
	}
	return s
}

func (e *Error) Unwrap() error { return e.Sentinel }

// Holder describes who holds what the call asked for, from the envelope's
// details: a path and name (branch in use), a lease id and status (name held),
// or a command (busy). Empty when the details name no holder.
func (e *Error) Holder() string {
	var parts []string
	for _, k := range []string{"path", "name", "lease_id", "status", "command"} {
		if v, ok := e.Details[k]; ok && fmt.Sprint(v) != "" {
			parts = append(parts, k+"="+fmt.Sprint(v))
		}
	}
	return strings.Join(parts, " ")
}

// AsError returns the *Error inside err, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	return e, errors.As(err, &e)
}

// FormatTTL renders d in pg-gwm's grammar: a whole number of d, h, m or s,
// using the largest unit that divides d exactly. A duration that is not whole
// seconds is rounded up to the next second; a non-positive one is "none".
func FormatTTL(d time.Duration) string {
	if d <= 0 {
		return "none"
	}
	if r := d % time.Second; r != 0 {
		d += time.Second - r
	}
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

// ParseTTL parses pg-gwm's duration grammar (<int><s|m|h|d>); Go's
// time.ParseDuration has no d.
func ParseTTL(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("duration %q: want <int><s|m|h|d>", s)
	}
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("duration %q: want <int><s|m|h|d>", s)
	}
	unit := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("duration %q: want <int><s|m|h|d>", s)
	}
	return time.Duration(n) * unit, nil
}

// CLI runs the pg-gwm binary through Exec.
type CLI struct {
	// Exec runs a call; nil means cmdrun.Exec.
	Exec cmdrun.Runner
	// Bin is the binary name or path; empty means "pg-gwm".
	Bin string
	// RepoRoot is the canonical clone passed as --repo on every call.
	RepoRoot string
	// Timeout bounds one call; zero means two minutes.
	Timeout time.Duration
}

var _ Client = (*CLI)(nil)

func parseRefusal(verb string, res cmdrun.Result) error {
	e := &Error{Sentinel: errOther, Code: res.Code}
	if s, ok := byCode[res.Code]; ok {
		e.Sentinel = s
	}
	var env struct {
		Error struct {
			Name    string         `json:"name"`
			Message string         `json:"message"`
			Hint    string         `json:"hint"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(res.Stdout, &env) == nil {
		e.Name, e.Message, e.Hint, e.Details = env.Error.Name, env.Error.Message, env.Error.Hint, env.Error.Details
	}
	if e.Message == "" {
		e.Message = verb + " failed: " + strings.TrimSpace(string(res.Stderr))
	}
	return e
}

func metaArgs(m map[string]string) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	first := map[string]int{"role": 0, "bead": 1, "session": 2}
	sort.Slice(keys, func(i, j int) bool {
		a, aok := first[keys[i]]
		b, bok := first[keys[j]]
		switch {
		case aok && bok:
			return a < b
		case aok != bok:
			return aok
		}
		return keys[i] < keys[j]
	})
	var out []string
	for _, k := range keys {
		out = append(out, "--metadata", k+"="+m[k])
	}
	return out
}

// Lease runs pg-gwm lease --or-renew for r.Name.
func (c *CLI) Lease(ctx context.Context, r LeaseRequest) (Lease, error) {
	args := []string{"lease", "--name", r.Name, "--or-renew"}
	if r.Owner != "" {
		args = append(args, "--owner", r.Owner)
	}
	args = append(args, "--ttl", FormatTTL(r.TTL))
	args = append(args, metaArgs(r.Metadata)...)
	switch {
	case r.Create && r.Branch != "":
		args = append(args, "-b", r.Branch)
	case r.Detach:
		args = append(args, "--detach")
	}
	// The repo and format flags go before the positional commit-ish.
	repoFmt := []string{"--repo", c.RepoRoot, "--format=json"}
	args = append(args, repoFmt...)
	if r.Branch != "" && !r.Create {
		args = append(args, r.Branch)
	}
	out, err := c.run(ctx, args)
	if err != nil {
		return Lease{}, err
	}
	var rec struct {
		ID           string  `json:"id"`
		LeaseID      *string `json:"lease_id"`
		WorktreePath string  `json:"worktree_path"`
		Branch       *string `json:"branch"`
		Name         *string `json:"name"`
		Head         string  `json:"head"`
		Renewed      *bool   `json:"renewed"`
	}
	if err := json.Unmarshal(out, &rec); err != nil {
		return Lease{}, fmt.Errorf("pg-gwm lease: decode record: %w", err)
	}
	l := Lease{ID: rec.ID, Path: rec.WorktreePath, Head: rec.Head}
	if rec.LeaseID != nil {
		l.LeaseID = *rec.LeaseID
	}
	if rec.Branch != nil {
		l.Branch = *rec.Branch
	}
	if rec.Name != nil {
		l.Name = *rec.Name
	}
	l.Renewed = rec.Renewed != nil && *rec.Renewed
	return l, nil
}

func (c *CLI) run(ctx context.Context, args []string) ([]byte, error) {
	exec := c.Exec
	if exec == nil {
		exec = cmdrun.Exec
	}
	bin := c.Bin
	if bin == "" {
		bin = "pg-gwm"
	}
	to := c.Timeout
	if to <= 0 {
		to = 2 * time.Minute
	}
	res := exec(ctx, cmdrun.Call{Name: bin, Args: args, Timeout: to})
	if len(res.Stderr) > 0 {
		slog.Info("pg-gwm stderr", "cmd", args[0], "stderr", strings.TrimSpace(string(res.Stderr)))
	}
	switch {
	case res.StartErr != nil:
		return nil, fmt.Errorf("pg-gwm %s: %w", args[0], res.StartErr)
	case res.TimedOut:
		return nil, fmt.Errorf("pg-gwm %s: timed out after %s", args[0], to)
	case res.CtxErr != nil:
		return nil, fmt.Errorf("pg-gwm %s: %w", args[0], res.CtxErr)
	case res.Code != 0 || res.Signal != "":
		return nil, parseRefusal(args[0], res)
	}
	return res.Stdout, nil
}

// Renew runs pg-gwm renew <lease id> --ttl <ttl>.
func (c *CLI) Renew(ctx context.Context, leaseID string, ttl time.Duration) error {
	_, err := c.run(ctx, []string{"renew", "--ttl", FormatTTL(ttl), "--repo", c.RepoRoot, "--format=json", leaseID})
	return err
}

// Release runs pg-gwm release by lease id, or by name when LeaseID is empty.
func (c *CLI) Release(ctx context.Context, k Key) error { return c.byKey(ctx, "release", k) }

// Expire runs pg-gwm expire by lease id, or by name when LeaseID is empty.
func (c *CLI) Expire(ctx context.Context, k Key) error { return c.byKey(ctx, "expire", k) }

func (c *CLI) byKey(ctx context.Context, verb string, k Key) error {
	args := []string{verb}
	if k.LeaseID == "" {
		args = append(args, "--name", k.Name)
	}
	args = append(args, "--repo", c.RepoRoot, "--format=json")
	if k.LeaseID != "" {
		args = append(args, k.LeaseID)
	}
	_, err := c.run(ctx, args)
	return err
}
