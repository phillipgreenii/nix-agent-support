package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Entity types the typed verbs operate on (pr is entityTypePR, desk.go).
const (
	entityTypeIssue  = "issue"
	entityTypeThread = "thread"
)

// typedEntityTypes are the three type groups, in help order.
var typedEntityTypes = []string{entityTypePR, entityTypeIssue, entityTypeThread}

var typeGroups = map[string]*cobra.Command{}

// typeGroup returns the `pg-desk <entityType>` group command (pr, issue or
// thread), creating it on rootCmd on first use and caching it. Every typed
// verb registers with typeGroup("pr").AddCommand(...) from its own init();
// creating the group lazily makes that independent of Go file-init order.
func typeGroup(entityType string) *cobra.Command {
	if g, ok := typeGroups[entityType]; ok {
		return g
	}
	g := &cobra.Command{
		Use:   entityType,
		Short: fmt.Sprintf("Operate on %s entities", entityType),
	}
	typeGroups[entityType] = g
	rootCmd.AddCommand(g)
	return g
}

func init() {
	// Create all three groups up front so `pg-desk pr|issue|thread` exist
	// even before any verb registers under them.
	for _, t := range typedEntityTypes {
		typeGroup(t)
	}
}

// Typed-verb exit codes [design 9.12]: 0 ok (no error), 1 any other error,
// 2 partial (at least one watched query or hydration failed), 3 total
// failure (nothing logged, cursor not advanced). pg-desk keeps its own
// scheme; it is not bound to pg-router's.
const (
	exitPartial = 2
	exitTotal   = 3
)

// exitError carries a process exit code alongside the error to report.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }
func (e *exitError) ExitCode() int { return e.code }

// newExitError wraps err so main exits with code instead of 1. A nil err
// still produces an exit error (with a generic message) so the code is kept.
func newExitError(code int, err error) error {
	if err == nil {
		err = fmt.Errorf("exit %d", code)
	}
	return &exitError{code: code, err: err}
}

// exitCodeFor is the process exit code for a command error: the code of the
// first error in the chain that implements ExitCode() int, else 1.
func exitCodeFor(err error) int {
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

// requireNewSchemaForTypedVerb is the refusal every typed verb with no
// old-schema counterpart applies right after opening the store. It returns
// the store's own error unchanged (it wraps store.ErrOldSchema and says to
// run pg-desk migrate --cutover).
func requireNewSchemaForTypedVerb(st *store.Store) error {
	return st.RequireNewSchema()
}

// resolveTypedRef turns a command-line entity reference into the store key
// (repo, id). For pr it is resolvePRRef; for issue and thread the id is ref
// verbatim, under the single configured repository (the store keys every
// entity type under it).
func resolveTypedRef(cfg *config.Config, entityType, ref string) (repo, id string, err error) {
	if cfg == nil || len(cfg.Repos) == 0 {
		return "", "", fmt.Errorf("no repository configured")
	}
	if entityType == entityTypePR {
		return resolvePRRef(cfg, ref)
	}
	return cfg.Repos[0].Remote, ref, nil
}
