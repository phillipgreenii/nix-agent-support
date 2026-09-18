package internal

import (
	"context"
	"errors"
	"testing"
)

type fakeRunner struct {
	statusJSON string
	infoJSON   map[string]string // selector -> json
	err        error
}

func (f *fakeRunner) Status(_ context.Context) ([]byte, error) {
	return []byte(f.statusJSON), f.err
}

func (f *fakeRunner) Info(_ context.Context, selector string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	j, ok := f.infoJSON[selector]
	if !ok {
		return nil, errors.New("pa-monitor: no directory/session matched")
	}
	return []byte(j), nil
}

func (f *fakeRunner) Search(_ context.Context, _ string, _ string) ([]byte, error) {
	return nil, errors.New("not used by this test")
}

func TestBackend_Show(t *testing.T) {
	r := &fakeRunner{infoJSON: map[string]string{
		"session:s1": `{"session_id":"s1","status":"working","model":"claude-sonnet-5","session_tokens":100,"cost_usd":0.1}`,
	}}
	b := New(r)
	got, err := b.Show(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.SessionID != "s1" || got.Status != "working" {
		t.Errorf("got %+v", got)
	}
	if got.AsOf == "" {
		t.Error("AsOf must be populated (INV-ASOF-1)")
	}
}

func TestBackend_Show_NotFound(t *testing.T) {
	r := &fakeRunner{infoJSON: map[string]string{}}
	b := New(r)
	if _, err := b.Show(context.Background(), "missing"); err == nil {
		t.Fatal("expected an error for an unknown session id")
	}
}

func TestBackend_List(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"working"},{"session_id":"s2","status":"idle"}]}`}
	b := New(r)
	got, err := b.List(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 2 || got.PresentIDs[0] != "s1" || got.PresentIDs[1] != "s2" {
		t.Errorf("got %+v", got)
	}
}

func TestBackend_DaemonUnreachable(t *testing.T) {
	r := &fakeRunner{err: errors.New("daemon unreachable")}
	b := New(r)
	if _, err := b.List(context.Background(), nil, false); err == nil {
		t.Fatal("expected an error when the daemon is unreachable")
	}
}
