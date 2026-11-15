package keychain

import (
	"context"
	"errors"
	"sync"
)

// Result is one scripted outcome of Fake.Password.
type Result struct {
	Token string
	Err   error
}

// Fake is a scripted Keychain for tests. Each call consumes the next Result in
// order; a call past the end of the script returns an *Error of KindOther.
// It is safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	script  []Result
	calls   int
	lastSvc string
	lastAcc string
}

// NewFake returns a Fake that plays the given results in order.
func NewFake(script ...Result) *Fake { return &Fake{script: script} }

// Password returns the next scripted outcome and counts the call.
func (f *Fake) Password(_ context.Context, service, account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	f.lastSvc, f.lastAcc = service, account
	if i >= len(f.script) {
		return "", &Error{Kind: KindOther, ExitCode: -1}
	}
	r := f.script[i]
	return r.Token, r.Err
}

// Calls reports how many times Password has been called.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// LastRequest returns the service and account of the latest call.
func (f *Fake) LastRequest() (service, account string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSvc, f.lastAcc
}

// Fail is a convenience Result carrying an *Error of the given kind.
func Fail(kind Kind) Result {
	return Result{Err: &Error{Kind: kind, ExitCode: -1}}
}

var (
	_ Keychain = Security{}
	_ Keychain = (*Fake)(nil)
	_ error    = (*Error)(nil)
	_          = errors.Is
)
