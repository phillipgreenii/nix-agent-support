package internal

import (
	"context"
	"errors"
	"testing"
)

type fakeSearchRunner struct {
	fakeRunner
	searchJSON string
	searchErr  error
}

func (f *fakeSearchRunner) Search(_ context.Context, _ string, _ string) ([]byte, error) {
	return []byte(f.searchJSON), f.searchErr
}

func TestBackend_Search(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"query":"flaky","matches":[{"session_id":"s1","role":"assistant","line":2,"snippet":"the payments test is flaky"}]}`}
	b := New(r)
	got, err := b.Search(context.Background(), "flaky", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s1" || got[0].Type != "agentsession" {
		t.Errorf("got %+v", got)
	}
}

func TestBackend_Search_NoMatches(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"query":"nope","matches":[]}`}
	b := New(r)
	got, err := b.Search(context.Background(), "nope", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestBackend_Search_RunnerError(t *testing.T) {
	r := &fakeSearchRunner{searchErr: errors.New("daemon unreachable")}
	b := New(r)
	if _, err := b.Search(context.Background(), "x", nil); err == nil {
		t.Fatal("expected an error")
	}
}
