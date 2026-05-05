package prreview

import (
	"context"
	"testing"
	"time"
)

func TestMockExecutor_DefaultResponse(t *testing.T) {
	mock := NewMockExecutor()
	mock.DefaultResponse = MockResponse{
		Stdout: []byte("default output"),
	}

	stdout, _, err := mock.Execute(context.Background(), "/test", "unknown-cmd")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if string(stdout) != "default output" {
		t.Errorf("stdout = %s, want 'default output'", string(stdout))
	}
}

func TestMockExecutor_SpecificResponse(t *testing.T) {
	mock := NewMockExecutor()
	mock.AddResponse("git fetch", MockResponse{
		Stdout: []byte("fetched"),
	})
	mock.AddResponse("git rev-parse", MockResponse{
		Stdout: []byte("abc123"),
	})

	stdout, _, _ := mock.Execute(context.Background(), "/test", "git", "fetch", "origin", "main")
	if string(stdout) != "fetched" {
		t.Errorf("stdout = %s, want 'fetched'", string(stdout))
	}

	stdout, _, _ = mock.Execute(context.Background(), "/test", "git", "rev-parse", "HEAD")
	if string(stdout) != "abc123" {
		t.Errorf("stdout = %s, want 'abc123'", string(stdout))
	}
}

func TestMockExecutor_CallLog(t *testing.T) {
	mock := NewMockExecutor()

	_, _, _ = mock.Execute(context.Background(), "/dir1", "cmd1", "arg1", "arg2")
	_, _, _ = mock.Execute(context.Background(), "/dir2", "cmd2")

	if mock.CallCount() != 2 {
		t.Errorf("CallCount() = %d, want 2", mock.CallCount())
	}

	call := mock.GetCall(0)
	if call == nil {
		t.Fatal("GetCall(0) returned nil")
	}
	if call.Dir != "/dir1" {
		t.Errorf("call.Dir = %s, want '/dir1'", call.Dir)
	}
	if call.Name != "cmd1" {
		t.Errorf("call.Name = %s, want 'cmd1'", call.Name)
	}
	if len(call.Args) != 2 {
		t.Errorf("len(call.Args) = %d, want 2", len(call.Args))
	}
}

func TestMockExecutor_GetCall_OutOfBounds(t *testing.T) {
	mock := NewMockExecutor()

	if mock.GetCall(0) != nil {
		t.Error("GetCall(0) should return nil for empty log")
	}

	_, _, _ = mock.Execute(context.Background(), "/test", "cmd")

	if mock.GetCall(-1) != nil {
		t.Error("GetCall(-1) should return nil")
	}
	if mock.GetCall(100) != nil {
		t.Error("GetCall(100) should return nil")
	}
}

func TestMockExecutor_ExecuteWithStdin(t *testing.T) {
	mock := NewMockExecutor()
	mock.DefaultResponse = MockResponse{Stdout: []byte("ok")}

	stdin := []byte(`{"key": "value"}`)
	stdout, _, err := mock.ExecuteWithStdin(context.Background(), "/test", stdin, "cmd", "arg")

	if err != nil {
		t.Fatalf("ExecuteWithStdin() error = %v", err)
	}

	if string(stdout) != "ok" {
		t.Errorf("stdout = %s, want 'ok'", string(stdout))
	}

	call := mock.GetCall(0)
	if call == nil {
		t.Fatal("Expected call to be recorded")
	}
	if string(call.Stdin) != string(stdin) {
		t.Errorf("call.Stdin = %s, want %s", string(call.Stdin), string(stdin))
	}
}

func TestRealExecutor_Execute(t *testing.T) {
	executor := NewRealExecutor(5 * time.Second)

	stdout, _, err := executor.Execute(context.Background(), "", "echo", "hello")

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if string(stdout) != "hello\n" {
		t.Errorf("stdout = %q, want 'hello\\n'", string(stdout))
	}
}

func TestRealExecutor_ExecuteWithTimeout(t *testing.T) {
	executor := NewRealExecutor(100 * time.Millisecond)

	_, _, err := executor.Execute(context.Background(), "", "sleep", "10")

	if err == nil {
		t.Error("Execute() expected error for timeout")
	}
}

func TestRealExecutor_ExecuteWithStdin(t *testing.T) {
	executor := NewRealExecutor(5 * time.Second)

	stdin := []byte("hello world")
	stdout, _, err := executor.ExecuteWithStdin(context.Background(), "", stdin, "cat")

	if err != nil {
		t.Fatalf("ExecuteWithStdin() error = %v", err)
	}

	if string(stdout) != "hello world" {
		t.Errorf("stdout = %q, want 'hello world'", string(stdout))
	}
}
