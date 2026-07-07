// Package sync — daemon-side pre-spawn credential gate (Change 1).
//
// Before spawning a review agent, the daemon fetches the PR head itself, and
// that fetch IS the credential check. It first RESOLVES the ssh-agent socket
// that actually holds the ZR certificate: the ambient SSH_AUTH_SOCK points at
// the (empty) macOS-default agent, while the real cert lives in a rotating
// ~/.ssh/agent/s.*.agent.* socket — so the gate probes candidates and caches
// the last good one. A cheap LOCAL cert-validity check (against that socket)
// then picks the path: a valid cert → a fast non-interactive (BatchMode) fetch;
// an expired/absent cert → a single-flight, cooldown-guarded, browser-capable
// fetch (allowing `step` to re-auth once). The resulting FetchOutcome drives
// control flow in reviewhook.go: a credential/network failure leaves the bead
// open for retry and NEVER bumps the dead-letter fail count.
package sync

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	stdsync "sync"
	"time"
)

// FetchOutcome classifies the result of the daemon's pre-spawn credential gate.
type FetchOutcome int

const (
	// FetchOK — the PR head ref is now local; proceed to spawn the reviewer.
	FetchOK FetchOutcome = iota
	// FetchDeferred — a browser-capable fetch is already in flight or the
	// cooldown has not elapsed; leave the bead open and retry next cycle. No
	// second browser prompt is raised.
	FetchDeferred
	// FetchFailed — the fetch failed for a credential/network reason. Leave the
	// bead open and retry next cycle with backoff. MUST NOT bump the
	// dead-letter fail count.
	FetchFailed
	// FetchStepMissing — the `step` binary is not resolvable on the daemon's
	// PATH (a deploy bug). Waiting will not fix it; emit a distinct alert.
	FetchStepMissing
)

// AgentSocketResolver resolves the SSH_AUTH_SOCK path that actually holds a
// usable certificate. On this machine the ambient SSH_AUTH_SOCK points at the
// empty macOS-default agent; the ZR cert lives in one of several rotating
// ~/.ssh/agent/s.*.agent.* sockets and which one holds it is NOT predictable by
// mtime, so the implementation probes each candidate. Injectable so tests never
// touch a real agent.
type AgentSocketResolver interface {
	// Resolve returns the path of an ssh-agent socket whose loaded keys include
	// a certificate, or ("", err) when no cert-bearing socket can be found.
	Resolve(ctx context.Context) (sock string, err error)
}

// CertChecker performs the cheap LOCAL SSH-certificate validity check (no
// network, no hang) against a specific agent socket. Injectable so tests never
// touch a real ssh-agent.
type CertChecker interface {
	// CertValid reports whether the certificate loaded in the agent at authSock
	// is present and currently within its validity window. A non-nil error
	// means the check itself could not run (the gate treats that as "not
	// valid").
	CertValid(ctx context.Context, authSock string) (valid bool, err error)
}

// PRFetcher fetches a PR head ref into the local repo, using the ssh-agent at
// authSock (empty → the ambient SSH_AUTH_SOCK). batchMode selects the
// non-interactive path (ssh BatchMode=yes, short timeout) vs the
// interactive-capable path (allows `step` to re-auth, longer timeout).
type PRFetcher interface {
	FetchPRHead(ctx context.Context, repoDir string, pr int, batchMode bool, authSock string) error
}

// Default timeouts / cooldown for the gate (tunable; see plan open questions).
const (
	defaultBatchTimeout       = 20 * time.Second
	defaultInteractiveTimeout = 180 * time.Second
	defaultBrowserCooldown    = 15 * time.Minute
)

// PreFetchGate is the daemon's pre-spawn credential gate. It is stateful
// (single-flight + cooldown) so browser prompts never pile up.
type PreFetchGate struct {
	// Resolver / Cert / Fetcher are the injectable seams. Required.
	Resolver AgentSocketResolver
	Cert     CertChecker
	Fetcher  PRFetcher

	// BatchTimeout bounds the non-interactive (cert-valid) fetch (T1).
	// InteractiveTimeout bounds the browser-capable fetch (T2).
	// Cooldown is the minimum interval between two browser-capable attempts.
	// Zero values fall back to the package defaults.
	BatchTimeout       time.Duration
	InteractiveTimeout time.Duration
	Cooldown           time.Duration

	// Now is the clock; nil means time.Now (UTC). Tests inject a fake.
	Now func() time.Time

	mu              stdsync.Mutex
	browserInFlight bool
	lastBrowserAt   time.Time
}

