package run

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Call records one Run invocation for assertions.
type Call struct {
	Name string
	Args []string
	Opts Options
}

type fakeResponse struct {
	name string
	args []string
	res  Result
	err  error
}

// FakeRunner is a scripted test double. Responses match by name + exact args and
// are consumed in order; an unscripted call returns an error so tests fail loudly.
type FakeRunner struct {
	mu        sync.Mutex
	responses []fakeResponse
	calls     []Call
}

func NewFakeRunner() *FakeRunner { return &FakeRunner{} }

func (f *FakeRunner) AddResponse(name string, args []string, res Result, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, fakeResponse{name: name, args: append([]string{}, args...), res: res, err: err})
}

func (f *FakeRunner) Run(_ context.Context, name string, args []string, opts Options) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: name, Args: append([]string{}, args...), Opts: opts})
	for i, r := range f.responses {
		if r.name == name && argsEqual(r.args, args) {
			f.responses = append(f.responses[:i], f.responses[i+1:]...)
			return r.res, r.err
		}
	}
	return Result{}, fmt.Errorf("FakeRunner: no scripted response for: %s %s", name, strings.Join(args, " "))
}

func (f *FakeRunner) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

var (
	_ Runner = (*FakeRunner)(nil)
	_ Runner = CLIRunner{}
)

func argsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
