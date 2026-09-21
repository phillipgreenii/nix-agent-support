package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
	ct "github.com/phillipgreenii/claude-transcript"
)

// defaultRetryCfg mirrors config.defaults().Retry for the actuator tests.
func defaultRetryCfg() config.Retry {
	return config.Retry{
		Enabled:     true,
		MaxAttempts: 3,
		BaseDelay:   config.Duration(time.Second),
		Timeout:     config.Duration(60 * time.Second),
		Classes:     []string{"transient_server", "transient_network"},
	}
}

// writeAPIErrorTranscript writes a one-line transcript whose only event is a
// synthetic isApiErrorMessage of the given kind+text, and returns its path.
func writeAPIErrorTranscript(t *testing.T, kind ct.ErrorKind, text string) string {
	t.Helper()
	ts := time.Unix(2000, 0).UTC()
	line := fmt.Sprintf(
		`{"type":"assistant","timestamp":"%s","error":%q,"isApiErrorMessage":true,`+
			`"message":{"model":"<synthetic>","content":[{"type":"text","text":%q}]}}`+"\n",
		ts.Format(time.RFC3339Nano), string(kind), text,
	)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeNudger records Nudge calls and can be made to fail.
type fakeNudger struct {
	calls   []string
	err     error
	liveSet map[string]bool // when non-nil, only listed sessions are "live"
}

func (f *fakeNudger) Nudge(tmuxSession string) error {
	if f.liveSet != nil && !f.liveSet[tmuxSession] {
		return errNoLivePane
	}
	f.calls = append(f.calls, tmuxSession)
	return f.err
}

// --- retryDecision (pure policy) ---

func TestRetryDecision_classToAction(t *testing.T) {
	cfg := defaultRetryCfg()
	cases := []struct {
		name      string
		class     ct.RetryClass
		wantRetry bool
	}{
		{"transient_server retries", ct.ClassTransientServer, true},
		{"transient_network retries", ct.ClassTransientNetwork, true},
		{"rate_limited hands back", ct.ClassRateLimited, false},
		{"terminal hands back", ct.ClassTerminal, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := retryDecision(cfg, tc.class, 0, 0, 2000)
			if got != tc.wantRetry {
				t.Errorf("retryDecision(%v) retry = %v, want %v", tc.class, got, tc.wantRetry)
			}
		})
	}
}

func TestRetryDecision_backoffIsExponential(t *testing.T) {
	cfg := defaultRetryCfg()
	cases := []struct {
		count int64
		want  time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
	}
	for _, tc := range cases {
		_, backoff := retryDecision(cfg, ct.ClassTransientServer, tc.count, 0, 2000)
		if backoff != tc.want {
			t.Errorf("retryCount=%d backoff = %v, want %v", tc.count, backoff, tc.want)
		}
	}
}

func TestRetryDecision_attemptCapExhausts(t *testing.T) {
	cfg := defaultRetryCfg() // MaxAttempts=3
	if retry, _ := retryDecision(cfg, ct.ClassTransientServer, 3, 1000, 1010); retry {
		t.Error("retry_count == max_attempts must not retry")
	}
	if retry, _ := retryDecision(cfg, ct.ClassTransientServer, 2, 1000, 1010); !retry {
		t.Error("retry_count < max_attempts must retry")
	}
}

func TestRetryDecision_windowTimeoutExhausts(t *testing.T) {
	cfg := defaultRetryCfg() // Timeout=60s
	// Window opened at 1000; now=1061 → 61s elapsed >= 60s → exhausted.
	if retry, _ := retryDecision(cfg, ct.ClassTransientServer, 1, 1000, 1061); retry {
		t.Error("elapsed >= timeout must not retry")
	}
	// now=1059 → 59s elapsed < 60s → still in budget.
	if retry, _ := retryDecision(cfg, ct.ClassTransientServer, 1, 1000, 1059); !retry {
		t.Error("elapsed < timeout must retry")
	}
	// No window open yet (windowStartedAt==0): the first retry is always in budget.
	if retry, _ := retryDecision(cfg, ct.ClassTransientServer, 0, 0, 9_999_999); !retry {
		t.Error("first retry (no window) must retry regardless of now")
	}
}

func TestRetryDecision_disabledNeverRetries(t *testing.T) {
	cfg := defaultRetryCfg()
	cfg.Enabled = false
	if retry, _ := retryDecision(cfg, ct.ClassTransientServer, 0, 0, 2000); retry {
		t.Error("disabled retry must never retry")
	}
}

