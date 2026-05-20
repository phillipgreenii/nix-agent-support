// Package cicd declares the CI/CD provider interface. Composite providers are
// configured by listing multiple providers per repo in config; pg-pr fans out.
package cicd

import (
	"context"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

var ErrNotImplemented = errors.New("pg-pr: not implemented in this phase")

type Provider interface {
	ListRuns(ctx context.Context, repo string, prNumber int) ([]api.CIRun, error)
	GetLogs(ctx context.Context, runID string) ([]byte, error)
	RerunFailed(ctx context.Context, repo string, prNumber int) error
}
