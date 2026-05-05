"""Remove command for gh-prreview."""

from __future__ import annotations

import contextlib

from rich.console import Console

from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.models.worktree import Worktree
from gh_prreview.protocols import GitHubServiceProtocol, GitServiceProtocol


class RemoveCommand:
    """Remove PR review worktrees and their local branches."""

    def __init__(
        self,
        github_service: GitHubServiceProtocol,
        git_service: GitServiceProtocol,
        config: GhPrreviewConfig,
        console: Console,
    ):
        self.github = github_service
        self.git = git_service
        self.config = config
        self.console = console

    async def execute(
        self,
        owner: str,
        repo: str,
        pr_numbers: list[int],
        remove_closed: bool = False,
        force: bool = False,
    ) -> int:
        """Execute the remove command.

        Args:
            owner: Repository owner
            repo: Repository name
            pr_numbers: List of PR numbers to remove
            remove_closed: If True, remove all closed/merged PRs
            force: Force removal even with uncommitted changes

        Returns:
            Exit code (0 for success)
        """
        review_path = self.config.review_path

        if not review_path.exists():
            self.console.print("[yellow]![/yellow] Review path doesn't exist")
            return 0

        # Determine which PRs to remove
        worktrees_to_remove: list[Worktree] = []

        if remove_closed:
            # Find all worktrees and check if PR is closed
            for item in review_path.iterdir():
                if item.is_dir() and item.name.startswith("pr-"):
                    worktree = self.git.get_worktree_info(item)
                    if worktree:
                        try:
                            pr = await self.github.get_pr(owner, repo, worktree.pr_number)
                            if pr.state in ("CLOSED", "MERGED"):
                                worktrees_to_remove.append(worktree)
                        except Exception as e:
                            if self.config.debug:
                                self.console.print(
                                    f"[dim]Could not check PR #{worktree.pr_number}: {e}[/dim]"
                                )
        else:
            # Remove specific PRs
            for pr_number in pr_numbers:
                worktree_path = review_path / f"pr-{pr_number}"
                if worktree_path.exists():
                    worktree = self.git.get_worktree_info(worktree_path)
                    if worktree:
                        worktrees_to_remove.append(worktree)
                    else:
                        self.console.print(
                            f"[yellow]![/yellow] Not a valid worktree: {worktree_path}"
                        )
                else:
                    self.console.print(f"[yellow]![/yellow] Worktree doesn't exist: pr-{pr_number}")

        if not worktrees_to_remove:
            self.console.print("[dim]No worktrees to remove.[/dim]")
            return 0

        # Warn about uncommitted changes
        if not force:
            dirty_worktrees = [wt for wt in worktrees_to_remove if wt.has_uncommitted_changes]
            if dirty_worktrees:
                self.console.print(
                    "[yellow]![/yellow] The following worktrees have uncommitted changes:"
                )
                for wt in dirty_worktrees:
                    self.console.print(f"  • PR #{wt.pr_number}: {wt.path}")
                self.console.print("\nUse --force to remove anyway.")
                return 1

        # Remove worktrees
        for worktree in worktrees_to_remove:
            try:
                with self.console.status(
                    f"[cyan]Removing worktree for PR #{worktree.pr_number}..."
                ):
                    self.git.remove_worktree(worktree.path, force=force)

                # Try to delete the branch (may fail if checked out elsewhere)
                with contextlib.suppress(Exception):
                    # Branch might be checked out elsewhere or have other git issues
                    self.git.delete_branch(worktree.branch_name, force=force)

                self.console.print(f"[green]✓[/green] Removed PR #{worktree.pr_number}")

            except Exception as e:
                self.console.print(f"[red]Error removing PR #{worktree.pr_number}:[/red] {e}")
                if self.config.debug:
                    import traceback

                    self.console.print(traceback.format_exc())

        # Prune stale worktree admin files
        with contextlib.suppress(Exception):
            self.git.prune_worktrees()

        return 0
