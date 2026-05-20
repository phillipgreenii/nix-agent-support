// Package github is the builtin GitHub VCS provider for pg-pr.
// Phase 0 contains only the constructor + ErrNotImplemented stubs.
package github

import (
	"context"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// Provider is the builtin GitHub VCS provider.
type Provider struct{}

// New constructs a GitHub VCS provider. Auth is delegated to `gh auth token` at call time.
func New() *Provider { return &Provider{} }

var errStub = errors.New("github vcs: not implemented (Phase 0 stub)")

func (p *Provider) GetPR(context.Context, string, int) (*api.PR, error) { return nil, errStub }
func (p *Provider) ListMyPRs(context.Context, string) ([]api.PR, error) { return nil, errStub }
func (p *Provider) ListTeamPRs(context.Context, string, []string) ([]api.PR, error) {
	return nil, errStub
}
func (p *Provider) CreatePR(context.Context, string, bool, string, string, string, string) (*api.PR, error) {
	return nil, errStub
}
func (p *Provider) UpdatePR(context.Context, string, int, string) error   { return errStub }
func (p *Provider) SetDraft(context.Context, string, int, bool) error     { return errStub }
func (p *Provider) SetAutomerge(context.Context, string, int, bool) error { return errStub }
func (p *Provider) Merge(context.Context, string, int) error              { return errStub }
func (p *Provider) Close(context.Context, string, int) error              { return errStub }
func (p *Provider) ListComments(context.Context, string, int) ([]api.Comment, error) {
	return nil, errStub
}
func (p *Provider) AddComment(context.Context, string, int, string) (*api.Comment, error) {
	return nil, errStub
}
func (p *Provider) ReplyToThread(context.Context, string, string, string) (*api.Comment, error) {
	return nil, errStub
}
func (p *Provider) ResolveThread(context.Context, string, string) error { return errStub }
func (p *Provider) PostReview(context.Context, string, int, string, []api.Comment) (*api.Review, error) {
	return nil, errStub
}

// Compile-time check that Provider satisfies vcs.Provider.
var _ vcs.Provider = (*Provider)(nil)
