package originguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"syscall"
	"time"

	failsig "github.com/phillipgreenii/pg-failsig"
)

// Defaults of the wrapper's own timing (contract rules W4 and W5).
const (
	// DefaultTerminalWait is how long a terminal caller waits for a login
	// already in progress before it preempts the holder.
	DefaultTerminalWait = 30 * time.Second
	killGrace           = 2 * time.Second
	certPollInterval    = time.Second
	lockPollInterval    = 250 * time.Millisecond
	preemptLockWait     = 5 * time.Second
	stderrTailBytes     = 4096
)

// Proxy is the ProxyCommand wrapper of rules W1 to W11. All of its effects go
// through seams, so the unit tests drive it with a fake clock, timers,
// spawner and presence.
type Proxy struct {
	Config   Config
	Clock    Clock
	Store    *Store
	Certs    *CertChecker
	Rec      *Recorder
	Presence PresenceSource
	Spawner  Spawner
	Procs    Procs
	Timers   Timers
	// HasTerminal is caller-has-terminal: stderr is a terminal OR /dev/tty
	// opens. StderrIsTerminal is the narrower test used for the stderr tee.
	HasTerminal      IsTTY
	StderrIsTerminal IsTTY
	// Stderr receives the wrapper's own lines and the tee of the child's.
	Stderr io.Writer
	// Signals delivers SIGINT, SIGTERM and SIGHUP; they are forwarded to the
	// child (and its group). Nil means none.
	Signals <-chan os.Signal
	// TerminalWait overrides DefaultTerminalWait when positive.
	TerminalWait time.Duration
}

type outcome struct {
	code        int
	loginOK     bool
	timedOut    bool
	established bool
	elapsed     time.Duration
	tail        string
}

type watch struct {
	startup bool // valid cert: record the startup outcome
	login   bool // invalid cert: poll the cert, bound the attempt
	timeout time.Duration
	realm   RealmConfig
	onLogin func() // called once when the cert turns valid
	onEst   func()
}

// Run executes argv under the guard of realm and returns the exit status.
func (p *Proxy) Run(ctx context.Context, realm string, argv []string) (code int) {
	started := false
	defer func() {
		if r := recover(); r != nil && !started {
			p.fault(realm, "panic")
			code = p.runPlain(ctx, argv)
		} else if r != nil {
			panic(r)
		}
	}()
	rc, ok := p.Config.Realms[realm]
	if !ok {
		p.fault(realm, "unknown-realm")
		return p.runPlain(ctx, argv)
	}
	if p.Store.Ignored(realm) {
		return p.runPlain(ctx, argv)
	}
	started = true // past this point a panic is not retried (the child may run)

	term := p.HasTerminal()
	reason := p.Certs.Check(ctx, rc)
	if reason == ReasonOK {
		return p.runValid(ctx, realm, rc, argv, term)
	}
	_, _ = p.Rec.Record(realm, Observation{Reason: reason})
	return p.runInvalid(ctx, realm, rc, argv, term, reason)
}

func (p *Proxy) fault(realm, token string) { _ = p.Rec.Fault(realm, token) }

func (p *Proxy) refuse(r Reason) int {
	fmt.Fprint(p.Stderr, refusalLine(r))
	return ExitUnavailable
}

func (p *Proxy) runPlain(ctx context.Context, argv []string) int {
	o, err := p.spawnAndWatch(ctx, ChildSpec{Argv: argv}, nil, watch{}, nil)
	if err != nil {
		return p.startFailure(err)
	}
	return o.code
}

func (p *Proxy) startFailure(err error) int {
	fmt.Fprintf(p.Stderr, "pg-origin-guard: cannot run command: %v\n", err)
	if errors.Is(err, os.ErrNotExist) {
		return 127
	}
	return 126
}

// runValid is the flow with a valid cert: breaker, run, record the startup.
func (p *Proxy) runValid(ctx context.Context, realm string, rc RealmConfig, argv []string, term bool) int {
	now := p.Clock.Now()
	st, _ := p.Store.Load(realm)
	if isCertReason(Reason(st.LastReason)) {
		// the cert is valid now: a manual renewal is seen at once
		p.recordOK(realm, "")
		st, _ = p.Store.Load(realm)
	}
	if !term && st.BreakerOpen(now) {
		trial := false
		_ = p.Store.Update(realm, func(s *RealmState) error {
			windowStart := s.OpenUntil.Add(-p.Config.Breaker.OpenDuration)
			if s.BreakerOpen(p.Clock.Now()) && s.TrialAt.Before(windowStart) {
				s.TrialAt = p.Clock.Now()
				trial = true
			}
			return nil
		})
		if !trial {
			return p.refuse(breakerReason(st.Class))
		}
	}
	spec := ChildSpec{Argv: argv}
	var tail *tailBuf
	if !p.StderrIsTerminal() {
		tail = &tailBuf{max: stderrTailBytes}
		spec.Stderr = io.MultiWriter(p.Stderr, tail)
	}
	w := watch{startup: true, realm: rc, onEst: func() { p.recordOK(realm, "") }}
	o, err := p.spawnAndWatch(ctx, spec, tail, w, nil)
	if err != nil {
		return p.startFailure(err)
	}
	b := p.Config.Breaker
	switch {
	case o.established:
	case o.code == 0, o.elapsed >= b.StartupWindow:
		p.recordOK(realm, "")
	default:
		class := ReasonUnknown
		if tail != nil {
			class = classifyStartup(o.tail)
		}
		p.recordStartupFailure(realm, class, o.tail)
	}
	return o.code
}

