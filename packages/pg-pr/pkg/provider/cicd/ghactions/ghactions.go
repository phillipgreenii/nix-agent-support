// Package ghactions is the builtin GitHub Actions CICD provider for pg-pr.
package ghactions

import (
	"context"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd"
)

type Provider struct{}

func New() *Provider { return &Provider{} }

var errStub = errors.New("github-actions cicd: not implemented (Phase 0 stub)")

func (p *Provider) ListRuns(context.Context, string, int) ([]api.CIRun, error) { return nil, errStub }
func (p *Provider) GetLogs(context.Context, string) ([]byte, error)            { return nil, errStub }
func (p *Provider) RerunFailed(context.Context, string, int) error             { return errStub }

var _ cicd.Provider = (*Provider)(nil)
