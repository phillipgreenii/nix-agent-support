package originguard

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// dialTimeout bounds the connect and the list request to a local agent.
const dialTimeout = 2 * time.Second

// SSHAgentDialer lists user certificates over the ssh-agent protocol with
// golang.org/x/crypto/ssh/agent. It only reads: it never signs.
type SSHAgentDialer struct{}

var _ AgentDialer = SSHAgentDialer{}

// ListCerts connects to the unix socket and returns the validity window of
// each user certificate the agent holds. Plain keys and host certificates are
// skipped.
func (SSHAgentDialer) ListCerts(ctx context.Context, socket string) ([]Cert, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))

	keys, err := agent.NewClient(conn).List()
	if err != nil {
		return nil, fmt.Errorf("list agent keys: %w", err)
	}
	var out []Cert
	for _, k := range keys {
		pk, err := ssh.ParsePublicKey(k.Blob)
		if err != nil {
			continue
		}
		cert, ok := pk.(*ssh.Certificate)
		if !ok || cert.CertType != ssh.UserCert {
			continue
		}
		c := Cert{ValidAfter: time.Unix(int64(cert.ValidAfter), 0)}
		if cert.ValidBefore != ssh.CertTimeInfinity {
			c.ValidBefore = time.Unix(int64(cert.ValidBefore), 0)
		}
		out = append(out, c)
	}
	return out, nil
}
