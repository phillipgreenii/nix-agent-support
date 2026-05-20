package scriptout

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd"
)

// execCICDProvider wraps a provider binary that speaks the scriptout
// protocol and exposes it as a cicd.Provider.
type execCICDProvider struct {
	binary string
}

// NewExecCICDProvider returns a cicd.Provider backed by the named binary.
// The binary is exec'd per-call; one request/response per invocation.
func NewExecCICDProvider(binary string) cicd.Provider {
	return &execCICDProvider{binary: binary}
}

func (e *execCICDProvider) ListRuns(ctx context.Context, repo string, prNumber int) ([]api.CIRun, error) {
	args := struct {
		Repo     string `json:"repo"`
		PRNumber int    `json:"pr_number"`
	}{Repo: repo, PRNumber: prNumber}
	raw, err := invokeWithArgs(ctx, e.binary, OpListRuns, args)
	if err != nil {
		return nil, err
	}
	var out []api.CIRun
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *execCICDProvider) GetLogs(ctx context.Context, runID string) ([]byte, error) {
	args := struct {
		RunID string `json:"run_id"`
	}{RunID: runID}
	raw, err := invokeWithArgs(ctx, e.binary, OpGetLogs, args)
	if err != nil {
		return nil, err
	}
	var logs string
	if err := unmarshalInto(raw, &logs); err != nil {
		return nil, err
	}
	return []byte(logs), nil
}

func (e *execCICDProvider) RerunFailed(ctx context.Context, repo string, prNumber int) error {
	args := struct {
		Repo     string `json:"repo"`
		PRNumber int    `json:"pr_number"`
	}{Repo: repo, PRNumber: prNumber}
	_, err := invokeWithArgs(ctx, e.binary, OpRerunFailed, args)
	return err
}

// invokeWithArgs is a small helper that marshals args into a Request and
// calls invoke. Centralized to keep each method short.
func invokeWithArgs(ctx context.Context, binary, op string, args any) (json.RawMessage, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return invoke(ctx, binary, Request{Op: op, Args: raw})
}