func (g *PreFetchGate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now().UTC()
}

func (g *PreFetchGate) batchTimeout() time.Duration {
	if g.BatchTimeout > 0 {
		return g.BatchTimeout
	}
	return defaultBatchTimeout
}

func (g *PreFetchGate) interactiveTimeout() time.Duration {
	if g.InteractiveTimeout > 0 {
		return g.InteractiveTimeout
	}
	return defaultInteractiveTimeout
}

func (g *PreFetchGate) cooldown() time.Duration {
	if g.Cooldown > 0 {
		return g.Cooldown
	}
	return defaultBrowserCooldown
}

// Ensure runs the gate for one PR and returns a classified FetchOutcome.
func (g *PreFetchGate) Ensure(ctx context.Context, repoDir string, pr int) FetchOutcome {
	// Resolve the agent socket that holds the cert (probe + cache). If none can
	// be found (agent down / cert absent), skip the cert check and fall through
	// to the interactive/browser path with the ambient SSH_AUTH_SOCK: the fetch
	// either re-auths or fails cleanly (bead stays open, self-heals when the
	// agent returns).
	sock, rerr := g.Resolver.Resolve(ctx)
	valid := false
	if rerr == nil {
		if v, cerr := g.Cert.CertValid(ctx, sock); cerr == nil {
			valid = v
		}
	} else {
		sock = "" // use the ambient env on the interactive path
	}

	if valid {
		// Valid cert → non-interactive, fast, no browser: no single-flight.
		cctx, cancel := context.WithTimeout(ctx, g.batchTimeout())
		defer cancel()
		return classifyFetch(g.Fetcher.FetchPRHead(cctx, repoDir, pr, true, sock))
	}

	// Expired/absent cert → browser-capable fetch, single-flight + cooldown so
	// prompts never pile up (the user must never return to 100s of windows).
	g.mu.Lock()
	if g.browserInFlight || g.now().Sub(g.lastBrowserAt) < g.cooldown() {
		g.mu.Unlock()
		return FetchDeferred
	}
	g.browserInFlight = true
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.browserInFlight = false
		g.lastBrowserAt = g.now()
		g.mu.Unlock()
	}()

	cctx, cancel := context.WithTimeout(ctx, g.interactiveTimeout())
	defer cancel()
	return classifyFetch(g.Fetcher.FetchPRHead(cctx, repoDir, pr, false, sock))
}

// classifyFetch maps a fetch error to a FetchOutcome. git fetch exits 128 on
// ALL failures, so classification is by stderr substring (surfaced in err) and
// used for logging/alerting only.
func classifyFetch(err error) FetchOutcome {
	if err == nil {
		return FetchOK
	}
	s := err.Error()
	// A missing `step` binary surfaces differently depending on who ran it: the
	// daemon's ~/.ssh/config ProxyCommand runs under /bin/sh ("sh: step: command
	// not found"), an interactive zsh would say "command not found: step", and
	// Go's own exec says "executable file not found". Match those concrete
	// tokens rather than a broad step+not-found predicate (which would also
	// catch unrelated errors).
	if strings.Contains(s, "step: command not found") ||
		strings.Contains(s, "command not found: step") ||
		strings.Contains(s, "executable file not found") {
		return FetchStepMissing
	}
	return FetchFailed
}

// parseCertValidBounds parses the "Valid:" line of `ssh-keygen -L` output and
// returns the certificate's not-before and not-after instants. A cert is usable
// only while now is after not-before AND before not-after (a not-yet-valid cert
// is invalid). "Valid: forever" returns a zero not-before (far past) and a
// far-future not-after. Returns ok=false when no Valid line is present or a
// timestamp won't parse.
func parseCertValidBounds(kgOut string) (notBefore, notAfter time.Time, ok bool) {
	sc := bufio.NewScanner(strings.NewReader(kgOut))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "Valid:") {
			continue
		}
		if strings.Contains(line, "forever") {
			return time.Time{}, time.Now().Add(100 * 365 * 24 * time.Hour), true
		}
		// Format: "Valid: from 2026-07-06T08:00:00 to 2026-07-06T20:00:00".
		fromIdx := strings.Index(line, "from ")
		toIdx := strings.Index(line, " to ")
		if fromIdx < 0 || toIdx < 0 || toIdx <= fromIdx {
			return time.Time{}, time.Time{}, false
		}
		fromTS := strings.TrimSpace(line[fromIdx+len("from ") : toIdx])
		toTS := strings.TrimSpace(line[toIdx+len(" to "):])
		// ssh-keygen prints local time with no zone: 2006-01-02T15:04:05.
		nb, e1 := time.ParseInLocation("2006-01-02T15:04:05", fromTS, time.Local)
		na, e2 := time.ParseInLocation("2006-01-02T15:04:05", toTS, time.Local)
		if e1 != nil || e2 != nil {
			return time.Time{}, time.Time{}, false
		}
		return nb, na, true
	}
	return time.Time{}, time.Time{}, false
}

