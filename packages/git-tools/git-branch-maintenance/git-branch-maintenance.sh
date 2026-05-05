# shellcheck shell=bash

# ============================================================================
# Help Text (defined early, before any external commands)
# ============================================================================

show_help() {
  cat <<EOF
Usage: git-branch-maintenance [OPTIONS] [BRANCH...]

Maintain git branches with fast-forward, rebase, and cleanup operations.

Operations (all optional):
  --ff                      Fast-forward all branches to origin/main
  --rebase                  Rebase all branches onto origin/main
  --delete-merged           Delete branches that are merged into main
  --delete-merged-worktrees Allow deletion of worktrees during --delete-merged

Protection Options:
  --protect-branch <branch> Add a protected branch for this run
  --protect-worktree <path> Add a protected worktree for this run

Options:
  --dry-run                 Show what would happen without making changes
  --force                   Skip working directory validation
  -h, --help                Show this help message and exit
  -v, --version             Show version information

If no operations are specified, displays the current status of each branch.
If specific branches are provided, only those branches will be processed.
The script processes each branch in order: ff → rebase → delete-merged
Protected branches cannot be deleted but can be ff/rebased.

Configuration (per-repository):
  # Protect branches from deletion
  git config --local --add git-branch-maintenance.protectedBranch "staging"
  git config --local --add git-branch-maintenance.protectedBranch "production"

  # Protect worktrees from deletion
  git config --local --add git-branch-maintenance.protectedWorktree "/path/to/worktree"

  # View current configuration
  git config --get-all git-branch-maintenance.protectedBranch
  git config --get-all git-branch-maintenance.protectedWorktree

  # Remove a protected branch
  git config --unset git-branch-maintenance.protectedBranch "staging"

Examples:
  # Show status of all branches
  git-branch-maintenance

  # Show status of specific branches
  git-branch-maintenance feature-branch another-branch

  # Fast-forward all branches
  git-branch-maintenance --ff

  # Fast-forward specific branches
  git-branch-maintenance --ff feature-branch

  # Fast-forward then rebase
  git-branch-maintenance --ff --rebase

  # Delete merged branches (preserves worktrees)
  git-branch-maintenance --delete-merged

  # Delete merged branches AND their worktrees
  git-branch-maintenance --delete-merged --delete-merged-worktrees

  # Protect a worktree for this run only
  git-branch-maintenance --delete-merged --protect-worktree /tmp/special

  # Do everything with dry-run
  git-branch-maintenance --ff --rebase --delete-merged --dry-run

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps/issues>
EOF
}

# ============================================================================
# Variable Declarations (initialized before argument parsing)
# ============================================================================

# Flags
DO_FF=false
DO_REBASE=false
DO_DELETE_MERGED=false
DELETE_MERGED_WORKTREES=false
DRY_RUN=false
FORCE=false
SPECIFIC_BRANCHES=()
PROTECTED_BRANCHES=()
PROTECTED_WORKTREES=()

# ============================================================================
# Utility Functions
# ============================================================================

get_branch_worktree() {
  local branch="$1"
  # Get worktree path for branch, or empty if not in worktree
  git worktree list --porcelain | awk -v branch="$branch" '
    /^worktree / { path=$2 }
    /^branch / && $2 == "refs/heads/"branch { print path; exit }
  '
}

is_protected_branch() {
  local branch="$1"
  local b
  for b in "${PROTECTED_BRANCHES[@]}"; do
    [[ $b == "$branch" ]] && return 0
  done
  return 1
}

is_protected_worktree() {
  local path="$1"
  local p
  for p in "${PROTECTED_WORKTREES[@]}"; do
    [[ $p == "$path" ]] && return 0
  done
  return 1
}

validate_clean_working_dir() {
  if [[ $FORCE == "true" ]]; then
    return
  fi

  if git status --porcelain | grep -q .; then
    echo "Error: Uncommitted changes detected. Please commit or stash your changes." >&2
    echo "       Or use --force to skip this check." >&2
    exit 1
  fi
}

validate_worktree_clean() {
  local worktree_path="$1"

  if [[ $FORCE == "true" ]]; then
    return 0
  fi

  # No worktree means no validation needed
  if [[ -z $worktree_path ]]; then
    return 0
  fi

  # Worktree path doesn't exist, treat as no worktree
  if [[ ! -d $worktree_path ]]; then
    return 0
  fi

  # Check if worktree has uncommitted changes
  if (cd "$worktree_path" && git status --porcelain | grep -q .); then
    return 1
  fi

  return 0
}

