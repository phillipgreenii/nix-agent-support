// Package issues declares the issue-tracker provider interface (jira, github issues, etc.).
package issues

import (
	"context"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

var ErrNotImplemented = errors.New("pg-pr: not implemented in this phase")

type Provider interface {
	GetIssue(ctx context.Context, id string) (*api.Issue, error)
}
