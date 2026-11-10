package originguard

import (
	"context"
	"testing"
	"time"
)

func validCert(clock Clock) Cert {
	return Cert{ValidAfter: clock.Now().Add(-time.Hour), ValidBefore: clock.Now().Add(time.Hour)}
}

func newCertChecker(clock Clock, agent fakeAgent, env map[string]string, owners map[string]uint32) *CertChecker {
	return &CertChecker{
		Clock: clock, Dialer: agent,
		Getenv: func(k string) string { return env[k] },
		OwnerUID: func(p string) (uint32, error) {
			if u, ok := owners[p]; ok {
				return u, nil
			}
			return 0, context.Canceled
		},
		UID: func() uint32 { return 501 },
	}
}

func TestCertValidAndClockPastExpiry(t *testing.T) {
	clock := newClock()
	agent := fakeAgent{"/env.sock": {validCert(clock)}}
	cc := newCertChecker(clock, agent, map[string]string{"SSH_AUTH_SOCK": "/env.sock"}, nil)
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonOK {
		t.Fatalf("got %v, want ok", got)
	}
	clock.Advance(2 * time.Hour) // past ValidBefore
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonCertExpired {
		t.Fatalf("got %v, want cert-expired", got)
	}
}

func TestCertNotYetValidIsExpired(t *testing.T) {
	clock := newClock()
	agent := fakeAgent{"/e": {{ValidAfter: clock.Now().Add(time.Hour), ValidBefore: clock.Now().Add(2 * time.Hour)}}}
	cc := newCertChecker(clock, agent, map[string]string{"SSH_AUTH_SOCK": "/e"}, nil)
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonCertExpired {
		t.Fatalf("got %v", got)
	}
}

func TestCertAnyValidCertCounts(t *testing.T) {
	clock := newClock()
	expired := Cert{ValidAfter: clock.Now().Add(-3 * time.Hour), ValidBefore: clock.Now().Add(-time.Hour)}
	forever := Cert{ValidAfter: clock.Now().Add(-time.Hour)} // zero ValidBefore: never expires
	cc := newCertChecker(clock, fakeAgent{"/e": {expired, forever}}, map[string]string{"SSH_AUTH_SOCK": "/e"}, nil)
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonOK {
		t.Fatalf("got %v", got)
	}
}

func TestCertMissingWhenAgentHoldsNone(t *testing.T) {
	cc := newCertChecker(newClock(), fakeAgent{"/e": {}}, map[string]string{"SSH_AUTH_SOCK": "/e"}, nil)
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonCertMissing {
		t.Fatalf("got %v", got)
	}
}

func TestUnsetSSHAuthSockIsAgentUnreachable(t *testing.T) {
	cc := newCertChecker(newClock(), fakeAgent{}, map[string]string{}, nil)
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonAgentUnreachable {
		t.Fatalf("got %v", got)
	}
}

func TestDeadSocketIsAgentUnreachable(t *testing.T) {
	cc := newCertChecker(newClock(), fakeAgent{}, map[string]string{"SSH_AUTH_SOCK": "/gone"}, nil)
	if got := cc.Check(context.Background(), RealmConfig{}); got != ReasonAgentUnreachable {
		t.Fatalf("got %v", got)
	}
}

func TestConfiguredSocketComesFirst(t *testing.T) {
	clock := newClock()
	agent := fakeAgent{"/cfg.sock": {validCert(clock)}, "/env.sock": {}}
	cc := newCertChecker(clock, agent, map[string]string{"SSH_AUTH_SOCK": "/env.sock"}, map[string]uint32{"/cfg.sock": 501})
	rc := RealmConfig{AgentSocket: "/cfg.sock"}
	if got := cc.Check(context.Background(), rc); got != ReasonOK {
		t.Fatalf("configured socket must win: got %v", got)
	}
	if socks := cc.Sockets(rc); len(socks) != 2 || socks[0] != "/cfg.sock" || socks[1] != "/env.sock" {
		t.Fatalf("sockets = %v", socks)
	}
}

func TestConfiguredSocketOwnedByOtherUserIsIgnored(t *testing.T) {
	clock := newClock()
	agent := fakeAgent{"/cfg.sock": {validCert(clock)}}
	cc := newCertChecker(clock, agent, map[string]string{}, map[string]uint32{"/cfg.sock": 0})
	if got := cc.Check(context.Background(), RealmConfig{AgentSocket: "/cfg.sock"}); got != ReasonAgentUnreachable {
		t.Fatalf("foreign-owned configured socket must be ignored: got %v", got)
	}
}

func TestConfiguredSocketFallsBackToEnvWhenDead(t *testing.T) {
	clock := newClock()
	agent := fakeAgent{"/env.sock": {validCert(clock)}}
	cc := newCertChecker(clock, agent, map[string]string{"SSH_AUTH_SOCK": "/env.sock"}, map[string]uint32{"/cfg.sock": 501})
	if got := cc.Check(context.Background(), RealmConfig{AgentSocket: "/cfg.sock"}); got != ReasonOK {
		t.Fatalf("got %v", got)
	}
}