# ============================================================================
# Temporary Worktree Management
# ============================================================================

create_tmp_worktree() {
  # Always create, even for dry-run (needed for branch switching)

  # Clean up any existing temporary worktree/branch from previous runs
  # This handles cases where the script was interrupted
  # Look for any worktree with the tmp-gbm branch (from any previous PID)
  local existing_worktree
  existing_worktree=$(git worktree list --porcelain | awk '/^worktree / { path=$2 } /^branch / && $2 == "refs/heads/'"$TMP_BRANCH"'" { print path }')
  if [[ -n $existing_worktree ]]; then
    git worktree remove "$existing_worktree" --force 2>/dev/null || true
  fi

  # Delete the branch if it exists
  if git branch --list "$TMP_BRANCH" | grep -q "$TMP_BRANCH"; then
    git branch -D "$TMP_BRANCH" 2>/dev/null || true
  fi

  # Create branch and worktree
  git branch -f "$TMP_BRANCH" "$BASE_BRANCH" 2>/dev/null
  git worktree add "$TMP_WORKTREE" "$TMP_BRANCH" 2>/dev/null
}

cleanup_tmp_worktree() {
  # Remove worktree and branch (guard against unset variables for early exit)
  if [[ -n ${TMP_WORKTREE:-} ]]; then
    git worktree remove "$TMP_WORKTREE" --force 2>/dev/null || true
  fi
  if [[ -n ${TMP_BRANCH:-} ]]; then
    git branch -D "$TMP_BRANCH" 2>/dev/null || true
  fi
}

# Set trap for cleanup
trap cleanup_tmp_worktree EXIT

# ============================================================================
# Status Functions
# ============================================================================

get_branch_status() {
  local branch="$1"

  local branch_hash
  local base_hash
  branch_hash=$(git rev-parse "$branch" 2>/dev/null)
  base_hash=$(git rev-parse "$BASE_BRANCH" 2>/dev/null)

  if [[ $branch_hash == "$base_hash" ]]; then
    echo "up-to-date"
  elif git merge-base --is-ancestor "$branch_hash" "$base_hash" 2>/dev/null; then
    echo "can-fast-forward"
  elif git branch --merged "$BASE_BRANCH" | grep -q "^[* ] $branch$"; then
    echo "merged"
  else
    echo "diverged"
  fi
}

print_branch_status() {
  local branch="$1"
  local status="$2"

  case "$status" in
  fast-forwarded)
    echo "  ✅ Fast-forwarded"
    ;;
  rebased)
    echo "  ✅ Rebased"
    ;;
  deleted)
    echo "  ✅ Deleted"
    ;;
  deleted-with-worktree)
    echo "  ✅ Deleted (with worktree)"
    ;;
  up-to-date)
    echo "  ✅ Up-to-date"
    ;;
  diverged)
    echo "  ⚠️  Diverged from $BASE_BRANCH"
    ;;
  rebase-failed)
    echo "  ⚠️  Rebase failed (conflicts)"
    ;;
  protected-from-deletion)
    echo "  ⚠️  Protected from deletion"
    ;;
  has-worktree)
    echo "  ⚠️  Has worktree, use --delete-merged-worktrees to remove"
    ;;
  skipped-dirty-worktree)
    echo "  ⚠️  Skipped: worktree has uncommitted changes"
    ;;
  not-merged)
    echo "  ⚠️  Not merged into $BASE_BRANCH"
    ;;
  can-fast-forward)
    echo "  ℹ️  Can fast-forward"
    ;;
  merged)
    echo "  ℹ️  Merged into $BASE_BRANCH"
    ;;
  *)
    echo "  ℹ️  Status: $status"
    ;;
  esac
}

# ============================================================================
# Operation Functions
# ============================================================================

