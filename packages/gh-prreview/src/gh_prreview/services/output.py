"""Output formatting service for gh-prreview."""

from __future__ import annotations

from datetime import UTC, datetime

from rich.console import Console
from rich.table import Table

from gh_prreview.models.pr import PullRequest, ReviewStatus


def format_relative_time(dt: datetime) -> str:
    """Format a datetime as succinct relative time (e.g., '2d', '17m').

    Args:
        dt: Datetime to format (should be timezone-aware)

    Returns:
        Formatted relative time string (short form)
    """
    now = datetime.now(UTC)
    diff = now - dt

    seconds = diff.total_seconds()
    minutes = seconds / 60
    hours = minutes / 60
    days = hours / 24

    if days >= 365:
        years = int(days / 365)
        return f"{years}y"
    elif days >= 30:
        months = int(days / 30)
        return f"{months}mo"
    elif days >= 1:
        return f"{int(days)}d"
    elif hours >= 1:
        return f"{int(hours)}h"
    elif minutes >= 1:
        return f"{int(minutes)}m"
    else:
        return "now"


def create_pr_table(
    title: str = "Pull Requests",
    show_status: bool = True,
    show_age: bool = False,
) -> Table:
    """Create a Rich table for displaying PRs.

    Args:
        title: Table title
        show_status: Whether to include status column
        show_age: Whether to include age column

    Returns:
        Configured Rich Table
    """
    table = Table(title=title, show_header=True, header_style="bold cyan")

    table.add_column("PR", style="cyan", width=6, justify="right", no_wrap=True)
    table.add_column("Title", style="white")  # Wraps naturally
    table.add_column("Author", style="yellow", width=20, no_wrap=True)

    if show_status:
        table.add_column("Status", width=12, no_wrap=True)  # STALE_REVIEW is 12 chars

    if show_age:
        table.add_column("Updated", style="dim", width=5, no_wrap=True, justify="right")

    return table


def add_pr_row(
    table: Table,
    pr: PullRequest,
    status: ReviewStatus | None = None,
    show_age: bool = False,
) -> None:
    """Add a PR row to a table.

    Args:
        table: Rich Table to add row to
        pr: PullRequest to display
        status: Optional ReviewStatus for status column
        show_age: Whether to include age column
    """
    # Create clickable hyperlink for PR number using OSC 8
    pr_link = f"[link={pr.url}]{pr.number}[/link]"

    # Title wraps naturally, no truncation needed
    row = [pr_link, pr.title, pr.author]

    if status is not None:
        status_style = {
            ReviewStatus.NEW: "green",
            ReviewStatus.STALE_REVIEW: "yellow",
            ReviewStatus.PENDING_REVIEW: "blue",
        }.get(status, "white")
        row.append(f"[{status_style}]{status.value}[/{status_style}]")

    if show_age:
        age = format_relative_time(pr.updated_at)
        row.append(age)

    table.add_row(*row)


def print_pr_table(
    console: Console,
    prs: list[tuple[PullRequest, ReviewStatus]] | list[PullRequest],
    title: str = "Pull Requests",
    show_status: bool = True,
    show_age: bool = False,
    empty_message: str = "No PRs found.",
) -> None:
    """Print a table of PRs to the console.

    Args:
        console: Rich Console to print to
        prs: List of PRs (with optional ReviewStatus tuples)
        title: Table title
        show_status: Whether to show status column
        show_age: Whether to show age column
        empty_message: Message to show if no PRs
    """
    if not prs:
        console.print(f"[dim]{empty_message}[/dim]")
        return

    table = create_pr_table(title=title, show_status=show_status, show_age=show_age)

    for item in prs:
        if isinstance(item, tuple):
            pr, status = item
            add_pr_row(table, pr, status=status, show_age=show_age)
        else:
            add_pr_row(table, item, show_age=show_age)

    console.print(table)
