// Package githubissues is the builtin GitHub Issues provider for pg-pr.
package githubissues

import (
	"context"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
)

type Provider struct{}

func New() *Provider { return &Provider{} }

var errStub = errors.New("github-issues: not implemented (Phase 0 stub)")

func (p *Provider) GetIssue(context.Context, string) (*api.Issue, error) { return nil, errStub }

var _ issues.Provider = (*Provider)(nil)