// firstCertLine returns the first `ssh-add -L` line that carries an OpenSSH
// certificate (its key type contains "-cert-v01@"), or "" when none is present.
func firstCertLine(out string) string {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "-cert-v01@") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// CLIAgentSocketResolver probes candidate ssh-agent sockets for a loaded
// certificate and caches the last good one. The socket only changes when the
// ssh-agent restarts (observed weeks apart), while reviews arrive minutes
// apart, so a cache hit is the overwhelmingly common case. The macOS-default
// agent (ambient SSH_AUTH_SOCK) is empty here, so it is deliberately NOT a
// candidate; only ~/.ssh/agent/s.*.agent.* sockets are probed. The list/hasCert
// seams are injectable (nil → the real glob/probe); the probe-and-cache logic
// is covered by TestCLIAgentSocketResolver_CacheHitThenMiss.
type CLIAgentSocketResolver struct {
	// list returns candidate socket paths. nil → glob $HOME/.ssh/agent/s.*.agent.*.
	list func() ([]string, error)
	// hasCert reports whether the agent at sock exposes a certificate line.
	// nil → run `SSH_AUTH_SOCK=<sock> ssh-add -L` and test with firstCertLine.
	hasCert func(ctx context.Context, sock string) bool

	mu       stdsync.Mutex
	lastGood string
}

// NewCLIAgentSocketResolver returns an AgentSocketResolver backed by a
// filesystem glob + `ssh-add -L` probing, caching the last cert-bearing socket.
func NewCLIAgentSocketResolver() *CLIAgentSocketResolver { return &CLIAgentSocketResolver{} }

func (r *CLIAgentSocketResolver) Resolve(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	list := r.list
	if list == nil {
		list = globAgentSockets
	}
	hasCert := r.hasCert
	if hasCert == nil {
		hasCert = probeSocketCert
	}

	// Cache hit: re-probe ONLY the last good socket (one cheap `ssh-add -L`).
	if r.lastGood != "" && hasCert(ctx, r.lastGood) {
		return r.lastGood, nil
	}

	// Cache miss (cold, or the agent restarted and the socket rotated): probe
	// every candidate and cache + return the first that carries a cert.
	cands, err := list()
	if err != nil {
		r.lastGood = ""
		return "", fmt.Errorf("list agent sockets: %w", err)
	}
	for _, cand := range cands {
		if hasCert(ctx, cand) {
			r.lastGood = cand
			return cand, nil
		}
	}
	r.lastGood = ""
	return "", fmt.Errorf("no cert-bearing ssh-agent socket found among %d candidate(s) under ~/.ssh/agent", len(cands))
}

// globAgentSockets returns the ~/.ssh/agent/s.*.agent.* candidate sockets.
func globAgentSockets() ([]string, error) {
	return filepath.Glob(os.Getenv("HOME") + "/.ssh/agent/s.*.agent.*")
}

// probeSocketCert reports whether `SSH_AUTH_SOCK=<sock> ssh-add -L` lists a
// certificate. Any error (dead socket, no keys loaded) → false.
func probeSocketCert(ctx context.Context, sock string) bool {
	cmd := exec.CommandContext(ctx, "ssh-add", "-L")
	cmd.Env = envWithAuthSock(sock)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false
	}
	return firstCertLine(out.String()) != ""
}

// envWithAuthSock returns os.Environ() with SSH_AUTH_SOCK forced to sock when
// sock is non-empty (a later duplicate wins in exec), plus any extra KEY=VALUE
// entries. An empty sock leaves the ambient SSH_AUTH_SOCK untouched.
func envWithAuthSock(sock string, extra ...string) []string {
	env := os.Environ()
	if sock != "" {
		env = append(env, "SSH_AUTH_SOCK="+sock)
	}
	return append(env, extra...)
}

