// Package manager is pg-gwm's engine: it leases worktrees of one repository,
// following the behavior set's lease algorithm, and reports every refusal as
// a catalog error.
package manager

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-gwm/internal/state"
	"github.com/phillipgreenii/x/exitcode"
)

// Catalog exit codes the shared library does not define.
const (
	CodeMaxReached    = 20
	CodeNameHeld      = 21
	CodeBranchInUse   = 22
	CodeDirty         = 23
	CodeLeaseReleased = 24
	CodeLeaseLost     = 25
	CodeRefUnresolved = 26
	CodeBroken        = 28
	CodeLowDisk       = 29
	CodeInterrupted   = 130
)

// Error is a catalog error: the name and code a requester branches on, a
// message, a hint saying what to do, and the facts the catalog names.
type Error struct {
	Name    string
	Code    int
	Message string
	Hint    string
	Details map[string]any
}

func (e *Error) Error() string { return e.Message }

// CodeOf is the exit code of err: the catalog code of an *Error, else 1.
func CodeOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return exitcode.Error
}

// AsError returns err as an *Error, wrapping any other error as internal.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return ErrInternal(err)
}

// ErrInternal is an unexpected failure.
func ErrInternal(err error) *Error {
	return &Error{Name: "internal", Code: exitcode.Error, Message: err.Error()}
}

// ErrUsage is a bad request: details names the offending flag or value.
func ErrUsage(msg, hint string, details map[string]any) *Error {
	return &Error{Name: "usage", Code: exitcode.Usage, Message: msg, Hint: hint, Details: details}
}

// ErrNotFound is a key that names nothing.
func ErrNotFound(key string) *Error {
	return &Error{
		Name: "not-found", Code: exitcode.NotFound, Message: fmt.Sprintf("nothing matches %q", key),
		Details: map[string]any{"key": key},
	}
}

// ErrBusy is a repository lock not taken within lock-wait.
func ErrBusy(holder string) *Error {
	d := map[string]any{}
	if holder != "" {
		d["holder"] = holder
	}
	return &Error{
		Name: "busy", Code: exitcode.Busy,
		Message: "the repository is locked by another pg-gwm command",
		Hint:    "retry in a moment, or raise lock-wait", Details: d,
	}
}

// ErrMaxReached is a repository at max with nothing to reclaim.
func ErrMaxReached(max int, counts map[state.Status]int, brokenIDs []string) *Error {
	by := map[string]any{}
	var parts []string
	for s, n := range counts {
		by[string(s)] = n
		parts = append(parts, fmt.Sprintf("%d %s", n, s))
	}
	sort.Strings(parts)
	hint := "release a worktree, wait with --wait, or raise max"
	if len(brokenIDs) > 0 {
		hint = fmt.Sprintf("broken worktree(s) %s count toward max; run: pg-gwm reap", strings.Join(brokenIDs, ", "))
	}
	return &Error{
		Name: "max-reached", Code: CodeMaxReached,
		Message: fmt.Sprintf("all %d allowed worktrees are in use (%s)", max, strings.Join(parts, ", ")),
		Hint:    hint, Details: map[string]any{"max": max, "counts": by},
	}
}

// ErrNameHeld is a name that a preparing, leased or expired lease holds.
func ErrNameHeld(name, holderLeaseID string, status state.Status) *Error {
	return &Error{
		Name: "name-held", Code: CodeNameHeld,
		Message: fmt.Sprintf("the name %q is held by lease %s (%s)", name, holderLeaseID, status),
		Hint:    fmt.Sprintf("renew it with: pg-gwm renew %s, or pass --or-renew", holderLeaseID),
		Details: map[string]any{"lease_id": holderLeaseID, "status": string(status)},
	}
}

// ErrBranchInUse is a branch checked out in another worktree.
func ErrBranchInUse(branch, path, name, leaseID string) *Error {
	d := map[string]any{"path": path, "branch": branch}
	if name != "" {
		d["name"] = name
	}
	if leaseID != "" {
		d["lease_id"] = leaseID
	}
	return &Error{
		Name: "branch-in-use", Code: CodeBranchInUse,
		Message: fmt.Sprintf("branch %q is checked out in %s", branch, path),
		Hint:    "use another branch, -b to start a new one, or --detach", Details: d,
	}
}

// ErrDirty is a worktree with unsaved work.
func ErrDirty(files, commits []string) *Error {
	return &Error{
		Name: "dirty", Code: CodeDirty,
		Message: "the worktree holds unsaved work",
		Hint:    "commit it to a branch or discard it, then try again",
		Details: map[string]any{"files": files, "commits": commits},
	}
}

// ErrLeaseReleased is a renew or release of a lease that was released.
func ErrLeaseReleased(leaseID string) *Error {
	return &Error{
		Name: "lease-released", Code: CodeLeaseReleased,
		Message: fmt.Sprintf("lease %s was released", leaseID),
		Hint:    "lease again", Details: map[string]any{"lease_id": leaseID},
	}
}

// ErrLeaseLost is a renew or release of a lease whose worktree was reclaimed.
func ErrLeaseLost(leaseID, preservedBranch string) *Error {
	d := map[string]any{"lease_id": leaseID}
	msg := fmt.Sprintf("lease %s was lost: its worktree was reclaimed", leaseID)
	hint := "lease again"
	if preservedBranch != "" {
		d["preserved_branch"] = preservedBranch
		hint = fmt.Sprintf("unsaved work is on branch %s; lease again", preservedBranch)
	}
	return &Error{Name: "lease-lost", Code: CodeLeaseLost, Message: msg, Hint: hint, Details: d}
}

// ErrRefUnresolvable is a commit-ish that does not resolve against local refs.
func ErrRefUnresolvable(ref, gitMessage string) *Error {
	return &Error{
		Name: "ref-unresolvable", Code: CodeRefUnresolved,
		Message: fmt.Sprintf("%q does not resolve to a commit in local refs", ref),
		Hint:    "pg-gwm never fetches; fetch first if the commit is remote",
		Details: map[string]any{"reference": ref, "git": gitMessage},
	}
}

// ErrBroken is an operation aimed at a broken worktree, or one that left a
// worktree broken.
func ErrBroken(id, reason string) *Error {
	d := map[string]any{"id": id}
	if reason != "" {
		d["reason"] = reason
	}
	msg := fmt.Sprintf("worktree %s is broken", id)
	if reason != "" {
		msg += ": " + reason
	}
	return &Error{
		Name: "broken", Code: CodeBroken, Message: msg,
		Hint: "run: pg-gwm reap", Details: d,
	}
}

// ErrLowDisk is a creation that would cross free-space-floor.
func ErrLowDisk(free, floor uint64) *Error {
	return &Error{
		Name: "low-disk", Code: CodeLowDisk,
		Message: fmt.Sprintf("%d bytes free, below the free-space-floor of %d", free, floor),
		Hint:    "free some disk space or lower free-space-floor",
		Details: map[string]any{"free_bytes": free, "floor": floor},
	}
}

// ErrInterrupted is a lease cut short by a signal; it is not a refusal.
func ErrInterrupted(msg string) *Error {
	return &Error{Name: "interrupted", Code: CodeInterrupted, Message: msg}
}
