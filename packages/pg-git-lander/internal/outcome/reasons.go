package outcome

// Resume flags, the valid next commands after a stop or refusal.
const (
	FlagContinue = "--continue"
	FlagSkip     = "--skip"
	FlagAbort    = "--abort"
	FlagQuit     = "--quit"
)

// RefusalReason is a refusal reason (exit 5, design section 6.2).
type RefusalReason string

// Refusal reasons.
const (
	RefStoppedPending      RefusalReason = "stopped-pending"
	RefTargetStopped       RefusalReason = "target-stopped"
	RefHolderDied          RefusalReason = "holder-died"
	RefUnreadableLock      RefusalReason = "unreadable-lock"
	RefNoWorktree          RefusalReason = "no-worktree"
	RefNotAWorktree        RefusalReason = "not-a-worktree"
	RefDirty               RefusalReason = "dirty"
	RefTargetWTMismatch    RefusalReason = "target-worktree-mismatch"
	RefSourceWTMismatch    RefusalReason = "source-worktree-mismatch"
	RefCwdWouldBeRemoved   RefusalReason = "cwd-would-be-removed"
	RefMainWorktreeRemoval RefusalReason = "main-worktree-removal"
	RefNamespaceCollision  RefusalReason = "namespace-collision"
	RefSwitchRefused       RefusalReason = "switch-refused"
	RefUnsupportedLayout   RefusalReason = "unsupported-layout"
	RefNoGitIdentity       RefusalReason = "no-git-identity"
	RefUntrustedConfig     RefusalReason = "untrusted-config"
	RefRepoConfigError     RefusalReason = "repo-config-error"
	RefBeforeHookRefused   RefusalReason = "before-hook-refused"
	RefNothingStopped      RefusalReason = "nothing-stopped"
	RefOperationMismatch   RefusalReason = "operation-mismatch"
	RefCannotResume        RefusalReason = "cannot-resume"
	RefLostToConcurrent    RefusalReason = "lost-to-concurrent"
)

// RefusalInfo describes a refusal reason.
type RefusalInfo struct {
	// Hint is the one-sentence "Next" column of section 6.2.
	Hint string
	// NextFlags are the resume flags that are valid next, when the refusal
	// points at a stopped operation; empty otherwise.
	NextFlags []string
}

// Refusals maps every refusal reason to its info.
var Refusals = map[RefusalReason]RefusalInfo{
	RefStoppedPending:      {"Run --continue, --skip, --abort or --quit on the stopped operation.", []string{FlagContinue, FlagSkip, FlagAbort, FlagQuit}},
	RefTargetStopped:       {"Resolve that operation first.", nil},
	RefHolderDied:          {"Run --continue or --abort on the converted operation.", []string{FlagContinue, FlagAbort}},
	RefUnreadableLock:      {"Inspect with status; a person removes the ref.", nil},
	RefNoWorktree:          {"Pass --target-worktree or --source-worktree, or run from a worktree.", nil},
	RefNotAWorktree:        {"Name a worktree listed by git worktree list.", nil},
	RefDirty:               {"Commit or discard the changes, or finish the foreign operation, then run again.", nil},
	RefTargetWTMismatch:    {"Drop --target-worktree, or name TARGET's own worktree.", nil},
	RefSourceWTMismatch:    {"Drop --source-worktree, or name BRANCH's own worktree.", nil},
	RefCwdWouldBeRemoved:   {"Run from another path; pass --source-worktree <path> to confirm the removal; or pass --keep-source-worktree.", nil},
	RefMainWorktreeRemoval: {"Pass --keep-source-worktree.", nil},
	RefNamespaceCollision:  {"Use another branch name.", nil},
	RefSwitchRefused:       {"Move or delete the file, then run again.", nil},
	RefUnsupportedLayout:   {"Not supported in version 1.", nil},
	RefNoGitIdentity:       {"Configure user.name and user.email for the environment the tool runs in.", nil},
	RefUntrustedConfig:     {"Trust the repo in the XDG file, or set ignore_repo_config = true.", nil},
	RefRepoConfigError:     {"Fix the file on TARGET (land) or on the primary branch (rebase), or set ignore_repo_config = true for the repo.", nil},
	RefBeforeHookRefused:   {"As the hook's output says.", nil},
	RefNothingStopped:      {"None.", nil},
	RefOperationMismatch:   {"Use the stopped operation's word.", nil},
	RefCannotResume:        {"For unresolved and markers-left, resolve and git add the paths, then --continue; otherwise --abort or --quit.", nil},
	RefLostToConcurrent:    {"None; read that invocation's outcome.", nil},
}

