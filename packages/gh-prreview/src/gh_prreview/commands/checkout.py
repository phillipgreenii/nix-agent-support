"""Checkout command for gh-prreview."""

from __future__ import annotations

from rich.console import Console

from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.protocols import GitHubServiceProtocol, GitServiceProtocol


class CheckoutCommand:
    """Checkout a PR as a git worktree for review."""

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

    async def execute(self, owner: str, repo: str, pr_number: int) -> int:
        """Execute the checkout command.

        Args:
            owner: Repository owner
            repo: Repository name
            pr_number: PR number to checkout

        Returns:
            Exit code (0 for success, 1 for error)
        """
        try:
            # Fetch PR details
            with self.console.status(f"[cyan]Fetching PR #{pr_number}..."):
                pr = await self.github.get_pr(owner, repo, pr_number)

            self.console.print(f"[green]✓[/green] Found PR: {pr.title}")

            # Determine worktree path
            worktree_path = self.config.review_path / f"pr-{pr_number}"

            if worktree_path.exists():
                self.console.print(f"[yellow]![/yellow] Worktree already exists at {worktree_path}")
                return 0

            # Ensure review path exists
            self.config.review_path.mkdir(parents=True, exist_ok=True)

            # Fetch the PR
            with self.console.status(f"[cyan]Fetching PR #{pr_number} from GitHub..."):
                self.git.fetch_pr(pr_number, owner, repo)

            # Create worktree
            branch_name = f"review/pr-{pr_number}"
            start_point = f"origin/pr/{pr_number}"

            with self.console.status(f"[cyan]Creating worktree at {worktree_path}..."):
                self.git.create_worktree(worktree_path, branch_name, start_point)

            self.console.print(f"[green]✓[/green] Created worktree at {worktree_path}")
            self.console.print(f"\n[dim]cd {worktree_path}[/dim]")

            return 0

        except Exception as e:
            self.console.print(f"[red]Error:[/red] {e}")
            if self.config.debug:
                import traceback

                self.console.print(traceback.format_exc())
            return 1
