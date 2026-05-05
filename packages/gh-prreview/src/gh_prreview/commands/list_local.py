"""List local command for gh-prreview."""

from __future__ import annotations

from rich.console import Console
from rich.table import Table

from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.models.worktree import Worktree
from gh_prreview.protocols import GitServiceProtocol


class ListLocalCommand:
    """List all locally checked out PR worktrees."""

    def __init__(
        self,
        git_service: GitServiceProtocol,
        config: GhPrreviewConfig,
        console: Console,
    ):
        self.git = git_service
        self.config = config
        self.console = console

    def execute(self) -> int:
        """Execute the list-local command.

        Returns:
            Exit code (0 for success)
        """
        review_path = self.config.review_path

        if not review_path.exists():
            self.console.print("[dim]No review worktrees found (review path doesn't exist).[/dim]")
            return 0

        # Find all PR worktrees
        worktrees: list[Worktree] = []
        for item in review_path.iterdir():
            if item.is_dir() and item.name.startswith("pr-"):
                worktree = self.git.get_worktree_info(item)
                if worktree:
                    worktrees.append(worktree)

        if not worktrees:
            self.console.print("[dim]No review worktrees found.[/dim]")
            return 0

        # Sort by PR number
        worktrees.sort(key=lambda w: w.pr_number)

        # Display as table
        table = Table(title="Local PR Worktrees", show_header=True, header_style="bold cyan")
        table.add_column("PR", style="cyan", width=6, justify="right")
        table.add_column("Branch", style="yellow", width=30)
        table.add_column("Path", style="white")
        table.add_column("Status", width=20)

        for wt in worktrees:
            status_parts = []
            if wt.has_uncommitted_changes:
                status_parts.append("[yellow]uncommitted[/yellow]")
            if wt.unpushed_commit_count > 0:
                status_parts.append(f"[blue]+{wt.unpushed_commit_count} commits[/blue]")

            status = " ".join(status_parts) if status_parts else "[dim]clean[/dim]"

            table.add_row(
                str(wt.pr_number),
                wt.branch_name,
                str(wt.path),
                status,
            )

        self.console.print(table)
        return 0
