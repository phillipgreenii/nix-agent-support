package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeGH is a minimal double for internal.Backend's ghProvider seam, so
// this file's wiring tests never spawn a real `gh` subprocess.
type fakeGH struct{}

func (fakeGH) GetPR(ctx context.Context, repo string, number int) (*api.PR, error) {
	return &api.PR{Repo: repo, Number: number, Title: "hello", State: "open"}, nil
}

func (fakeGH) ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error) {
	return nil, nil
}

func (fakeGH) ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error) {
	return nil, nil
}

func (fakeGH) CheckAuth(ctx context.Context) error { return nil }

func newTestBackend(t *testing.T) *internal.Backend {
	t.Helper()
	return internal.New(fakeGH{}, internal.NewStore(t.TempDir()+"/store.json"))
}

// TestNewDispatchTable_CapabilitiesVocabularyNonEmpty is the packet's
// required test asserting the capabilities op's vocabulary.category list is
// non-empty, proving the vocabulary is actually declared and not just
// committed to in prose [design: §4.3, §6.1].
func TestNewDispatchTable_CapabilitiesVocabularyNonEmpty(t *testing.T) {
	table := newDispatchTable(newTestBackend(t))
	entry, ok := table[scriptout.OpCapabilities]
	if !ok {
		t.Fatal("capabilities entry missing from this binary's own dispatch table")
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T, want scriptout.CapabilitiesResponse", result)
	}
	cats, ok := resp.Vocabulary["category"].([]string)
	if !ok || len(cats) == 0 {
		t.Fatalf("vocabulary.category = %#v, want a non-empty []string", resp.Vocabulary["category"])
	}
}

// TestServeLoop_ShowRoundTripsThroughStdinStdout is the packet's required
// scriptout-level test that this binary's main() correctly wires its op
// table into the Tier-1 core's generic serve loop
// (pkg/scriptout.ServeLoop): an end-to-end stdin-JSON-in, stdout-JSON-out
// exercise, mirroring packages/pg-pr/pkg/plugin/scriptout/scriptout_test.go's
// own style. That file's own runServe test helper is unexported in a
// different package (pkg/scriptout, a different module's package this one
// cannot reach into), so this backend's own main() is instead exercised
// through the real os.Stdin/os.Stdout ServeLoop always reads — swapped for
// pipes here — which is the only external seam scriptout.ServeLoop exposes.
func TestServeLoop_ShowRoundTripsThroughStdinStdout(t *testing.T) {
	origStdin, origStdout := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = origStdin, origStdout }()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin, os.Stdout = inR, outW

	if _, err := inW.WriteString(`{"op":"show","args":{"id":"owner/repo#1"}}`); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	code := scriptout.ServeLoop(newDispatchTable(newTestBackend(t)))

	if err := outW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	raw, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%s", code, raw)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected wire error: %+v", resp.Error)
	}
	var pr struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(resp.Result, &pr); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if pr.ID != "owner/repo#1" || pr.Title != "hello" {
		t.Fatalf("result = %+v", pr)
	}
}
