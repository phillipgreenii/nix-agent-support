// Package outcome is the tool's outcome model: the outcome words and their
// exit codes, the refusal and stop reasons, the warning words, the outcome
// object (JSON schema 1) and its human rendering.
package outcome

import "github.com/phillipgreenii/x/exitcode"

// Schema is the JSON outcome schema version. A change that removes or renames
// a field MUST bump it.
const Schema = 1

// Word is an outcome word.
type Word string

// Outcome words.
const (
	Landed        Word = "landed"
	Rebased       Word = "rebased"
	UpToDate      Word = "up-to-date"
	NothingToLand Word = "nothing-to-land"
	Aborted       Word = "aborted"
	Quit          Word = "quit"
	Status        Word = "status"
	Log           Word = "log"
	ConfigOK      Word = "config-ok"
	InternalError Word = "internal-error"
	UsageError    Word = "usage-error"
	ConfigError   Word = "config-error"
	Busy          Word = "busy"
	Stopped       Word = "stopped"
	Refused       Word = "refused"
)

// AllWords lists every outcome word.
var AllWords = []Word{
	Landed, Rebased, UpToDate, NothingToLand, Aborted, Quit, Status, Log, ConfigOK,
	InternalError, UsageError, ConfigError, Busy, Stopped, Refused,
}

// Exit codes. The shared outcomes use x/exitcode; stopped and refused are
// app-specific codes in the app range.
const (
	ExitOK       = exitcode.OK
	ExitInternal = exitcode.Error
	ExitUsage    = exitcode.Usage
	ExitConfig   = exitcode.Config
	ExitBusy     = exitcode.Busy
	ExitStopped  = exitcode.AppMin + 1 // 21
	ExitRefused  = exitcode.AppMin + 2 // 22
)

// ExitCode maps an outcome word to its exit code (design section 6.1). An
// unknown word maps to ExitInternal.
func ExitCode(w Word) int {
	switch w {
	case Landed, Rebased, UpToDate, NothingToLand, Aborted, Quit, Status, Log, ConfigOK:
		return ExitOK
	case InternalError:
		return ExitInternal
	case UsageError:
		return ExitUsage
	case ConfigError:
		return ExitConfig
	case Busy:
		return ExitBusy
	case Stopped:
		return ExitStopped
	case Refused:
		return ExitRefused
	}
	return ExitInternal
}

// Warning is a warning word carried by an exit-0 outcome.
type Warning string

// Warning words (design section 6.3).
const (
	WarnAfterHookFailed         Warning = "after-hook-failed"
	WarnBranchNotDeleted        Warning = "branch-not-deleted"
	WarnBranchNotMoved          Warning = "branch-not-moved"
	WarnSourceWorktreeNotRemove Warning = "source-worktree-not-removed"
	WarnWorkInDestination       Warning = "work-in-destination"
	WarnUndefinedMergeDriver    Warning = "undefined-merge-driver"
	WarnUnknownRepoConfigKey    Warning = "unknown-repo-config-key"
	WarnConfigChanged           Warning = "config-changed"
	WarnLossyMergeDriver        Warning = "lossy-merge-driver"
	WarnRelativeCommandPath     Warning = "relative-command-path"
	WarnCommandNotFound         Warning = "command-not-found"
)

// Detail values of branch-not-deleted.
const (
	NotDeletedMoved        = "moved"
	NotDeletedNotContained = "not-contained"
)

// Detail values (section 6.2/6.3).
const (
	DetailResolverFailed     = "resolver-failed"
	DetailVerifyFailed       = "verify-failed"
	DetailMarkersLeft        = "markers-left"
	DetailForeignRebase      = "foreign-rebase"
	DetailUnresolved         = "unresolved"
	DetailRebaseStateMissing = "rebase-state-missing"
	DetailWorktreeMismatch   = "worktree-mismatch"
)

// Worktree roles for dirty and not-a-worktree.
const (
	RoleSource      = "source"
	RoleDestination = "destination"
	RoleWork        = "work"
)

// Branch fates, worktree fates and paths taken.
const (
	BranchDeleted = "deleted"
	BranchMoved   = "moved"
	BranchKept    = "kept"

	WorktreeRemoved      = "removed"
	WorktreeKept         = "kept"
	WorktreeRemoveFailed = "remove-failed"

	PathFastForward = "fast-forward"
	PathRebase      = "rebase"
	PathNothing     = "nothing"
)
