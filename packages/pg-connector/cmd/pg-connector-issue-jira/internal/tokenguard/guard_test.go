package tokenguard

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/keychain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func ok(tok string) keychain.Result { return keychain.Result{Token: tok} }

func TestStartReadsOnceAndManyCallsReadNothing(t *testing.T) {
	r := newRig(t, ok(fixtureToken))
	r.start()
	for i := 0; i < 60; i++ {
		if _, err := r.call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := r.kc.Calls(); n != 1 {
		t.Errorf("keychain reads = %d, want 1 (the start read)", n)
	}
	if r.store.token() != fixtureToken || r.guard.Generation() != 1 {
		t.Errorf("file %q gen %d, want the start token at generation 1", r.store.token(), r.guard.Generation())
	}
	if svc, acc := r.kc.LastRequest(); svc != "svc" || acc != "acct" {
		t.Errorf("keychain asked for %q/%q", svc, acc)
	}
}

func TestRotatedTokenRewritesAndRetriesOnce(t *testing.T) {
	rot := newToken(t)
	r := newRig(t, ok(fixtureToken), ok(rot))
	r.start()
	r.pjira.accept(rot)
	r.clock.Advance(2 * time.Minute)
	if _, err := r.call(); err != nil {
		t.Fatalf("call: %v", err)
	}
	if r.kc.Calls() != 2 || r.store.token() != rot || r.guard.Generation() != 2 {
		t.Errorf("reads %d file %q gen %d, want 2 reads, rotated file, gen 2", r.kc.Calls(), r.store.token(), r.guard.Generation())
	}
	if r.pjira.count() != 2 {
		t.Errorf("pjira launches = %d, want 2 (first + one retry)", r.pjira.count())
	}
	sp := r.spends()
	if len(sp) != 2 || sp[0].Outcome != origin.ResultUnauthenticated || sp[1].Outcome != origin.ResultOK {
		t.Fatalf("spends = %+v, want the first ticket settled unauthenticated then the retry ok", sp)
	}
	if st, _ := r.gov.State().Bucket(origin.Bucket); st.InFlight != 0 {
		t.Errorf("in flight %d, want 0 (no leaked ticket)", st.InFlight)
	}
	if got := r.obs.got(); len(got) != 1 || got[0] != origin.ResultOK {
		t.Errorf("observer %v, want one ok", got)
	}
	if un, _ := r.auth.Unauthenticated(); un {
		t.Error("a retried call must not mark the daemon unauthenticated")
	}
	if r.guard.Degraded() {
		t.Error("guard degraded after a successful rotation")
	}
}

func TestSameTokenDegradesAndStopsLaunching(t *testing.T) {
	r := newRig(t, ok("old"), ok("old"))
	r.start()
	r.clock.Advance(2 * time.Minute)
	_, err := r.call()
	if !origin.IsUnauthenticated(err) {
		t.Fatalf("err = %v, want the plain 401 failure", err)
	}
	st := r.guard.State()
	if !st.Degraded || st.Reason != ReasonTokenRejected {
		t.Fatalf("state = %+v, want degraded token_rejected", st)
	}
	if r.pjira.count() != 1 {
		t.Errorf("launches = %d, want 1 (no retry)", r.pjira.count())
	}
	before := len(r.spends())
	for i := 0; i < 5; i++ {
		_, err := r.call()
		if !origin.IsUnauthenticated(err) || !errors.Is(err, origin.ErrTokenDegraded) {
			t.Fatalf("refused call err = %v", err)
		}
		// The refusal MUST NOT read as an auth failure to the event log.
		if errors.Is(internal.ClassifyPjiraError(err.Error()), scriptout.ErrUnauthenticated) {
			t.Fatalf("refusal text %q classifies as unauthenticated (would emit failure_class=auth)", err)
		}
	}
	if r.pjira.count() != 1 || len(r.spends()) != before {
		t.Errorf("launches %d spends %d while degraded, want no launch and no spend", r.pjira.count(), len(r.spends())-before)
	}
	if got := r.obs.got(); got[len(got)-1] != origin.ResultUnauthenticated {
		t.Errorf("observer %v, want result=unauthenticated for refused calls", got)
	}
}

func TestSecondRejectionAfterRetryDegrades(t *testing.T) {
	r := newRig(t, ok(fixtureToken), ok("also-bad"))
	r.start()
	r.pjira.accept() // nothing is accepted
	r.clock.Advance(2 * time.Minute)
	if _, err := r.call(); !origin.IsUnauthenticated(err) {
		t.Fatalf("err = %v", err)
	}
	if r.pjira.count() != 2 || r.guard.State().Reason != ReasonTokenRejected {
		t.Errorf("launches %d state %+v, want 2 launches and token_rejected", r.pjira.count(), r.guard.State())
	}
	if _, err := r.call(); !errors.Is(err, origin.ErrTokenDegraded) || r.pjira.count() != 2 {
		t.Errorf("after degrade: err %v launches %d, want a refusal and no launch", err, r.pjira.count())
	}
	if r.kc.Calls() != 2 {
		t.Errorf("reads = %d, want 2", r.kc.Calls())
	}
}

func TestNon401NeverReadsRetriesOrDegrades(t *testing.T) {
	for _, msg := range []string{
		"pjira issue PROJ-1: status 403 Forbidden",
		"pjira issue PROJ-1: status 500 Internal Server Error",
		"pjira issue PROJ-403: status 404 Not Found",
	} {
		r := newRig(t, ok(fixtureToken))
		r.start()
		r.clock.Advance(time.Hour)
		r.pjira.hook = func([]string) (string, error, bool) { return "", errors.New(msg), true }
		if _, err := r.call(); err == nil {
			t.Fatalf("%s: want an error", msg)
		}
		if r.kc.Calls() != 1 || r.pjira.count() != 1 || r.guard.Degraded() {
			t.Errorf("%s: reads %d launches %d degraded %v, want 1, 1, false", msg, r.kc.Calls(), r.pjira.count(), r.guard.Degraded())
		}
	}
}

func TestFloorBlocksComparisonWithinSixtySeconds(t *testing.T) {
	r := newRig(t, ok("old"), ok("old"))
	r.start()
	r.clock.Advance(30 * time.Second)
	_, err := r.call()
	if !origin.IsUnauthenticated(err) || errors.Is(err, origin.ErrTokenDegraded) {
		t.Fatalf("err = %v, want the plain failure", err)
	}
	if r.kc.Calls() != 1 || r.pjira.count() != 1 || r.guard.Degraded() {
		t.Errorf("inside the floor: reads %d launches %d degraded %v, want 1, 1, false", r.kc.Calls(), r.pjira.count(), r.guard.Degraded())
	}
	if un, _ := r.auth.Unauthenticated(); !un {
		t.Error("the plain failure keeps its existing AuthState marking")
	}
	r.clock.Advance(31 * time.Second)
	if _, err := r.call(); err == nil {
		t.Fatal("want failure")
	}
	if r.kc.Calls() != 2 || r.guard.State().Reason != ReasonTokenRejected {
		t.Errorf("after the floor: reads %d state %+v, want the comparison to run", r.kc.Calls(), r.guard.State())
	}
}

func TestConcurrentFailuresCauseOneRead(t *testing.T) {
	const n = 20
	rot := newToken(t)
	r := newRig(t, ok(fixtureToken), ok(rot))
	r.start()
	r.pjira.accept(rot)
	r.clock.Advance(time.Hour)
	var ready sync.WaitGroup
	ready.Add(n)
	release := make(chan struct{})
	var once sync.Once
	r.pjira.gate = func() {
		// Only the first wave waits: every call has launched under the old file.
		if r.guard.Generation() == 1 {
			ready.Done()
			<-release
		}
	}
	var done sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			_, err := r.call()
			errs <- err
		}()
	}
	ready.Wait()
	once.Do(func() { close(release) })
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("call: %v", err)
		}
	}
	if r.kc.Calls() != 2 {
		t.Errorf("reads = %d, want 2 (start + one re-pull for %d concurrent failures)", r.kc.Calls(), n)
	}
}