func TestRetryDecision_classNotConfigured(t *testing.T) {
	cfg := defaultRetryCfg()
	cfg.Classes = []string{"transient_network"} // server not configured
	if retry, _ := retryDecision(cfg, ct.ClassTransientServer, 0, 0, 2000); retry {
		t.Error("transient_server must not retry when not in Classes")
	}
	if retry, _ := retryDecision(cfg, ct.ClassTransientNetwork, 0, 0, 2000); !retry {
		t.Error("transient_network must retry when configured")
	}
}

// --- maybeRetry (actuation) ---

func newRetryStore(t *testing.T, clk *clock.Fake) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store.db"), clk)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestMaybeRetry_transientServerResumesInPlace(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a"}); err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
	nudger := &fakeNudger{}
	a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}

	sess, _, _ := st.GetByExternalID(ctx, "ext-a")
	retried, err := a.maybeRetry(ctx, sess, tp)
	if err != nil {
		t.Fatalf("maybeRetry err = %v", err)
	}
	if !retried {
		t.Fatal("retried = false, want true (transient server with budget)")
	}
	if len(nudger.calls) != 1 || nudger.calls[0] != "cc-ext-a" {
		t.Errorf("nudge calls = %v, want one to cc-ext-a", nudger.calls)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-a")
	if got.State != store.Working {
		t.Errorf("state = %q, want working (kept OUT of errored)", got.State)
	}
	if got.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", got.RetryCount)
	}
	if got.RetryWindowStartedAt != 2000 {
		t.Errorf("RetryWindowStartedAt = %d, want 2000", got.RetryWindowStartedAt)
	}
}

func TestMaybeRetry_terminalHandsBack(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a"}); err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrAuthFailed, "Please run /login")
	nudger := &fakeNudger{}
	a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}

	sess, _, _ := st.GetByExternalID(ctx, "ext-a")
	retried, err := a.maybeRetry(ctx, sess, tp)
	if err != nil {
		t.Fatalf("maybeRetry err = %v", err)
	}
	if retried {
		t.Error("retried = true, want false (authentication_failed is terminal)")
	}
	if len(nudger.calls) != 0 {
		t.Errorf("nudge calls = %v, want none", nudger.calls)
	}
}

func TestMaybeRetry_rateLimitedHandsBack(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a"}); err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrRateLimit, "You've hit your limit · resets 3:30pm (America/New_York)")
	nudger := &fakeNudger{}
	a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}

	sess, _, _ := st.GetByExternalID(ctx, "ext-a")
	retried, _ := a.maybeRetry(ctx, sess, tp)
	if retried {
		t.Error("retried = true, want false (rate_limited hands back to pg-router quota gate)")
	}
}

func TestMaybeRetry_budgetExhaustedHandsBack(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	// retry_count already at the cap.
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a", RetryCount: 3, RetryWindowStartedAt: 1990}); err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 529 Overloaded")
	nudger := &fakeNudger{}
	a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}

	sess, _, _ := st.GetByExternalID(ctx, "ext-a")
	retried, _ := a.maybeRetry(ctx, sess, tp)
	if retried {
		t.Error("retried = true, want false (attempt cap exhausted)")
	}
	if len(nudger.calls) != 0 {
		t.Errorf("nudge calls = %v, want none (exhausted)", nudger.calls)
	}
}

// TestMaybeRetry_neverFailOnNudgeError: a nudge delivery failure must surface as
// retried=false (so the caller hands back as errored) and must NOT leave the row
// half-mutated (no bump, still working untouched by the retry path).
func TestMaybeRetry_neverFailOnNudgeError(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a"}); err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
	nudger := &fakeNudger{err: errors.New("tmux boom")}
	a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}

	sess, _, _ := st.GetByExternalID(ctx, "ext-a")
	retried, err := a.maybeRetry(ctx, sess, tp)
	if retried {
		t.Error("retried = true, want false (nudge failed)")
	}
	if err == nil {
		t.Error("err = nil, want non-nil so the caller hands back")
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-a")
	if got.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0 (no bump after a failed nudge)", got.RetryCount)
	}
}

