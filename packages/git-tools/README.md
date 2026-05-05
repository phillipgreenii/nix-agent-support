# Git Tools

A collection of Git utility scripts for branch management and workflow automation.

## Tools

### git-branch-maintenance

Maintain Git branches through fast-forwarding, rebasing, and cleaning up merged branches.

#### Features

- **Fast-forward** branches to stay up-to-date with main
- **Rebase** branches onto main
- **Delete merged branches** with optional worktree cleanup
- **Protected branches and worktrees** via git config or command-line flags
- **Dry-run mode** to preview changes
- **Per-repository configuration** using git config

#### Basic Usage

```bash
# Show status of all branches
git-branch-maintenance

# Show status of specific branches only
git-branch-maintenance feature-1 feature-2

# Fast-forward all branches
git-branch-maintenance --ff

# Rebase all branches onto origin/main
git-branch-maintenance --rebase

# Delete branches that are merged into main
git-branch-maintenance --delete-merged

# Delete merged branches AND their worktrees
git-branch-maintenance --delete-merged --delete-merged-worktrees

# Preview changes without making them
git-branch-maintenance --ff --rebase --delete-merged --dry-run
```

#### Configuration

You can configure protected branches and worktrees on a per-repository basis using git config:

##### Protected Branches

By default, `main` and `develop` branches are protected from deletion. You can customize this per repository:

```bash
# Add a protected branch (can add multiple)
git config --local --add git-branch-maintenance.protectedBranch "staging"
git config --local --add git-branch-maintenance.protectedBranch "production"

# View current protected branches
git config --get-all git-branch-maintenance.protectedBranch

# Remove a protected branch
git config --unset git-branch-maintenance.protectedBranch "staging"

# Remove all protected branches (will fall back to defaults: main, develop)
git config --unset-all git-branch-maintenance.protectedBranch
```

##### Protected Worktrees

Protect specific worktrees from deletion:

```bash
# Add a protected worktree path (can add multiple)
git config --local --add git-branch-maintenance.protectedWorktree "/Users/you/worktrees/stable"
git config --local --add git-branch-maintenance.protectedWorktree "/tmp/important-worktree"

# View current protected worktrees
git config --get-all git-branch-maintenance.protectedWorktree

# Remove a protected worktree
git config --unset git-branch-maintenance.protectedWorktree "/tmp/important-worktree"
```

##### Command-Line Overrides

You can add additional protections for a specific run without modifying git config:

```bash
# Protect a branch for this run only
git-branch-maintenance --delete-merged --protect-branch temp-feature

# Protect a worktree for this run only
git-branch-maintenance --delete-merged --delete-merged-worktrees \
  --protect-worktree /tmp/special-worktree

# Combine git config and CLI flags (both apply)
# If git config has "staging" protected and you add CLI flag for "demo"
# then both "staging" and "demo" will be protected
git-branch-maintenance --delete-merged --protect-branch demo
```

#### Examples

**Multi-repository workflow:**

```bash
# Repository A: Protect staging branch
cd ~/projects/repo-a
git config --local --add git-branch-maintenance.protectedBranch "staging"

# Repository B: Protect production branch
cd ~/projects/repo-b
git config --local --add git-branch-maintenance.protectedBranch "production"

# Each repository maintains its own protected branches
# The script automatically reads the correct config per repository
```

**One-time worktree protection:**

```bash
# You have a temporary worktree for a presentation
git worktree add /tmp/demo-worktree feature-branch

# Later, clean up merged branches but keep the demo worktree
git-branch-maintenance --delete-merged --delete-merged-worktrees \
  --protect-worktree /tmp/demo-worktree
```

**Complete maintenance workflow:**

```bash
# Update all branches, rebase onto main, and clean up merged branches
git-branch-maintenance --ff --rebase --delete-merged

# Preview the same operation first
git-branch-maintenance --ff --rebase --delete-merged --dry-run
```

#### Options Reference

**Operations:**

- `--ff` - Fast-forward branches to origin/main
- `--rebase` - Rebase branches onto origin/main
- `--delete-merged` - Delete branches merged into main
- `--delete-merged-worktrees` - Allow deletion of worktrees (use with --delete-merged)

**Protection:**

- `--protect-branch <branch>` - Add a protected branch for this run
- `--protect-worktree <path>` - Add a protected worktree for this run

**Other:**

- `--dry-run` - Show what would happen without making changes
- `--force` - Skip working directory validation
- `--help` - Show detailed help message

### git-branch-status

Display the status of Git branches relative to the main branch.

```bash
# Show status of all branches
git-branch-status

# Show status of specific branches
git-branch-status feature-1 feature-2
```

### git-choose-branch

Interactive branch selector using fzf.

```bash
# Choose a branch interactively
git-choose-branch

# Use in scripts
branch=$(git-choose-branch)
git checkout "$branch"
```

## Installation

This package is part of the phillipg-nix-support-apps flake. Install by enabling in your home-manager configuration:

```nix
programs.git-tools.enable = true;
```

Or build directly:

```bash
nix build .#git-tools
```

## Testing

Run the test suite:

```bash
# Run all tests
bats tests/

# Run specific test file
bats tests/test-git-branch-maintenance.bats
```

## Development

The tools are implemented as bash scripts with:

- Shellcheck validation
- BATS test suite
- Version information embedded during build
- Nix-based packaging with runtime dependencies
