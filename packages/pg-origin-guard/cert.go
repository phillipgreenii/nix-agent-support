package originguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Cert is the validity window of one user certificate held by an agent. A
// zero ValidBefore means the certificate never expires.
type Cert struct {
	ValidAfter  time.Time
	ValidBefore time.Time
}

// ValidAt reports whether the certificate is inside its validity window at t.
func (c Cert) ValidAt(t time.Time) bool {
	if !c.ValidAfter.IsZero() && t.Before(c.ValidAfter) {
		return false
	}
	if !c.ValidBefore.IsZero() && !t.Before(c.ValidBefore) {
		return false
	}
	return true
}

// AgentDialer lists the user certificates an ssh-agent holds. Socket is the
// agent's unix socket path. An error means the agent is unreachable. The real
// implementation reads over golang.org/x/crypto/ssh/agent; tests supply an
// in-process agent or a fake.
type AgentDialer interface {
	ListCerts(ctx context.Context, socket string) ([]Cert, error)
}

// CertChecker is the local certificate check of rule W1. It never uses the
// network and never opens a browser.
type CertChecker struct {
	Clock  Clock
	Dialer AgentDialer
	// Getenv reads SSH_AUTH_SOCK. Nil means os.Getenv.
	Getenv func(string) string
	// OwnerUID returns the owner of path. Nil means stat.
	OwnerUID func(path string) (uint32, error)
	// UID is the current user. Nil means os.Getuid.
	UID func() uint32
	// LaunchdSocket resolves the launchd agent socket at run time. It is the
	// last resort, tried only when no earlier socket answered. Nil disables
	// the fallback; production wires LaunchctlSocket.
	LaunchdSocket func(ctx context.Context) (string, error)
}

// LaunchctlSocket asks launchd for SSH_AUTH_SOCK (`launchctl getenv`). The
// socket directory is random per boot, so it cannot be configured statically.
func LaunchctlSocket(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "launchctl", "getenv", "SSH_AUTH_SOCK").Output()
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", errors.New("launchd has no SSH_AUTH_SOCK")
	}
	return s, nil
}

func (c *CertChecker) getenv(k string) string {
	if c.Getenv != nil {
		return c.Getenv(k)
	}
	return os.Getenv(k)
}

func (c *CertChecker) uid() uint32 {
	if c.UID != nil {
		return c.UID()
	}
	return uint32(os.Getuid())
}

func (c *CertChecker) owner(p string) (uint32, error) {
	if c.OwnerUID != nil {
		return c.OwnerUID(p)
	}
	return statOwner(p)
}

func statOwner(p string) (uint32, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("no owner information for %s", p)
	}
	return st.Uid, nil
}

// Sockets returns the agent sockets to try, in order: the realm's configured
// agent_socket (ignored unless the current user owns it), then SSH_AUTH_SOCK.
// The launchd socket (LaunchdSocket) is tried afterwards by Check.
func (c *CertChecker) Sockets(realm RealmConfig) []string {
	var out []string
	if realm.AgentSocket != "" {
		if uid, err := c.owner(realm.AgentSocket); err == nil && uid == c.uid() {
			out = append(out, realm.AgentSocket)
		}
	}
	if s := c.getenv("SSH_AUTH_SOCK"); s != "" {
		out = append(out, s)
	}
	return out
}

// Check answers ok, cert-missing, cert-expired or agent-unreachable. The
// first socket whose agent answers decides. An agent holding no user
// certificate is cert-missing. One whose certificates are all outside their
// validity window is cert-expired. Any certificate valid now counts: there is
// no principal matching.
func (c *CertChecker) Check(ctx context.Context, realm RealmConfig) Reason {
	socks := c.Sockets(realm)
	tried := map[string]bool{}
	for _, s := range socks {
		tried[s] = true
	}
	// Lazy launchd fallback: a lookup failure or an already-tried socket
	// adds nothing, so the result stays agent-unreachable.
	socks = append(socks, "")
	for _, sock := range socks {
		if sock == "" {
			if c.LaunchdSocket == nil {
				break
			}
			s, err := c.LaunchdSocket(ctx)
			if err != nil || s == "" || tried[s] || !filepath.IsAbs(s) {
				break
			}
			if uid, err := c.owner(s); err != nil || uid != c.uid() {
				break
			}
			sock = s
		}
		certs, err := c.Dialer.ListCerts(ctx, sock)
		if err != nil {
			continue
		}
		if len(certs) == 0 {
			return ReasonCertMissing
		}
		now := c.Clock.Now()
		for _, cert := range certs {
			if cert.ValidAt(now) {
				return ReasonOK
			}
		}
		return ReasonCertExpired
	}
	return ReasonAgentUnreachable
}