var timeoutRE = regexp.MustCompile(`(?i)timed out|timeout`)

func classifyStartup(tail string) Reason {
	switch failsig.Classify(tail).Signature {
	case failsig.GitAuth:
		return ReasonAuthRejected
	case failsig.GitNetwork:
		return ReasonNetwork
	}
	if timeoutRE.MatchString(tail) {
		return ReasonTimeout
	}
	return ReasonUnknown
}

func (p *Proxy) recordOK(realm, loginAttempt string) {
	if loginAttempt == "" {
		st, _ := p.Store.Load(realm)
		if st.Available() && st.ConsecutiveFailures == 0 && st.Attempts == 0 && st.TrialAt.IsZero() {
			return
		}
	}
	_ = p.Store.Update(realm, func(s *RealmState) error {
		s.ApplyOK(p.Clock.Now())
		return nil
	})
	_, _ = p.Rec.Record(realm, Observation{Reason: ReasonOK, LoginAttempt: loginAttempt})
}

func (p *Proxy) recordStartupFailure(realm string, class Reason, tail string) {
	var open bool
	_ = p.Store.Update(realm, func(s *RealmState) error {
		s.ApplyStartupFailure(p.Clock.Now(), class, RedactTail(tail), p.Config.Breaker)
		open = s.BreakerOpen(p.Clock.Now())
		return nil
	})
	if open || class == ReasonAuthRejected {
		_, _ = p.Rec.Record(realm, Observation{Reason: class, LastError: tail})
	}
}

// runInvalid is the flow with no valid cert.
func (p *Proxy) runInvalid(ctx context.Context, realm string, rc RealmConfig, argv []string, term bool, reason Reason) int {
	if term {
		lk := p.acquireTerminal(ctx, realm)
		defer lk.Unlock()
		return p.login(ctx, realm, rc, argv, false, lk)
	}
	if p.Presence.Read().NoHuman() {
		return p.refuse(ReasonNoHuman)
	}
	lk, err := p.Store.TryLogin(realm)
	if errors.Is(err, ErrLockHeld) {
		return p.refuse(ReasonAttempting)
	}
	if err != nil {
		p.fault(realm, "login-lock")
		return p.runPlain(ctx, argv)
	}
	defer lk.Unlock()
	if st, _ := p.Store.Load(realm); st.NextAttemptAt.After(p.Clock.Now()) {
		return p.refuse(ReasonBackoff)
	}
	return p.login(ctx, realm, rc, argv, true, lk)
}

// login runs the child as a login attempt while holding lk (which may be
// nil for a terminal caller that gave up waiting).
func (p *Proxy) login(ctx context.Context, realm string, rc RealmConfig, argv []string, own bool, lk *FileLock) int {
	_ = p.Store.Update(realm, func(s *RealmState) error {
		s.AttemptStartedAt = p.Clock.Now()
		s.HolderPID, s.HolderToken = 0, ""
		return nil
	})
	spec := ChildSpec{Argv: argv, OwnGroup: own}
	var tail *tailBuf
	if !p.StderrIsTerminal() {
		tail = &tailBuf{max: stderrTailBytes}
		spec.Stderr = io.MultiWriter(p.Stderr, tail)
	}
	succeeded := false
	w := watch{login: true, realm: rc}
	if own {
		w.timeout = rc.Login.AttemptTimeout
	}
	w.onLogin = func() {
		succeeded = true
		p.recordOK(realm, "ok")
		lk.Unlock()
	}
	onStart := func(c Child) {
		_ = p.Store.Update(realm, func(s *RealmState) error {
			s.HolderPID = c.Pid()
			s.HolderToken = p.Procs.Token(c.Pid())
			return nil
		})
	}
	o, err := p.spawnAndWatch(ctx, spec, tail, w, onStart)
	if err != nil {
		p.endAttempt(realm)
		return p.startFailure(err)
	}
	if !succeeded {
		if p.Certs.Check(ctx, rc) == ReasonOK {
			succeeded = true
			p.recordOK(realm, "ok")
		} else {
			res := "failed"
			if o.timedOut {
				res = "timeout"
			}
			var reason Reason
			_ = p.Store.Update(realm, func(s *RealmState) error {
				s.ApplyLoginFailure(p.Clock.Now(), rc.Login, RedactTail(o.tail))
				return nil
			})
			reason = p.Certs.Check(ctx, rc)
			_, _ = p.Rec.Record(realm, Observation{Reason: reason, LastError: o.tail, LoginAttempt: res})
		}
	}
	return o.code
}