try_ff() {
  local branch="$1"
  local worktree_path="$2"

  local branch_hash
  local base_hash
  branch_hash=$(git rev-parse "$branch" 2>/dev/null)
  base_hash=$(git rev-parse "$BASE_BRANCH" 2>/dev/null)

  # Already up-to-date
  if [[ $branch_hash == "$base_hash" ]]; then
    return 0 # up-to-date
  fi

  # Check if can fast-forward
  if ! git merge-base --is-ancestor "$branch_hash" "$base_hash" 2>/dev/null; then
    return 1 # diverged
  fi

  if [[ $DRY_RUN == "true" ]]; then
    echo "  [DRY-RUN] Would fast-forward"
    return 0
  fi

  if [[ -n $worktree_path ]]; then
    # Branch already checked out in worktree - just cd and merge
    if [[ -d $worktree_path ]]; then
      (cd "$worktree_path" && git merge --ff-only "$BASE_BRANCH" 2>/dev/null)
    else
      # Worktree path doesn't exist, treat as not in worktree
      git update-ref "refs/heads/$branch" "$base_hash"
    fi
  else
    # Not in worktree - use update-ref
    git update-ref "refs/heads/$branch" "$base_hash"
  fi

  return 0
}

try_rebase() {
  local branch="$1"
  local worktree_path="$2"

  if [[ $DRY_RUN == "true" ]]; then
    echo "  [DRY-RUN] Would attempt rebase"
    return 0
  fi

  if [[ -n $worktree_path ]]; then
    # Branch already checked out in worktree - cd and rebase
    if [[ -d $worktree_path ]]; then
      (
        cd "$worktree_path"
        set +e
        git rebase --autostash --autosquash "$BASE_BRANCH" 2>/dev/null
        rebase_status=$?
        set -e

        if [[ $rebase_status -ne 0 ]]; then
          git rebase --abort 2>/dev/null || true
          return 1
        fi
        return 0
      )
    else
      # Worktree path doesn't exist, treat as not in worktree
      (
        cd "$TMP_WORKTREE"
        git checkout "$branch" 2>/dev/null
        set +e
        git rebase --autostash --autosquash "$BASE_BRANCH" 2>/dev/null
        rebase_status=$?
        set -e

        if [[ $rebase_status -ne 0 ]]; then
          git rebase --abort 2>/dev/null || true
          git checkout "$TMP_BRANCH" 2>/dev/null
          return 1
        fi
        git checkout "$TMP_BRANCH" 2>/dev/null
        return 0
      )
    fi
  else
    # Not in worktree - checkout in tmp-gbm, rebase, return
    (
      cd "$TMP_WORKTREE"
      git checkout "$branch" 2>/dev/null
      set +e
      git rebase --autostash --autosquash "$BASE_BRANCH" 2>/dev/null
      rebase_status=$?
      set -e

      if [[ $rebase_status -ne 0 ]]; then
        git rebase --abort 2>/dev/null || true
        git checkout "$TMP_BRANCH" 2>/dev/null
        return 1
      fi
      git checkout "$TMP_BRANCH" 2>/dev/null
      return 0
    )
  fi
}

try_delete_merged() {
  local branch="$1"
  local worktree_path="$2"

  # Check if protected (can ff/rebase but not delete)
  if is_protected_branch "$branch"; then
    return 2 # protected
  fi

  # Check if merged
  if ! git branch --merged "$BASE_BRANCH" | grep -q "^[* ] $branch$"; then
    return 3 # not merged
  fi

  if [[ $DRY_RUN == "true" ]]; then
    if [[ -n $worktree_path ]]; then
      if [[ $DELETE_MERGED_WORKTREES == "true" ]] && ! is_protected_worktree "$worktree_path"; then
        echo "  [DRY-RUN] Would delete branch and worktree"
      else
        echo "  [DRY-RUN] Would skip (has worktree)"
      fi
    else
      echo "  [DRY-RUN] Would delete branch"
    fi
    return 0
  fi

  if [[ -n $worktree_path ]]; then
    if [[ $DELETE_MERGED_WORKTREES == "true" ]] && ! is_protected_worktree "$worktree_path"; then
      # Delete worktree and branch
      (cd "$TMP_WORKTREE" && git worktree remove "$worktree_path" --force 2>/dev/null)
      git branch -d "$branch" 2>/dev/null
      return 0
    else
      return 4 # has worktree, skip
    fi
  else
    # Just delete branch
    (cd "$TMP_WORKTREE" && git branch -d "$branch" 2>/dev/null)
    return 0
  fi
}

# ============================================================================
# Summary Functions
# ============================================================================