func TestDegradedSurvivesInFlightSuccess(t *testing.T) {
	r := newRig(t, ok("old"), ok("old"))
	r.start()
	r.pjira.accept("old")
	gate := make(chan struct{})
	started := make(chan struct{})
	var first sync.Once
	r.pjira.gate = func() {
		first.Do(func() { close(started); <-gate })
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := r.call(); err != nil {
			t.Errorf("in-flight call: %v", err)
		}
	}()
	<-started
	r.clock.Advance(2 * time.Minute)
	r.guard.RejectedAfterRetry() // another call degraded the guard meanwhile
	close(gate)
	wg.Wait()
	if !r.guard.Degraded() {
		t.Fatal("an in-flight success (AuthState.Clear) flipped a degraded guard back to healthy")
	}
	if _, err := r.call(); !errors.Is(err, origin.ErrTokenDegraded) {
		t.Errorf("err = %v, want a refusal", err)
	}
}

func TestNoKeychainReadAfterRejectedHoweverLong(t *testing.T) {
	r := newRig(t, ok("old"), ok("old"))
	r.start()
	r.clock.Advance(2 * time.Minute)
	_, _ = r.call()
	reads := r.kc.Calls()
	for i := 0; i < 5; i++ {
		r.clock.Advance(72 * time.Hour)
		r.timer.Advance(72 * time.Hour)
		_, _ = r.call()
	}
	if r.kc.Calls() != reads || r.timer.Pending() != 0 {
		t.Errorf("reads %d (was %d), timers %d, want no read and no timer after token_rejected", r.kc.Calls(), reads, r.timer.Pending())
	}
}

