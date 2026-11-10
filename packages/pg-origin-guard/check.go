package originguard

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// RemoteResolver returns the origin URL of the repository at repoPath. It
// MUST NOT use the network.
type RemoteResolver func(ctx context.Context, repoPath string) (string, error)

// GitRemoteResolver reads remote.origin.url with the local git binary. It
// reads local config only.
func GitRemoteResolver(ctx context.Context, repoPath string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", fmt.Errorf("read origin of %s: %w", repoPath, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Checker is the read-only Guard: realm ignored, then the local cert check,
// then the recorded breaker state, then (for a RepoPath) the .git check. It
// never uses the network and never logs in.
type Checker struct {
	Config  Config
	Clock   Clock
	Store   *Store
	Certs   *CertChecker
	Rec     *Recorder
	Resolve RemoteResolver
	// OnRecordError receives a state or event write failure. Nil drops it.
	OnRecordError func(error)
}

var _ Guard = (*Checker)(nil)

// Check implements Guard.
func (c *Checker) Check(ctx context.Context, req Request) (Result, error) {
	realm, mountMissing, err := c.resolveRealm(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if mountMissing {
		return Result{Available: false, Reason: ReasonMountMissing}, nil
	}
	rc := c.Config.Realms[realm]

	if c.Store.Ignored(realm) {
		return Result{Realm: realm, Available: true, Reason: ReasonIgnored}, nil
	}

	reason := c.Certs.Check(ctx, rc)
	st, _ := c.Store.Load(realm)
	if reason == ReasonOK && st.BreakerOpen(c.Clock.Now()) {
		reason = breakerReason(st.Class)
	}

	rec, rerr := c.Rec.Record(realm, Observation{Reason: reason})
	if rerr != nil && c.OnRecordError != nil {
		c.OnRecordError(rerr)
	}
	if rerr != nil {
		rec = st // the store may have failed; fall back to what was read
	}

	res := Result{Realm: realm, Available: reason == ReasonOK, Reason: reason}
	if !res.Available {
		res.Since = rec.Since
		if res.Since.IsZero() {
			res.Since = c.Clock.Now()
		}
		res.LastError = rec.LastError
	}

	return res, nil
}

func breakerReason(class string) Reason {
	switch r := Reason(class); r {
	case ReasonNetwork, ReasonTimeout, ReasonUnknown:
		return r
	}
	return ReasonUnknown
}

func gitDirExists(repo string) bool {
	_, err := os.Lstat(filepath.Join(repo, ".git"))
	return err == nil
}

// resolveRealm picks the realm of the request. For a RepoPath whose .git is
// missing it reports mountMissing, since the origin cannot be read.
func (c *Checker) resolveRealm(ctx context.Context, req Request) (realm string, mountMissing bool, err error) {
	n := 0
	for _, s := range []string{req.Realm, req.Remote, req.RepoPath} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return "", false, &UsageError{Msg: "exactly one of realm, remote and repo path is required"}
	}
	switch {
	case req.Realm != "":
		if _, ok := c.Config.Realms[req.Realm]; !ok {
			return "", false, &UsageError{Msg: fmt.Sprintf("unknown realm %q", req.Realm)}
		}
		return req.Realm, false, nil
	case req.Remote != "":
		r, err := c.matchRemote(req.Remote)
		return r, false, err
	default:
		if !gitDirExists(req.RepoPath) {
			return "", true, nil
		}
		resolve := c.Resolve
		if resolve == nil {
			resolve = GitRemoteResolver
		}
		remote, err := resolve(ctx, req.RepoPath)
		if err != nil {
			return "", false, err
		}
		r, err := c.matchRemote(remote)
		return r, false, err
	}
}

func (c *Checker) matchRemote(remote string) (string, error) {
	host, user, ok := ParseRemote(remote)
	if !ok {
		return "", &UsageError{Msg: "cannot read a host from the remote"}
	}
	names := make([]string, 0, len(c.Config.Realms))
	for n := range c.Config.Realms {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := c.Config.Realms[n]
		if r.MatchHost == "" && r.MatchUser == "" {
			continue
		}
		if globMatch(r.MatchHost, host) && globMatch(r.MatchUser, user) {
			return n, nil
		}
	}
	return "", &UsageError{Msg: "no realm matches the remote"}
}

func globMatch(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

// ParseRemote returns the host and user of a git remote: a URL
// (ssh://user@host/..., https://host/...) or scp-like user@host:path.
func ParseRemote(remote string) (host, user string, ok bool) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		return u.Hostname(), u.User.Username(), true
	}
	hostPart, _, found := strings.Cut(remote, ":")
	if !found || strings.Contains(hostPart, "/") {
		return "", "", false
	}
	if u, h, has := strings.Cut(hostPart, "@"); has {
		return h, u, h != ""
	}
	return hostPart, "", hostPart != ""
}

// NewChecker wires the production dependencies from cfg and env.
func NewChecker(cfg Config, env Env, dialer AgentDialer, clock Clock) *Checker {
	store := NewStore(env.StateDir(), clock, cfg.MaxBackoff())
	sink := &FileSink{Dir: env.StateDir(), Clock: clock, RetentionDays: cfg.Events.RetentionDays}
	return &Checker{
		Config: cfg,
		Clock:  clock,
		Store:  store,
		Certs:  &CertChecker{Clock: clock, Dialer: dialer, Getenv: env.Getenv},
		Rec:    &Recorder{Store: store, Sink: sink, Clock: clock, HeartbeatInterval: cfg.Events.HeartbeatInterval},
	}
}