show_summary() {
  echo ""
  echo "📊 Summary:"
  echo ""

  # Show branches that became merged after FF/rebase
  if [[ ${#SUMMARY_MERGED_BRANCHES[@]} -gt 0 ]]; then
    echo "🔄 Branches that became merged (could be deleted):"
    for branch in "${SUMMARY_MERGED_BRANCHES[@]}"; do
      echo "  • $branch"
    done
    echo ""
  fi

  # Show deleted branches
  if [[ ${#SUMMARY_DELETED_BRANCHES[@]} -gt 0 ]]; then
    echo "🗑️  Deleted branches:"
    for branch in "${SUMMARY_DELETED_BRANCHES[@]}"; do
      echo "  • $branch"
    done
    echo ""
  fi

  # Show deleted worktrees
  if [[ ${#SUMMARY_DELETED_WORKTREES[@]} -gt 0 ]]; then
    echo "🗑️  Deleted worktrees:"
    for worktree in "${SUMMARY_DELETED_WORKTREES[@]}"; do
      echo "  • $worktree"
    done
    echo ""
  fi

  # Show counts
  local merged_count=${#SUMMARY_MERGED_BRANCHES[@]}
  local deleted_count=${#SUMMARY_DELETED_BRANCHES[@]}
  local worktree_count=${#SUMMARY_DELETED_WORKTREES[@]}

  if [[ $merged_count -gt 0 || $deleted_count -gt 0 || $worktree_count -gt 0 ]]; then
    echo "📈 Totals:"
    if [[ $merged_count -gt 0 ]]; then
      echo "  • $merged_count branch(es) became merged"
    fi
    if [[ $deleted_count -gt 0 ]]; then
      echo "  • $deleted_count branch(es) deleted"
    fi
    if [[ $worktree_count -gt 0 ]]; then
      echo "  • $worktree_count worktree(s) deleted"
    fi
  else
    echo "ℹ️  No branches became merged or were deleted"
  fi
}

# ============================================================================
# Parse Arguments
# ============================================================================

while [[ $# -gt 0 ]]; do
  case $1 in
  --ff)
    DO_FF=true
    ;;
  --rebase)
    DO_REBASE=true
    ;;
  --delete-merged)
    DO_DELETE_MERGED=true
    ;;
  --delete-merged-worktrees)
    DELETE_MERGED_WORKTREES=true
    ;;
  --protect-branch)
    if [[ -z ${2:-} || ${2:-} == --* ]]; then
      echo "Error: --protect-branch requires a branch name argument" >&2
      exit 1
    fi
    PROTECTED_BRANCHES+=("$2")
    shift
    ;;
  --protect-worktree)
    if [[ -z ${2:-} || ${2:-} == --* ]]; then
      echo "Error: --protect-worktree requires a path argument" >&2
      exit 1
    fi
    PROTECTED_WORKTREES+=("$2")
    shift
    ;;
  --dry-run)
    DRY_RUN=true
    ;;
  --force)
    FORCE=true
    ;;
  -h | --help)
    show_help
    exit 0
    ;;
  -v | --version)
    show_version
    exit 0
    ;;
  --*)
    echo "Unknown option: $1" >&2
    echo "Use --help for usage information" >&2
    exit 1
    ;;
  *)
    # Not an option, treat as branch name
    SPECIFIC_BRANCHES+=("$1")
    ;;
  esac
  shift
done

# ============================================================================
# Configuration Initialization (after flag parsing)
# ============================================================================

# Initialize protected branches from git config (multi-value)
mapfile -t PROTECTED_BRANCHES_FROM_CONFIG < <(git config --get-all git-branch-maintenance.protectedBranch 2>/dev/null || true)

# Merge config branches with command-line specified branches
if [[ ${#PROTECTED_BRANCHES[@]} -eq 0 ]]; then
  # No command-line branches specified, use config or defaults
  if [[ ${#PROTECTED_BRANCHES_FROM_CONFIG[@]} -gt 0 ]]; then
    PROTECTED_BRANCHES=("${PROTECTED_BRANCHES_FROM_CONFIG[@]}")
  else
    PROTECTED_BRANCHES=("main" "develop")
  fi
else
  # Command-line branches specified, merge with config
  PROTECTED_BRANCHES+=("${PROTECTED_BRANCHES_FROM_CONFIG[@]}")
fi

# Initialize protected worktrees from git config (multi-value) if not specified on command line
if [[ ${#PROTECTED_WORKTREES[@]} -eq 0 ]]; then
  mapfile -t PROTECTED_WORKTREES < <(git config --get-all git-branch-maintenance.protectedWorktree 2>/dev/null || true)
fi

# Base branch for operations
BASE_BRANCH="origin/main"

# Temporary worktree settings
TMP_WORKTREE="/tmp/git-branch-maintenance-$$"
TMP_BRANCH="tmp-gbm"

# Summary tracking
SUMMARY_MERGED_BRANCHES=()
SUMMARY_DELETED_BRANCHES=()
SUMMARY_DELETED_WORKTREES=()

# ============================================================================
# Main Execution
# ============================================================================

# Validate working directory is clean (if doing operations that modify repo)
if [[ $DO_FF == "true" || $DO_REBASE == "true" || $DO_DELETE_MERGED == "true" ]]; then
  validate_clean_working_dir
fi

# Fetch all remotes
echo "Fetching all remotes..."
git fetch --all --prune

# Create temporary worktree
echo "Creating temporary worktree..."
create_tmp_worktree

echo ""
echo "Processing branches..."
echo ""

# Get branches to process
if [[ ${#SPECIFIC_BRANCHES[@]} -gt 0 ]]; then
  # Process only specified branches
  branches_to_process=("${SPECIFIC_BRANCHES[@]}")
else
  # Process all local branches
  branches_to_process=()
  while read -r branch; do
    branches_to_process+=("$branch")
  done < <(git for-each-ref --format='%(refname:short)' refs/heads)
fi

# Process each branch
for branch in "${branches_to_process[@]}"; do
  # Skip tmp-gbm itself
  [[ $branch == "$TMP_BRANCH" ]] && continue

  echo "Branch: $branch"

  worktree_path=$(get_branch_worktree "$branch")

  # Check if worktree is clean before attempting any operations
  if ! validate_worktree_clean "$worktree_path"; then
    status="skipped-dirty-worktree"
    print_branch_status "$branch" "$status"
    echo ""
    continue
  fi

  status=""

  # Try FF
  if [[ $DO_FF == "true" ]]; then
    set +e
    try_ff "$branch" "$worktree_path"
    ff_result=$?
    set -e
    case $ff_result in
    0)
      # Check if it was actually up-to-date or fast-forwarded
      branch_hash=$(git rev-parse "$branch" 2>/dev/null)
      base_hash=$(git rev-parse "$BASE_BRANCH" 2>/dev/null)
      if [[ $branch_hash == "$base_hash" ]]; then
        status="up-to-date"
      else
        status="fast-forwarded"
      fi
      ;;
    1) status="diverged" ;;
    esac
  fi

  # Check if branch is now merged after FF/rebase operations
  if [[ $status == "fast-forwarded" || $status == "rebased" ]]; then
    if git branch --merged "$BASE_BRANCH" | grep -q "^[* ] $branch$"; then
      SUMMARY_MERGED_BRANCHES+=("$branch")
    fi
  fi

  # Try rebase if requested and not already up-to-date
  if [[ $DO_REBASE == "true" && $status != "fast-forwarded" && $status != "up-to-date" ]]; then
    set +e
    try_rebase "$branch" "$worktree_path"
    rebase_result=$?
    set -e
    if [[ $rebase_result -eq 0 ]]; then
      status="rebased"
      # Check if branch is now merged after rebase
      if git branch --merged "$BASE_BRANCH" | grep -q "^[* ] $branch$"; then
        SUMMARY_MERGED_BRANCHES+=("$branch")
      fi
    else
      status="rebase-failed"
    fi
  fi

  # Try delete
  if [[ $DO_DELETE_MERGED == "true" ]]; then
    set +e
    try_delete_merged "$branch" "$worktree_path"
    delete_result=$?
    set -e
    case $delete_result in
    0)
      SUMMARY_DELETED_BRANCHES+=("$branch")
      if [[ -n $worktree_path ]]; then
        status="deleted-with-worktree"
        SUMMARY_DELETED_WORKTREES+=("$worktree_path")
      else
        status="deleted"
      fi
      ;;
    2) status="protected-from-deletion" ;;
    3) status="not-merged" ;;
    4) status="has-worktree" ;;
    esac
  fi

  # If no operations specified or status still empty, show current state
  if [[ -z $status ]]; then
    status=$(get_branch_status "$branch")
  fi

  # Print status
  print_branch_status "$branch" "$status"
  echo ""
done

# Show summary
show_summary

echo "✅ Done!"