func TestReadErrorsBackOffThenWaitForRestart(t *testing.T) {
	r := newRig(t, keychain.Fail(keychain.KindLocked), keychain.Fail(keychain.KindLocked),
		keychain.Fail(keychain.KindTimeout), keychain.Fail(keychain.KindItemMissing))
	r.start()
	st := r.guard.State()
	if !st.Degraded || st.Reason != ReasonTokenUnavailable || st.ReadErrKind != keychain.KindLocked {
		t.Fatalf("state = %+v, want degraded token_unavailable (locked)", st)
	}
	if r.store.writeCount() != 0 {
		t.Error("no file may be written without a token")
	}
	if _, err := r.call(); !errors.Is(err, origin.ErrTokenDegraded) || r.pjira.count() != 0 {
		t.Fatalf("err %v launches %d, want a refusal with no pjira launch", err, r.pjira.count())
	}
	for i, d := range []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute} {
		r.timer.AwaitPending(1)
		r.timer.Advance(d - time.Second)
		if r.kc.Calls() != 1+i {
			t.Fatalf("step %d: read before the %s wait elapsed (%d reads)", i, d, r.kc.Calls())
		}
		r.timer.Advance(time.Second)
		for r.kc.Calls() != 2+i {
			r.rec.await(t, "token_read")
		}
	}
	r.timer.Advance(240 * time.Hour)
	if r.kc.Calls() != 4 {
		t.Errorf("reads = %d, want 4 (start + 3 retries) and then none", r.kc.Calls())
	}
	if st := r.guard.State(); st.Reason != ReasonTokenUnavailable || st.ReadErrKind != keychain.KindItemMissing {
		t.Errorf("state = %+v, want still token_unavailable with the last error kind", st)
	}
}

func TestBackoffRecoversWhenKeychainUnlocks(t *testing.T) {
	r := newRig(t, keychain.Fail(keychain.KindLocked), ok(fixtureToken))
	r.start()
	r.timer.AwaitPending(1)
	r.timer.Advance(time.Minute)
	for !strings.Contains(r.store.token(), "FIXTURE") {
		r.rec.await(t, "token_file_write")
	}
	if r.guard.Degraded() || r.guard.Generation() != 1 {
		t.Errorf("state %+v, want healthy at generation 1", r.guard.State())
	}
	if _, err := r.call(); err != nil {
		t.Errorf("call after recovery: %v", err)
	}
}

func TestRejectedDigestAndReasonSurviveAnUnavailableRead(t *testing.T) {
	r := newRig(t, ok("old"), ok("old"))
	r.start()
	r.clock.Advance(2 * time.Minute)
	_, _ = r.call()
	if r.guard.State().Reason != ReasonTokenRejected {
		t.Fatal("setup: want token_rejected")
	}
	r.guard.noteReadError(&keychain.Error{Kind: keychain.KindLocked, ExitCode: 36})
	st := r.guard.State()
	if st.Reason != ReasonTokenRejected || st.ReadErrKind != keychain.KindLocked {
		t.Errorf("state = %+v, want token_rejected kept and the read error kind recorded", st)
	}
	r.guard.mu.Lock()
	kept := r.guard.hasRej
	r.guard.mu.Unlock()
	if !kept {
		t.Error("the rejected digest was cleared")
	}
}

func TestReadErrorOnRepullKeepsPreviousFile(t *testing.T) {
	r := newRig(t, ok(fixtureToken), keychain.Fail(keychain.KindLocked))
	r.start()
	r.clock.Advance(2 * time.Minute)
	r.pjira.accept()
	_, _ = r.call()
	if st := r.guard.State(); st.Reason != ReasonTokenUnavailable {
		t.Errorf("state = %+v, want token_unavailable", st)
	}
	if r.store.token() != fixtureToken || r.store.writeCount() != 1 {
		t.Errorf("file %q writes %d, want the previous file untouched", r.store.token(), r.store.writeCount())
	}
}

