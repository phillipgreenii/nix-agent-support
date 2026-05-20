package scriptout

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
)

// execIssuesProvider wraps a provider binary speaking the scriptout
// protocol and exposes it as an issues.Provider.
type execIssuesProvider struct {
	binary string
}

// NewExecIssuesProvider returns an issues.Provider backed by the named
// binary. The binary is exec'd per-call; one request/response per invocation.
func NewExecIssuesProvider(binary string) issues.Provider {
	return &execIssuesProvider{binary: binary}
}

func (e *execIssuesProvider) GetIssue(ctx context.Context, id string) (*api.Issue, error) {
	args := struct {
		ID string `json:"id"`
	}{ID: id}
	raw, err := invokeWithArgs(ctx, e.binary, OpGetIssue, args)
	if err != nil {
		return nil, err
	}
	var out api.Issue
	if err := unmarshalInto(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