// StopReason is a stop reason (exit 4, design section 6.3).
type StopReason string

// Stop reasons.
const (
	StopConflict          StopReason = "conflict"
	StopResolveFailed     StopReason = "resolve-failed"
	StopMaxConflictRounds StopReason = "max-conflict-rounds"
	StopHookFailed        StopReason = "hook-failed"
	StopCommitRejected    StopReason = "commit-rejected"
	StopTransient         StopReason = "transient"
	StopTimeout           StopReason = "timeout"
	StopInterrupted       StopReason = "interrupted"
	StopRace              StopReason = "race"
	StopBranchMoved       StopReason = "branch-moved"
	StopCheckoutChanged   StopReason = "checkout-changed"
	StopMoveUnverified    StopReason = "move-unverified"
	StopLockLost          StopReason = "lock-lost"
	StopHolderDied        StopReason = "holder-died"
	StopGitError          StopReason = "git-error"
	StopInternalError     StopReason = "internal-error"
)

// Stages of a stopped operation.
const (
	StageRebasing    = "rebasing"
	StageAfterRebase = "after_rebase"
	StageBeforeMove  = "before_move"
	StageMoving      = "moving"
	StageAny         = "any"
)

// StopInfo describes a stop reason.
type StopInfo struct {
	// Stages is the stages the reason can occur in, as written in the design.
	Stages []string
	// Retryable is the retryable flag.
	Retryable bool
	// Next is the valid next resume flags.
	Next []string
}

// Stops maps every stop reason to its info.
var Stops = map[StopReason]StopInfo{
	StopConflict:          {[]string{StageRebasing}, false, []string{FlagContinue, FlagSkip, FlagAbort, FlagQuit}},
	StopResolveFailed:     {[]string{StageRebasing}, false, []string{FlagContinue, FlagSkip, FlagAbort, FlagQuit}},
	StopMaxConflictRounds: {[]string{StageRebasing}, false, []string{FlagAbort, FlagQuit}},
	StopHookFailed:        {[]string{StageAfterRebase, StageBeforeMove}, false, []string{FlagContinue, FlagAbort, FlagQuit}},
	StopCommitRejected:    {[]string{StageRebasing, StageAfterRebase, StageBeforeMove}, false, []string{FlagContinue, FlagAbort, FlagQuit}},
	StopTransient:         {[]string{StageRebasing, StageAfterRebase, StageBeforeMove}, true, []string{FlagContinue, FlagAbort}},
	StopTimeout:           {[]string{StageAny}, true, []string{FlagContinue, FlagAbort}},
	StopInterrupted:       {[]string{StageAny}, true, []string{FlagContinue, FlagAbort}},
	StopRace:              {[]string{StageMoving}, true, []string{FlagContinue, FlagAbort}},
	StopBranchMoved:       {[]string{StageMoving}, false, []string{FlagAbort}},
	StopCheckoutChanged:   {[]string{StageMoving}, true, []string{FlagContinue, FlagAbort}},
	StopMoveUnverified:    {[]string{StageMoving}, false, []string{FlagAbort}},
	StopLockLost:          {[]string{StageAny}, true, []string{FlagContinue, FlagAbort}},
	StopHolderDied:        {[]string{StageAny}, true, []string{FlagContinue, FlagAbort}},
	StopGitError:          {[]string{StageAny}, false, []string{FlagAbort, FlagQuit}},
	StopInternalError:     {[]string{StageAny}, false, []string{FlagAbort}},
}

// AllRefusals and AllStops list the reasons in a stable order.
var (
	AllRefusals = []RefusalReason{
		RefStoppedPending, RefTargetStopped, RefHolderDied, RefUnreadableLock, RefNoWorktree,
		RefNotAWorktree, RefDirty, RefTargetWTMismatch, RefSourceWTMismatch, RefCwdWouldBeRemoved,
		RefMainWorktreeRemoval, RefNamespaceCollision, RefSwitchRefused, RefUnsupportedLayout,
		RefNoGitIdentity, RefUntrustedConfig, RefRepoConfigError, RefBeforeHookRefused,
		RefNothingStopped, RefOperationMismatch, RefCannotResume, RefLostToConcurrent,
	}
	AllStops = []StopReason{
		StopConflict, StopResolveFailed, StopMaxConflictRounds, StopHookFailed, StopCommitRejected,
		StopTransient, StopTimeout, StopInterrupted, StopRace, StopBranchMoved, StopCheckoutChanged,
		StopMoveUnverified, StopLockLost, StopHolderDied, StopGitError, StopInternalError,
	}
)
