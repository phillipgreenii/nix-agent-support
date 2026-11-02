package changes

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRefreshClass(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		degraded string
		want     string
	}{
		{"clean", nil, "", ""},
		{"degraded read", nil, "ci list", RefreshClassDegraded},
		{
			"unavailable is transient even when the text says not found",
			errors.New("pipeline: gather pr o/r#1: pg-connector [pr show o/r#1 --fresh]: exit 1: unavailable: 502, repository not found"), "", RefreshClassTransient,
		},
		{"unauthenticated", errors.New("pg-connector [pr show x]: exit 1: unauthenticated: bad credentials"), "", RefreshClassUnauthenticated},
		{"wire not_found", errors.New("pg-connector [pr show x]: exit 1: not_found: gone"), "", RefreshClassNotFound},
		{"other wire code", errors.New("pg-connector [x]: exit 1: version_mismatch: skew"), "", RefreshClassHydrateError},
		{
			"config hash mismatch is transient (apply restart window)",
			errors.New("pipeline: gather pr o/r#1: pg-connector [pr show o/r#1 --fresh]: exit 1: invalid_argument: config hash mismatch: the request's backends block was rendered from config /nix/store/a-pg-connector-github-config.json (hash 2bf15f0c) but the daemon runs config /nix/store/b-pg-connector-github-config.json (hash d809103d)"), "", RefreshClassTransient,
		},
		{
			// The Jira daemon's CheckBlock (cmd/pg-connector-issue-jira/internal/config/block.go)
			// renders the same ErrConfigHashMismatch text; pg-desk issue refresh of a Jira key
			// meets it through the issue gather path (pg2-gnocj6).
			"jira config hash mismatch is transient (apply restart window)",
			errors.New("pipeline: gather issue ZR-123: pg-connector [issue show ZR-123 --fresh]: exit 1: invalid_argument: config hash mismatch: the request's backends block was rendered from config /nix/store/a-pg-connector-issue-jira-config.json (hash 2bf15f0c) but the daemon runs config /nix/store/b-pg-connector-issue-jira-config.json (hash d809103d)"), "", RefreshClassTransient,
		},
		{"other invalid_argument stays hydrate_error", errors.New("pg-connector [x]: exit 1: invalid_argument: cursor_expired: gone"), "", RefreshClassHydrateError},
		{"exit 4 text", errors.New("gather: fetch triggering PR o/r#1: pg-connector reports not_found"), "", RefreshClassNotFound},
		{"pipeline not found", errors.New("pipeline: issue bd-1: not found"), "", RefreshClassNotFound},
		{"killed child", errors.New("pg-connector [x]: signal: killed"), "", RefreshClassTransient},
		{"deadline", fmt.Errorf("gather: %w", context.DeadlineExceeded), "", RefreshClassTransient},
		{"busy store", errors.New("store: database is locked"), "", RefreshClassTransient},
		{"anything else", errors.New("decode pg-connector stdout: unexpected end"), "", RefreshClassHydrateError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RefreshClass(tc.err, tc.degraded); got != tc.want {
				t.Errorf("RefreshClass = %q, want %q", got, tc.want)
			}
		})
	}
}
