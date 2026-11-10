package originguard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// startAgent serves an in-process keyring on a unix socket and returns the
// socket path and the keyring.
func startAgent(t *testing.T) (string, agent.Agent) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	kr := agent.NewKeyring()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(kr, c); _ = c.Close() }()
		}
	}()
	return sock, kr
}

func newSigner(t *testing.T) (ssh.Signer, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s, k
}

// addCert signs a user (or host) certificate with a throwaway CA and loads it
// into the agent.
func addCert(t *testing.T, kr agent.Agent, certType uint32, after, before uint64) {
	t.Helper()
	ca, _ := newSigner(t)
	userSigner, userKey := newSigner(t)
	cert := &ssh.Certificate{
		Key: userSigner.PublicKey(), CertType: certType, KeyId: "test",
		ValidPrincipals: []string{"tester"}, ValidAfter: after, ValidBefore: before,
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	if err := kr.Add(agent.AddedKey{PrivateKey: userKey, Certificate: cert}); err != nil {
		t.Fatal(err)
	}
}

func TestRealAgentCertWithClockPastExpiry(t *testing.T) {
	sock, kr := startAgent(t)
	clock := newClock()
	addCert(t, kr, ssh.UserCert, uint64(clock.Now().Add(-time.Hour).Unix()), uint64(clock.Now().Add(time.Hour).Unix()))

	cc := &CertChecker{
		Clock: clock, Dialer: SSHAgentDialer{},
		Getenv: func(k string) string {
			if k == "SSH_AUTH_SOCK" {
				return sock
			}
			return ""
		},
	}
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonOK {
		t.Fatalf("got %v, want ok", got)
	}
	clock.Advance(2 * time.Hour)
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonCertExpired {
		t.Fatalf("got %v, want cert-expired", got)
	}
}

func TestRealAgentEmptyAndHostCertIsMissing(t *testing.T) {
	sock, kr := startAgent(t)
	got, err := SSHAgentDialer{}.ListCerts(context.Background(), sock)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty agent: %v %v", got, err)
	}
	// a plain key and a host certificate are not user certificates
	_, k := newSigner(t)
	if err := kr.Add(agent.AddedKey{PrivateKey: k}); err != nil {
		t.Fatal(err)
	}
	addCert(t, kr, ssh.HostCert, 0, ssh.CertTimeInfinity)
	got, err = SSHAgentDialer{}.ListCerts(context.Background(), sock)
	if err != nil || len(got) != 0 {
		t.Fatalf("plain key and host cert must be skipped: %v %v", got, err)
	}
}

func TestRealAgentInfiniteCertNeverExpires(t *testing.T) {
	sock, kr := startAgent(t)
	addCert(t, kr, ssh.UserCert, 0, ssh.CertTimeInfinity)
	got, err := SSHAgentDialer{}.ListCerts(context.Background(), sock)
	if err != nil || len(got) != 1 || !got[0].ValidBefore.IsZero() || !got[0].ValidAt(t0.AddDate(50, 0, 0)) {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestRealAgentUnreachable(t *testing.T) {
	if _, err := (SSHAgentDialer{}).ListCerts(context.Background(), filepath.Join(t.TempDir(), "none.sock")); err == nil {
		t.Fatal("want an error for a missing socket")
	}
}
