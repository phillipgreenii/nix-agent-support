package internal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var timeZero time.Time

func TestResolve_KeychainFirstWithConfiguredItem(t *testing.T) {
	kc := &fakeKeychain{pw: "from-kc"}
	b := New(&stubTransport{}, resolver(kc, map[string]string{TokenEnvVar: "from-env"}))
	cfg := baseCfg()
	cfg["keychain_service"] = "svc"
	cfg["keychain_account"] = "acct"
	st := b.transport.(*stubTransport)
	st.incidents = map[string][]apiIncident{"*": nil}
	if _, err := b.List(cfgCtx(cfg), nil, false); err != nil {
		t.Fatal(err)
	}
	if st.tokensSeen[0] != "from-kc" {
		t.Fatalf("token = %q, want keychain value", st.tokensSeen[0])
	}
	if len(kc.calls) != 1 || kc.calls[0] != [2]string{"svc", "acct"} {
		t.Fatalf("keychain calls = %v", kc.calls)
	}
}

func TestResolve_DefaultKeychainItemNames(t *testing.T) {
	kc := &fakeKeychain{pw: "x"}
	if err := New(&stubTransport{}, resolver(kc, nil)).CheckAuth(cfgCtx(baseCfg())); err != nil {
		t.Fatal(err)
	}
	if kc.calls[0] != [2]string{"pd-schedule-manager", "pagerduty-api"} {
		t.Fatalf("default item = %v", kc.calls[0])
	}
}

func TestResolve_EnvFallbackWhenKeychainMissing(t *testing.T) {
	kc := &fakeKeychain{err: errors.New("not found")}
	b := New(&stubTransport{}, resolver(kc, map[string]string{TokenEnvVar: " from-env "}))
	if err := b.CheckAuth(cfgCtx(baseCfg())); err != nil {
		t.Fatalf("CheckAuth = %v, want env fallback to succeed", err)
	}
	tok, err := b.tokens.Resolve(context.Background(), backendConfig{KeychainService: "s", KeychainAccount: "a"})
	if err != nil || tok != "from-env" {
		t.Fatalf("token = %q, %v", tok, err)
	}
}

func TestResolve_DisableKeychainSkipsIt(t *testing.T) {
	kc := &fakeKeychain{pw: "kc"}
	b := New(&stubTransport{}, resolver(kc, map[string]string{TokenEnvVar: "env"}))
	cfg := baseCfg()
	cfg["disable_keychain"] = true
	if err := b.CheckAuth(cfgCtx(cfg)); err != nil {
		t.Fatal(err)
	}
	if len(kc.calls) != 0 {
		t.Fatalf("keychain consulted despite disable_keychain: %v", kc.calls)
	}
}

func TestMissingKeyIsUnauthenticatedAndCheckAuthFails(t *testing.T) {
	kc := &fakeKeychain{err: errors.New("not found")}
	st := &stubTransport{}
	b := New(st, resolver(kc, nil))
	if err := b.CheckAuth(cfgCtx(baseCfg())); err == nil {
		t.Fatal("CheckAuth = nil with no key anywhere")
	}
	ctx := cfgCtx(baseCfg())
	_, err := b.List(ctx, nil, false)
	wantErrIs(t, err, scriptout.ErrUnauthenticated)
	_, err = b.Show(ctx, "pagerduty:P1")
	wantErrIs(t, err, scriptout.ErrUnauthenticated)
	_, err = b.ListAttention(ctx)
	wantErrIs(t, err, scriptout.ErrUnauthenticated)
	if len(st.queries) != 0 {
		t.Fatalf("PagerDuty contacted without a key: %v", st.queries)
	}
	// An empty keychain value also counts as missing.
	b = New(st, resolver(&fakeKeychain{pw: ""}, nil))
	if err := b.CheckAuth(ctx); err == nil {
		t.Fatal("empty keychain value accepted")
	}
}
