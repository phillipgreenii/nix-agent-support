package prreview

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// CommandExecutor defines the interface for executing shell commands.
type CommandExecutor interface {
	Execute(ctx context.Context, dir string, name string, args ...string) (stdout, stderr []byte, err error)
	ExecuteWithStdin(ctx context.Context, dir string, stdin []byte, name string, args ...string) (stdout, stderr []byte, err error)
}

// RealExecutor executes commands using os/exec.
type RealExecutor struct {
	Timeout time.Duration
}

// NewRealExecutor creates a new RealExecutor with the given timeout.
func NewRealExecutor(timeout time.Duration) *RealExecutor {
	return &RealExecutor{Timeout: timeout}
}

// Execute runs a command and returns its output.
func (e *RealExecutor) Execute(ctx context.Context, dir string, name string, args ...string) (stdout, stderr []byte, err error) {
	return e.ExecuteWithStdin(ctx, dir, nil, name, args...)
}

// ExecuteWithStdin runs a command with stdin and returns its output.
func (e *RealExecutor) ExecuteWithStdin(ctx context.Context, dir string, stdin []byte, name string, args ...string) (stdout, stderr []byte, err error) {
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	err = cmd.Run()
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), err
}

// MockExecutor is a test double for CommandExecutor.
type MockExecutor struct {
	Responses       map[string]MockResponse
	DefaultResponse MockResponse
	CallLog         []MockCall
}

// MockCall records a call to Execute.
type MockCall struct {
	Dir   string
	Name  string
	Args  []string
	Stdin []byte
}

// MockResponse defines a response for a specific command.
type MockResponse struct {
	Stdout []byte
	Stderr []byte
	Err    error
}

// NewMockExecutor creates a new MockExecutor.
func NewMockExecutor() *MockExecutor {
	return &MockExecutor{
		Responses: make(map[string]MockResponse),
		CallLog:   make([]MockCall, 0),
	}
}

// AddResponse adds a response for a command pattern.
// Pattern format: "command subcommand" (first two parts)
func (m *MockExecutor) AddResponse(pattern string, resp MockResponse) {
	m.Responses[pattern] = resp
}

// Execute records the call and returns preset data.
func (m *MockExecutor) Execute(ctx context.Context, dir string, name string, args ...string) (stdout, stderr []byte, err error) {
	return m.ExecuteWithStdin(ctx, dir, nil, name, args...)
}

// ExecuteWithStdin records the call and returns preset data.
func (m *MockExecutor) ExecuteWithStdin(ctx context.Context, dir string, stdin []byte, name string, args ...string) (stdout, stderr []byte, err error) {
	m.CallLog = append(m.CallLog, MockCall{Dir: dir, Name: name, Args: args, Stdin: stdin})

	key := name
	if len(args) > 0 {
		key = fmt.Sprintf("%s %s", name, args[0])
	}

	if resp, ok := m.Responses[key]; ok {
		return resp.Stdout, resp.Stderr, resp.Err
	}

	if resp, ok := m.Responses[name]; ok {
		return resp.Stdout, resp.Stderr, resp.Err
	}

	return m.DefaultResponse.Stdout, m.DefaultResponse.Stderr, m.DefaultResponse.Err
}

// GetCall returns a specific call from the log.
func (m *MockExecutor) GetCall(index int) *MockCall {
	if index < 0 || index >= len(m.CallLog) {
		return nil
	}
	return &m.CallLog[index]
}

// CallCount returns the number of calls made.
func (m *MockExecutor) CallCount() int {
	return len(m.CallLog)
}