func TestWriteAndStartFailuresDegradeTokenFile(t *testing.T) {
	r := newRig(t)
	r.store.startErr = errors.New("unsafe")
	r.start()
	if r.guard.State().Reason != ReasonTokenFile || r.kc.Calls() != 0 {
		t.Errorf("state %+v reads %d, want token_file and no keychain read", r.guard.State(), r.kc.Calls())
	}
	r = newRig(t, ok(fixtureToken))
	r.store.writeErr = errors.New("disk full")
	r.start()
	if r.guard.State().Reason != ReasonTokenFile || r.store.token() != "" {
		t.Errorf("state %+v, want token_file with nothing written", r.guard.State())
	}
}

func TestMissingTokenFileRewritesSameTokenAndRetries(t *testing.T) {
	r := newRig(t, ok(fixtureToken), ok(fixtureToken))
	r.start()
	r.clock.Advance(2 * time.Minute)
	var once sync.Once
	r.pjira.hook = func([]string) (string, error, bool) {
		var out error
		once.Do(func() { out = errors.New("pjira: read token file: open /x/jira-token: no such file or directory") })
		return "", out, out != nil
	}
	if _, err := r.call(); err != nil {
		t.Fatalf("call: %v", err)
	}
	if r.kc.Calls() != 2 || r.store.writeCount() != 2 || r.guard.Degraded() || r.pjira.count() != 2 {
		t.Errorf("reads %d writes %d degraded %v launches %d, want 2, 2, false, 2",
			r.kc.Calls(), r.store.writeCount(), r.guard.Degraded(), r.pjira.count())
	}
}

func TestMissingTokenFileNotWhileClosing(t *testing.T) {
	r := newRig(t, ok(fixtureToken))
	r.start()
	r.clock.Advance(2 * time.Minute)
	r.guard.SetClosing()
	r.pjira.hook = func([]string) (string, error, bool) {
		return "", errors.New("pjira: read token file: no such file"), true
	}
	_, _ = r.call()
	if r.kc.Calls() != 1 || r.pjira.count() != 1 || r.guard.Degraded() {
		t.Errorf("reads %d launches %d degraded %v, want 1, 1, false while closing", r.kc.Calls(), r.pjira.count(), r.guard.Degraded())
	}
}

func TestFailedAuthStatusTriggersRepull(t *testing.T) {
	rot := newToken(t)
	r := newRig(t, ok(fixtureToken), ok(rot))
	r.start()
	r.clock.Advance(2 * time.Minute)
	r.pjira.accept(rot)
	r.pjira.hook = func(args []string) (string, error, bool) {
		if args[0] == "auth-status" && r.store.token() != rot {
			return "UNAUTHENTICATED\n", errors.New("exit status 1"), true
		}
		return "", nil, false
	}
	out, err := r.run.Run(t.Context(), "auth-status")
	if err != nil || out != "{}" || r.kc.Calls() != 2 {
		t.Errorf("out %q err %v reads %d, want a retry after one re-pull", out, err, r.kc.Calls())
	}
}

func TestRetryFileRecoversATokenFileDegrade(t *testing.T) {
	r := newRig(t, ok(fixtureToken))
	r.store.startErr = errors.New("unsafe")
	r.start()
	if r.guard.State().Reason != ReasonTokenFile {
		t.Fatalf("state %+v, want token_file", r.guard.State())
	}
	// Still refused: no keychain read, still degraded.
	r.guard.RetryFile(context.Background())
	if r.kc.Calls() != 0 || r.guard.State().Reason != ReasonTokenFile {
		t.Fatalf("reads %d state %+v, want no read and still token_file", r.kc.Calls(), r.guard.State())
	}
	r.store.startErr = nil
	r.guard.RetryFile(context.Background())
	if r.guard.Degraded() || r.store.token() != fixtureToken || r.kc.Calls() != 1 {
		t.Errorf("degraded %v token %q reads %d, want recovered with one read", r.guard.Degraded(), r.store.token(), r.kc.Calls())
	}
}

func TestRetryFileDoesNothingInOtherStates(t *testing.T) {
	r := newRig(t, ok(fixtureToken))
	r.start()
	r.clock.Advance(2 * time.Minute)
	r.guard.RetryFile(context.Background())
	if r.kc.Calls() != 1 || r.store.writeCount() != 1 {
		t.Errorf("a healthy guard retried: reads %d writes %d", r.kc.Calls(), r.store.writeCount())
	}
}