// TestMaybeRetry_noLivePaneHandsBack: a process-drop (pane not live) is not
// resumable in-session → hand back.
func TestMaybeRetry_noLivePaneHandsBack(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a"}); err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
	nudger := &fakeNudger{liveSet: map[string]bool{}} // nothing live
	a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}

	sess, _, _ := st.GetByExternalID(ctx, "ext-a")
	retried, _ := a.maybeRetry(ctx, sess, tp)
	if retried {
		t.Error("retried = true, want false (no live pane)")
	}
}

// TestMaybeRetry_noApiErrorHandsBack: a StopFailure with no classifiable
// api-error in the transcript is not a retry candidate.
func TestMaybeRetry_noApiErrorHandsBack(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: &fakeNudger{}, now: clk.Now, sleep: func(time.Duration) {}}
	sess, _, _ := st.GetByExternalID(ctx, "ext-a")
	retried, _ := a.maybeRetry(ctx, sess, path)
	if retried {
		t.Error("retried = true, want false (no api-error in transcript)")
	}
}

// TestMaybeRetry_backoffIsWaited proves the actuator sleeps the computed backoff
// before re-nudging.
func TestMaybeRetry_backoffIsWaited(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	// retry_count = 2 → backoff should be baseDelay<<2 = 4s.
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a", RetryCount: 2, RetryWindowStartedAt: 1999}); err != nil {
		t.Fatal(err)
	}
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 502 Bad Gateway")
	var slept time.Duration
	a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: &fakeNudger{}, now: clk.Now, sleep: func(d time.Duration) { slept = d }}
	sess, _, _ := st.GetByExternalID(ctx, "ext-a")
	if _, err := a.maybeRetry(ctx, sess, tp); err != nil {
		t.Fatalf("maybeRetry err = %v", err)
	}
	if slept != 4*time.Second {
		t.Errorf("slept = %v, want 4s (baseDelay * 2^2)", slept)
	}
}

// --- ccpool_retries_total / ccpool_retry_exhausted_total call-site behavior ---
// (design D6/D11, pg2-qye99.6). recordRetry/recordRetryExhausted are spied via
// the package-level indirection vars (see their declaration in retry.go) so
// these tests observe exactly which class/reason maybeRetry chose, without
// depending on telemetry's own process-wide lazy instrument singleton.

// withRetrySpies substitutes recordRetry/recordRetryExhausted with spies for
// the duration of fn, then restores the real telemetry-backed functions.
func withRetrySpies(t *testing.T, fn func(retryCalls *[]string, exhaustedCalls *int)) {
	t.Helper()
	origRetry, origExhausted := recordRetry, recordRetryExhausted
	t.Cleanup(func() { recordRetry, recordRetryExhausted = origRetry, origExhausted })

	var retryCalls []string
	var exhaustedCalls int
	recordRetry = func(class string) { retryCalls = append(retryCalls, class) }
	recordRetryExhausted = func() { exhaustedCalls++ }
	fn(&retryCalls, &exhaustedCalls)
}

// TestMaybeRetry_recordsRetryUnconditionallyForEveryClassifiableError proves
// ccpool_retries_total fires at retryDecision's own call site, unconditionally,
// whenever a classifiable API error reaches maybeRetry — including terminal/
// rate_limited classes retryDecision itself never retries (design D6).
func TestMaybeRetry_recordsRetryUnconditionallyForEveryClassifiableError(t *testing.T) {
	cases := []struct {
		name      string
		kind      ct.ErrorKind
		text      string
		wantClass string
	}{
		{"transient server", ct.ErrServerError, "API Error: 500 Internal server error", "transient_server"},
		{"terminal (auth failed)", ct.ErrAuthFailed, "Please run /login", "terminal"},
		{"rate limited", ct.ErrRateLimit, "You've hit your limit · resets 3:30pm (America/New_York)", "rate_limited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRetrySpies(t, func(retryCalls *[]string, exhaustedCalls *int) {
				clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
				st := newRetryStore(t, clk)
				ctx := context.Background()
				if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a"}); err != nil {
					t.Fatal(err)
				}
				tp := writeAPIErrorTranscript(t, tc.kind, tc.text)
				a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: &fakeNudger{}, now: clk.Now, sleep: func(time.Duration) {}}
				sess, _, _ := st.GetByExternalID(ctx, "ext-a")
				if _, err := a.maybeRetry(ctx, sess, tp); err != nil {
					t.Fatalf("maybeRetry err = %v", err)
				}
				if got := *retryCalls; len(got) != 1 || got[0] != tc.wantClass {
					t.Errorf("recordRetry calls = %v, want [%q]", got, tc.wantClass)
				}
			})
		})
	}
}