func (p *Proxy) endAttempt(realm string) {
	_ = p.Store.Update(realm, func(s *RealmState) error {
		s.AttemptStartedAt = time.Time{}
		s.HolderPID, s.HolderToken = 0, ""
		return nil
	})
}

// acquireTerminal gets the login lock for a terminal caller: wait with a
// status line, then preempt a stuck holder after the bound. It returns nil
// (no lock) when the lock cannot be had; the human with a terminal proceeds
// anyway.
func (p *Proxy) acquireTerminal(ctx context.Context, realm string) *FileLock {
	lk, err := p.Store.TryLogin(realm)
	if err == nil {
		return lk
	}
	if !errors.Is(err, ErrLockHeld) {
		return nil
	}
	st, _ := p.Store.Load(realm)
	since := int64(0)
	if !st.AttemptStartedAt.IsZero() {
		since = int64(p.Clock.Now().Sub(st.AttemptStartedAt).Seconds())
	}
	fmt.Fprintf(p.Stderr, "pg-origin-guard: waiting for a login already in progress, started %ds ago\n", since)

	wait := p.TerminalWait
	if wait <= 0 {
		wait = DefaultTerminalWait
	}
	deadline := p.Timers.After(wait)
	preempted := false
	var final <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			deadline = nil
			p.preempt(ctx, realm)
			preempted = true
			final = p.Timers.After(preemptLockWait)
		case <-final:
			return nil
		case <-p.Timers.After(lockPollInterval):
		}
		if lk, err := p.Store.TryLogin(realm); err == nil {
			return lk
		}
		_ = preempted
	}
}

// preempt signals the holder's process group, but only after verifying that
// the recorded pid still has the recorded start-time token.
func (p *Proxy) preempt(ctx context.Context, realm string) {
	st, _ := p.Store.Load(realm)
	if st.HolderPID <= 0 || st.HolderToken == "" || p.Procs.Token(st.HolderPID) != st.HolderToken {
		return
	}
	_ = p.Procs.KillGroup(st.HolderPID, syscall.SIGTERM)
	select {
	case <-ctx.Done():
	case <-p.Timers.After(killGrace):
	}
	if p.Procs.Token(st.HolderPID) == st.HolderToken {
		_ = p.Procs.KillGroup(st.HolderPID, syscall.SIGKILL)
	}
}

// spawnAndWatch starts the child and supervises it until it ends.
func (p *Proxy) spawnAndWatch(ctx context.Context, spec ChildSpec, tail *tailBuf, w watch, onStart func(Child)) (outcome, error) {
	child, err := p.Spawner.Start(spec)
	if err != nil {
		return outcome{}, err
	}
	if onStart != nil {
		onStart(child)
	}
	t0 := p.Clock.Now()
	var o outcome
	var est, poll, to, kill <-chan time.Time
	if w.startup {
		est = p.Timers.After(p.Config.Breaker.EstablishedAfter)
	}
	if w.login {
		poll = p.Timers.After(certPollInterval)
		if w.timeout > 0 {
			to = p.Timers.After(w.timeout)
		}
	}
	ctxDone := ctx.Done()
	terminate := func() {
		_ = child.Signal(syscall.SIGTERM)
		kill = p.Timers.After(killGrace)
	}
	for {
		select {
		case <-child.Done():
			o.code = child.ExitCode()
			o.elapsed = p.Clock.Now().Sub(t0)
			if tail != nil {
				o.tail = tail.String()
			}
			return o, nil
		case <-est:
			est = nil
			o.established = true
			w.onEst()
		case <-poll:
			if p.Certs.Check(ctx, w.realm) == ReasonOK {
				o.loginOK = true
				poll, to = nil, nil
				w.onLogin()
			} else {
				poll = p.Timers.After(certPollInterval)
			}
		case <-to:
			to = nil
			o.timedOut = true
			terminate()
		case <-kill:
			kill = nil
			_ = child.Signal(syscall.SIGKILL)
		case s := <-p.Signals:
			if sig, ok := s.(syscall.Signal); ok {
				_ = child.Signal(sig)
			}
		case <-ctxDone:
			ctxDone = nil
			terminate()
		}
	}
}

func isCertReason(r Reason) bool {
	return r == ReasonCertExpired || r == ReasonCertMissing || r == ReasonAgentUnreachable
}