// CLICertChecker checks a specific agent socket's certificate validity entirely
// locally, via `SSH_AUTH_SOCK=<authSock> ssh-add -L` piped to
// `ssh-keygen -L -f -`. Not unit-tested (needs a live agent); covered by e2e.
type CLICertChecker struct{}

// NewCLICertChecker returns a CertChecker backed by ssh-add / ssh-keygen.
func NewCLICertChecker() *CLICertChecker { return &CLICertChecker{} }

func (c *CLICertChecker) CertValid(ctx context.Context, authSock string) (bool, error) {
	addCmd := exec.CommandContext(ctx, "ssh-add", "-L")
	addCmd.Env = envWithAuthSock(authSock)
	var addOut, addErr bytes.Buffer
	addCmd.Stdout = &addOut
	addCmd.Stderr = &addErr
	if err := addCmd.Run(); err != nil {
		return false, fmt.Errorf("ssh-add -L: %w: %s", err, strings.TrimSpace(addErr.String()))
	}
	certLine := firstCertLine(addOut.String())
	if certLine == "" {
		return false, nil // no cert loaded → treat as expired/absent
	}
	kgCmd := exec.CommandContext(ctx, "ssh-keygen", "-L", "-f", "-")
	kgCmd.Stdin = strings.NewReader(certLine + "\n")
	var kgOut, kgErr bytes.Buffer
	kgCmd.Stdout = &kgOut
	kgCmd.Stderr = &kgErr
	if err := kgCmd.Run(); err != nil {
		return false, fmt.Errorf("ssh-keygen -L: %w: %s", err, strings.TrimSpace(kgErr.String()))
	}
	notBefore, notAfter, ok := parseCertValidBounds(kgOut.String())
	if !ok {
		return false, nil
	}
	// Require now within [notBefore, notAfter): a not-yet-valid cert is invalid.
	now := time.Now()
	return now.After(notBefore) && now.Before(notAfter), nil
}

// CLIPRFetcher fetches a PR head via the system `git` binary, mirroring
// worktree.CLIGitClient.FetchPR's refspec so the pre-fetched ref
// (refs/remotes/origin/pr/<pr>) is exactly what `pg-pr worktree add`'s
// origin/pr/<pr> start point expects. Not unit-tested (needs a remote); e2e.
type CLIPRFetcher struct {
	// ConnectTimeoutSecs is the ssh ConnectTimeout applied on both paths.
	// Zero falls back to defaultConnectTimeoutSecs.
	ConnectTimeoutSecs int
}

// NewCLIPRFetcher returns a PRFetcher backed by the system git binary.
func NewCLIPRFetcher() *CLIPRFetcher { return &CLIPRFetcher{} }

const defaultConnectTimeoutSecs = 10

func (f *CLIPRFetcher) FetchPRHead(ctx context.Context, repoDir string, pr int, batchMode bool, authSock string) error {
	ct := f.ConnectTimeoutSecs
	if ct <= 0 {
		ct = defaultConnectTimeoutSecs
	}
	// Batch mode: fail fast, never prompt (no browser/OIDC). Interactive mode:
	// omit BatchMode so the ~/.ssh/config `step ssh proxycommand ... Dex`
	// ProxyCommand may re-auth (silent if the OIDC session is still valid).
	sshCmd := fmt.Sprintf("ssh -o ConnectTimeout=%d", ct)
	if batchMode {
		sshCmd = fmt.Sprintf("ssh -o BatchMode=yes -o ConnectTimeout=%d", ct)
	}
	refspec := fmt.Sprintf("pull/%d/head:refs/remotes/origin/pr/%d", pr, pr)
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "fetch", "origin", refspec)
	// Thread the resolved agent socket (empty → ambient env) plus the ssh wrapper.
	cmd.Env = envWithAuthSock(authSock, "GIT_SSH_COMMAND="+sshCmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git fetch pull/%d/head (batch=%v): %w: %s",
			pr, batchMode, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Compile-time: the CLI impls satisfy the gate's seams.
var (
	_ AgentSocketResolver = (*CLIAgentSocketResolver)(nil)
	_ CertChecker         = (*CLICertChecker)(nil)
	_ PRFetcher           = (*CLIPRFetcher)(nil)
)