// TestMaybeRetry_exhaustedOnlyFiresForGenuineExhaustion is the AC test: it
// confirms ccpool_retry_exhausted_total increments ONLY for the two genuine
// policy-exhaustion reasons (attempt cap, retry-window timeout) and NOT for
// the other two !doRetry reasons (disabled, class not configured) — even
// though all four reach maybeRetry's same `if !doRetry` branch (design D6
// round-2 semantic post-check finding).
func TestMaybeRetry_exhaustedOnlyFiresForGenuineExhaustion(t *testing.T) {
	cases := []struct {
		name        string
		cfg         func() config.Retry
		retryCount  int64
		windowStart int64
		wantExhaust bool
	}{
		{
			name:        "disabled decline does NOT count as exhaustion",
			cfg:         func() config.Retry { c := defaultRetryCfg(); c.Enabled = false; return c },
			wantExhaust: false,
		},
		{
			name:        "class not configured does NOT count as exhaustion",
			cfg:         func() config.Retry { c := defaultRetryCfg(); c.Classes = []string{"rate_limited"}; return c },
			wantExhaust: false,
		},
		{
			name:        "attempt cap IS genuine exhaustion",
			cfg:         defaultRetryCfg,
			retryCount:  3, // == MaxAttempts
			wantExhaust: true,
		},
		{
			name:        "window timeout IS genuine exhaustion",
			cfg:         defaultRetryCfg,
			windowStart: 1000, // now(2000) - 1000 = 1000s elapsed >= 60s timeout
			wantExhaust: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRetrySpies(t, func(_ *[]string, exhaustedCalls *int) {
				clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
				st := newRetryStore(t, clk)
				ctx := context.Background()
				if err := st.Insert(ctx, store.Session{
					ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a",
					RetryCount: tc.retryCount, RetryWindowStartedAt: tc.windowStart,
				}); err != nil {
					t.Fatal(err)
				}
				tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
				a := &retryActuator{cfg: tc.cfg(), store: st, nudger: &fakeNudger{}, now: clk.Now, sleep: func(time.Duration) {}}
				sess, _, _ := st.GetByExternalID(ctx, "ext-a")
				retried, err := a.maybeRetry(ctx, sess, tp)
				if err != nil {
					t.Fatalf("maybeRetry err = %v", err)
				}
				if retried {
					t.Fatalf("retried = true, want false (this case must decline)")
				}
				gotExhaust := *exhaustedCalls != 0
				if gotExhaust != tc.wantExhaust {
					t.Errorf("recordRetryExhausted called = %v (count=%d), want %v", gotExhaust, *exhaustedCalls, tc.wantExhaust)
				}
			})
		})
	}
}

// TestMaybeRetry_exhaustedNotFiredOnNonPolicyDeclines: the OTHER return-false
// branches in maybeRetry (no classifiable error, dead pane, never-fail
// infra-failure) must never fire ccpool_retry_exhausted_total either — only
// the genuine-exhaustion path inside `if !doRetry` may.
func TestMaybeRetry_exhaustedNotFiredOnNonPolicyDeclines(t *testing.T) {
	withRetrySpies(t, func(_ *[]string, exhaustedCalls *int) {
		clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
		st := newRetryStore(t, clk)
		ctx := context.Background()
		if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", State: store.Working, TmuxSession: "cc-ext-a"}); err != nil {
			t.Fatal(err)
		}
		// No classifiable api-error → rec.Kind == "" branch, before retryDecision
		// is ever consulted.
		path := filepath.Join(t.TempDir(), "empty.jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"hi"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: &fakeNudger{}, now: clk.Now, sleep: func(time.Duration) {}}
		sess, _, _ := st.GetByExternalID(ctx, "ext-a")
		if _, err := a.maybeRetry(ctx, sess, path); err != nil {
			t.Fatalf("maybeRetry err = %v", err)
		}
		if *exhaustedCalls != 0 {
			t.Errorf("recordRetryExhausted called %d times, want 0 (no classifiable error, never reached !doRetry)", *exhaustedCalls)
		}
	})
}

// --- hook integration: fail event ---

const failPayloadRetry = `{"session_id":"csid-x","transcript_path":"%s","hook_event_name":"Stop"}`

