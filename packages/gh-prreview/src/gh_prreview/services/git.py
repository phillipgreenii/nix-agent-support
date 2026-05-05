"""Git service for gh-prreview."""

from __future__ import annotations

import re
import subprocess
from pathlib import Path

from gh_prreview.models.worktree import Worktree


class GitService:
    """Service for git operations."""

    def get_repo_from_remote(self, path: Path | None = None) -> tuple[str, str] | None:
        """Extract owner/repo from git remote URL.

        Returns:
            Tuple of (owner, repo) or None if not a git repo
        """
        if path is None:
            path = Path.cwd()

        try:
            result = subprocess.run(
                ["git", "-C", str(path), "config", "--get", "remote.origin.url"],
                capture_output=True,
                text=True,
                check=True,
            )
            remote_url = result.stdout.strip()

            # Parse GitHub URL (both HTTPS and SSH formats)
            # https://github.com/owner/repo.git
            # git@github.com:owner/repo.git
            patterns = [
                r"github\.com[:/]([^/]+)/(.+?)(?:\.git)?$",
            ]

            for pattern in patterns:
                match = re.search(pattern, remote_url)
                if match:
                    owner = match.group(1)
                    repo = match.group(2)
                    return (owner, repo)

            return None
        except subprocess.CalledProcessError:
            return None

    def create_worktree(
        self, target_path: Path, branch: str, start_point: str | None = None
    ) -> None:
        """Create a new git worktree.

        Args:
            target_path: Path where worktree should be created
            branch: Name of branch to create in worktree
            start_point: Optional starting point (commit/branch) for the worktree
        """
        cmd = ["git", "worktree", "add", str(target_path), "-b", branch]
        if start_point:
            cmd.append(start_point)

        subprocess.run(cmd, check=True, capture_output=True)

    def remove_worktree(self, path: Path, force: bool = False) -> None:
        """Remove a git worktree.

        Args:
            path: Path to the worktree to remove
            force: Whether to force removal even with uncommitted changes
        """
        cmd = ["git", "worktree", "remove", str(path)]
        if force:
            cmd.append("--force")

        subprocess.run(cmd, check=True, capture_output=True)

    def prune_worktrees(self) -> None:
        """Prune stale worktree administrative files."""
        subprocess.run(["git", "worktree", "prune"], check=True, capture_output=True)

    def list_worktrees(self) -> list[dict[str, str]]:
        """List all worktrees.

        Returns:
            List of dicts with 'worktree', 'HEAD', 'branch' keys
        """
        result = subprocess.run(
            ["git", "worktree", "list", "--porcelain"], capture_output=True, text=True, check=True
        )

        worktrees = []
        current: dict[str, str] = {}

        for line in result.stdout.splitlines():
            if not line:
                if current:
                    worktrees.append(current)
                    current = {}
                continue

            parts = line.split(" ", 1)
            if len(parts) == 2:
                key, value = parts
                current[key] = value

        if current:
            worktrees.append(current)

        return worktrees

    def get_worktree_info(self, path: Path) -> Worktree | None:
        """Get information about a worktree.

        Args:
            path: Path to the worktree

        Returns:
            Worktree object or None if path is not a worktree
        """
        if not path.exists():
            return None

        # Extract PR number from path (assumes format: review_path/pr-{number})
        pr_match = re.search(r"pr-(\d+)$", str(path))
        if not pr_match:
            return None

        pr_number = int(pr_match.group(1))

        # Get branch name
        try:
            result = subprocess.run(
                ["git", "-C", str(path), "rev-parse", "--abbrev-ref", "HEAD"],
                capture_output=True,
                text=True,
                check=True,
            )
            branch_name = result.stdout.strip()
        except subprocess.CalledProcessError:
            return None

        has_changes = self.has_uncommitted_changes(path)
        unpushed_count = self.count_unpushed_commits(path)

        return Worktree(
            pr_number=pr_number,
            path=path,
            branch_name=branch_name,
            has_uncommitted_changes=has_changes,
            unpushed_commit_count=unpushed_count,
        )

    def has_uncommitted_changes(self, path: Path) -> bool:
        """Check if worktree has uncommitted changes.

        Args:
            path: Path to the worktree

        Returns:
            True if there are uncommitted changes
        """
        try:
            result = subprocess.run(
                ["git", "-C", str(path), "status", "--porcelain"],
                capture_output=True,
                text=True,
                check=True,
            )
            return bool(result.stdout.strip())
        except subprocess.CalledProcessError:
            return False

    def count_unpushed_commits(self, path: Path) -> int:
        """Count commits not pushed to upstream.

        Args:
            path: Path to the worktree

        Returns:
            Number of unpushed commits (0 if no upstream)
        """
        try:
            # Check if upstream exists
            subprocess.run(
                ["git", "-C", str(path), "rev-parse", "@{u}"],
                capture_output=True,
                check=True,
            )

            # Count commits ahead of upstream
            result = subprocess.run(
                ["git", "-C", str(path), "rev-list", "--count", "@{u}..HEAD"],
                capture_output=True,
                text=True,
                check=True,
            )
            return int(result.stdout.strip())
        except subprocess.CalledProcessError:
            # No upstream or other error
            return 0

    def fetch_pr(self, pr_number: int, owner: str, repo: str) -> None:
        """Fetch a PR from GitHub.

        Args:
            pr_number: PR number to fetch
            owner: Repository owner
            repo: Repository name
        """
        subprocess.run(
            [
                "git",
                "fetch",
                "origin",
                f"pull/{pr_number}/head:refs/remotes/origin/pr/{pr_number}",
            ],
            check=True,
            capture_output=True,
        )

    def delete_branch(self, branch: str, force: bool = False) -> None:
        """Delete a local branch.

        Args:
            branch: Branch name to delete
            force: Whether to force deletion
        """
        cmd = ["git", "branch", "-D" if force else "-d", branch]
        subprocess.run(cmd, check=True, capture_output=True)
