package status

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenguard"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/keychain"
)

type fakeToken struct{ st tokenguard.State }

func (f *fakeToken) State() tokenguard.State { return f.st }

// degradedGolden is the exact reason line, the exact text-form auth block and
// the JSON auth object of each degraded reason.
var degradedGolden = []struct {
	reason, line, text, json string
}{
	{
		reason: "token_rejected",
		line:   "unauthenticated: Jira rejected the token and the keychain holds the same token: rotate the zr-jira keychain item, then restart the daemon; restart the daemon to recover",
		text:   "auth: ok\n  identity: operator@example.test\n  degraded_reason: token_rejected\n",
		json:   `{"state":"ok","identity":"operator@example.test","degraded_reason":"token_rejected"}`,
	},
	{
		reason: "token_unavailable",
		line:   "unauthenticated: the keychain read failed (item missing, locked or timed out); restart the daemon to recover",
		text:   "auth: ok\n  identity: operator@example.test\n  degraded_reason: token_unavailable\n",
		json:   `{"state":"ok","identity":"operator@example.test","degraded_reason":"token_unavailable"}`,
	},
	{
		reason: "token_file",
		line:   "unauthenticated: the token file could not be written; restart the daemon to recover",
		text:   "auth: ok\n  identity: operator@example.test\n  degraded_reason: token_file\n",
		json:   `{"state":"ok","identity":"operator@example.test","degraded_reason":"token_file"}`,
	},
}

func TestADegradedGuardIsDegradedExitFiveWithItsFixedTextForEachReason(t *testing.T) {
	for _, g := range degradedGolden {
		t.Run(g.reason, func(t *testing.T) {
			r := newRig(t)
			// AuthState is clear: the guard alone makes the report degraded.
			r.cfg.Token = &fakeToken{st: tokenguard.State{Degraded: true, Reason: g.reason}}
			rep := r.collect()
			if rep.Health != Degraded || rep.Exit() != 5 {
				t.Fatalf("health %s exit %d, want degraded 5", rep.Health, rep.Exit())
			}
			if len(rep.Reasons) != 1 || rep.Reasons[0] != g.line {
				t.Fatalf("reasons = %q, want [%q]", rep.Reasons, g.line)
			}
			if rep.Auth.State != AuthOK || rep.Auth.DegradedReason != g.reason {
				t.Errorf("auth = %+v", rep.Auth)
			}

			var tb bytes.Buffer
			if err := Render(&tb, rep); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(tb.String(), "  reason: "+g.line+"\n") || !strings.Contains(tb.String(), g.text) {
				t.Errorf("text form lacks the golden lines:\n%s", tb.String())
			}

			var jb bytes.Buffer
			if err := RenderJSON(&jb, rep); err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Health  string          `json:"health"`
				Reasons []string        `json:"reasons"`
				Auth    json.RawMessage `json:"auth"`
			}
			if err := json.Unmarshal(jb.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, doc.Auth); err != nil {
				t.Fatal(err)
			}
			if compact.String() != g.json || doc.Health != "degraded" || len(doc.Reasons) != 1 || doc.Reasons[0] != g.line {
				t.Errorf("json health %q reasons %q auth %s, want auth %s", doc.Health, doc.Reasons, compact.String(), g.json)
			}
		})
	}
}

func TestADegradedGuardWinsOverAClearedAuthMarkerAndNamesOneUnauthenticatedLine(t *testing.T) {
	r := newRig(t)
	r.cfg.Token = &fakeToken{st: tokenguard.State{Degraded: true, Reason: tokenguard.ReasonTokenRejected}}
	// A call in flight succeeded: the marker is clear. The report is still
	// degraded.
	r.auth.bad = false
	if rep := r.collect(); rep.Exit() != 5 || !hasReason(rep, "unauthenticated:", "restart the daemon to recover") {
		t.Fatalf("cleared marker: exit %d reasons %v", rep.Exit(), rep.Reasons)
	}
	// With the marker set too there is still exactly one unauthenticated line.
	r.auth.bad = true
	rep := r.collect()
	n := 0
	for _, reason := range rep.Reasons {
		if strings.HasPrefix(reason, "unauthenticated:") {
			n++
		}
	}
	if n != 1 || rep.Exit() != 5 {
		t.Fatalf("exit %d reasons %v, want one unauthenticated line", rep.Exit(), rep.Reasons)
	}
}

func TestAHealthyOrAbsentGuardAddsNoDegradedReason(t *testing.T) {
	r := newRig(t)
	for _, tok := range []TokenGuard{nil, &fakeToken{}} {
		r.cfg.Token = tok
		rep := r.collect()
		if rep.Exit() != 0 || rep.Auth.DegradedReason != "" || len(rep.Reasons) != 0 {
			t.Errorf("guard %v: exit %d auth %+v reasons %v, want healthy", tok, rep.Exit(), rep.Auth, rep.Reasons)
		}
	}
	// A guard that holds a stale reason string while not degraded is healthy.
	r.cfg.Token = &fakeToken{st: tokenguard.State{Reason: "token_file"}}
	if rep := r.collect(); rep.Exit() != 0 {
		t.Errorf("non-degraded state with a reason: exit %d", rep.Exit())
	}
}

type memStore struct{ cur string }

func (s *memStore) Start() error         { return nil }
func (s *memStore) Write(b []byte) error { s.cur = string(b); return nil }

// The leak check for status output: a real guard driven to token_rejected
// with the fixture token, then the text and JSON forms scanned for the token
// and its digest.
func TestStatusOutputOfARealDegradedGuardLeaksNoToken(t *testing.T) {
	const tok = "FIXTURE-TOKEN-do-not-use-0001"
	g := tokenguard.New(tokenguard.Config{
		Keychain: keychain.NewFake(keychain.Result{Token: tok}, keychain.Result{Token: tok}),
		Store:    &memStore{}, Service: "svc", Account: "acct",
		Clock: governor.NewManualClock(t0), Timer: tokenguard.NewManualTimer(),
	})
	t.Cleanup(g.Close)
	g.Start(t.Context())
	g.Refresh(t.Context(), g.Generation(), origin.ReasonUnauthenticated)
	// RejectedAfterRetry is the rejected-after-retry path of token_rejected.
	g.RejectedAfterRetry()
	if st := g.State(); !st.Degraded || st.Reason != tokenguard.ReasonTokenRejected {
		t.Fatalf("state = %+v", st)
	}
	r := newRig(t)
	r.cfg.Token = g
	rep := r.collect()
	if rep.Exit() != 5 || rep.Auth.DegradedReason != "token_rejected" {
		t.Fatalf("exit %d auth %+v", rep.Exit(), rep.Auth)
	}
	d := sha256.Sum256([]byte(tok))
	secrets := []string{tok, hex.EncodeToString(d[:]), base64.StdEncoding.EncodeToString(d[:]), base64.RawURLEncoding.EncodeToString(d[:]), string(d[:])}
	var tb, jb bytes.Buffer
	if err := Render(&tb, rep); err != nil {
		t.Fatal(err)
	}
	if err := RenderJSON(&jb, rep); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{tb.String(), jb.String()} {
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Fatalf("status output leaks the token or its digest")
			}
		}
	}
	// Negative control: the scan sees a planted token.
	if !strings.Contains(tb.String()+tok, tok) {
		t.Fatal("negative control broken")
	}
}