func TestHookFail_retriesKeepsWorking(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", ClaudeSessionID: "csid-x", State: store.Working, TmuxSession: "cc-ext-a", TranscriptPath: tp}); err != nil {
		t.Fatal(err)
	}
	nudger := &fakeNudger{}
	ra := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}
	payload := fmt.Sprintf(failPayloadRetry, tp)
	if err := handleHookN("fail", strings.NewReader(payload), st, "", nil, nil, ra, false, io.Discard); err != nil {
		t.Fatalf("handleHookN fail: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-a")
	if got.State == store.Errored {
		t.Error("state = errored, want NOT errored (retried in place)")
	}
	if got.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", got.RetryCount)
	}
	if len(nudger.calls) != 1 {
		t.Errorf("nudge calls = %v, want one", nudger.calls)
	}
}

func TestHookFail_exhaustedBudgetGoesErrored(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 529 Overloaded")
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", ClaudeSessionID: "csid-x", State: store.Working, TmuxSession: "cc-ext-a", TranscriptPath: tp, RetryCount: 3, RetryWindowStartedAt: 1990}); err != nil {
		t.Fatal(err)
	}
	nudger := &fakeNudger{}
	ra := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}
	payload := fmt.Sprintf(failPayloadRetry, tp)
	if err := handleHookN("fail", strings.NewReader(payload), st, "", nil, nil, ra, false, io.Discard); err != nil {
		t.Fatalf("handleHookN fail: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-a")
	if got.State != store.Errored {
		t.Errorf("state = %q, want errored (budget exhausted hands back)", got.State)
	}
	if len(nudger.calls) != 0 {
		t.Errorf("nudge calls = %v, want none", nudger.calls)
	}
}

func TestHookFail_nudgeErrorFallsBackToErrored(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", ClaudeSessionID: "csid-x", State: store.Working, TmuxSession: "cc-ext-a", TranscriptPath: tp}); err != nil {
		t.Fatal(err)
	}
	nudger := &fakeNudger{err: errors.New("tmux boom")}
	ra := &retryActuator{cfg: defaultRetryCfg(), store: st, nudger: nudger, now: clk.Now, sleep: func(time.Duration) {}}
	payload := fmt.Sprintf(failPayloadRetry, tp)
	if err := handleHookN("fail", strings.NewReader(payload), st, "", nil, nil, ra, false, io.Discard); err != nil {
		t.Fatalf("handleHookN fail must never error: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-a")
	if got.State != store.Errored {
		t.Errorf("state = %q, want errored (nudge failure falls back)", got.State)
	}
}

func TestHookFail_nilActuatorGoesErrored(t *testing.T) {
	// Backward-compat: a nil actuator (e.g. handleHook) keeps today's behavior.
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	tp := writeAPIErrorTranscript(t, ct.ErrServerError, "API Error: 500 Internal server error")
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", ClaudeSessionID: "csid-x", State: store.Working, TmuxSession: "cc-ext-a", TranscriptPath: tp}); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(failPayloadRetry, tp)
	if err := handleHookN("fail", strings.NewReader(payload), st, "", nil, nil, nil, false, io.Discard); err != nil {
		t.Fatalf("handleHookN fail: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-a")
	if got.State != store.Errored {
		t.Errorf("state = %q, want errored (nil actuator = today's behavior)", got.State)
	}
}

// --- hook integration: stop resets the retry budget ---

func TestHookStop_resetsRetryBudget(t *testing.T) {
	clk := &clock.Fake{T: time.Unix(2000, 0).UTC()}
	st := newRetryStore(t, clk)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-a", ClaudeSessionID: "csid-x", State: store.Working, RetryCount: 2, RetryWindowStartedAt: 1990}); err != nil {
		t.Fatal(err)
	}
	const stopP = `{"session_id":"csid-x","transcript_path":"/p/x.jsonl","hook_event_name":"Stop"}`
	if err := handleHookN("stop", strings.NewReader(stopP), st, "", nil, nil, nil, false, io.Discard); err != nil {
		t.Fatalf("handleHookN stop: %v", err)
	}
	got, _, _ := st.GetByExternalID(ctx, "ext-a")
	if got.State != store.Idle {
		t.Errorf("state = %q, want idle", got.State)
	}
	if got.RetryCount != 0 || got.RetryWindowStartedAt != 0 {
		t.Errorf("retry state = (%d, %d), want (0, 0) reset on success", got.RetryCount, got.RetryWindowStartedAt)
	}
}
