"""CLI entry point for gh-prreview."""

import asyncio
import sys

import click
from rich.console import Console

from gh_prreview import __version__
from gh_prreview.commands.checkout import CheckoutCommand
from gh_prreview.commands.list_awaiting import ListAwaitingCommand
from gh_prreview.commands.list_local import ListLocalCommand
from gh_prreview.commands.remove import RemoveCommand
from gh_prreview.models.config import ConfigurationError
from gh_prreview.services.config import ConfigService
from gh_prreview.services.git import GitService
from gh_prreview.services.github import GitHubService

console = Console()


@click.group()
@click.version_option(version=__version__, prog_name="gh-prreview")
@click.option("--debug", is_flag=True, help="Enable debug output")
@click.pass_context
def cli(ctx: click.Context, debug: bool) -> None:
    """GitHub Pull Request Review Extension for gh CLI.

    Manage PR worktrees for code review.

    \b
    Required Configuration:
      GH_PRREVIEW_REVIEW_PATH    Path for PR review worktrees

    \b
    Optional Configuration:
      GH_PRREVIEW_WATCH_LABELS   Comma-separated labels to watch
      GH_PRREVIEW_WATCH_USERS    Comma-separated usernames to watch
      GH_PRREVIEW_OLD_PR_DAYS    Days before PR is considered stale (default: 30)

    Configuration can also be set via: gh config set prreview.<key> <value>
    """
    ctx.ensure_object(dict)

    try:
        config_service = ConfigService()
        config = config_service.load_config()
        if debug:
            from dataclasses import replace

            config = replace(config, debug=True)

        ctx.obj["config"] = config
        ctx.obj["console"] = console
        ctx.obj["git"] = GitService()

    except ConfigurationError as e:
        console.print(f"[red]Configuration Error:[/red]\n{e}")
        sys.exit(1)


@cli.command()
@click.argument("pr_id", type=int)
@click.pass_context
def checkout(ctx: click.Context, pr_id: int) -> None:
    """Checkout a PR as a git worktree for review."""

    async def _run() -> int:
        # Get repo info from git
        git_service: GitService = ctx.obj["git"]
        repo_info = git_service.get_repo_from_remote()

        if not repo_info:
            console.print("[red]Error:[/red] Not in a git repository or no GitHub remote found")
            return 1

        owner, repo = repo_info

        async with GitHubService() as github:
            command = CheckoutCommand(
                github_service=github,
                git_service=git_service,
                config=ctx.obj["config"],
                console=ctx.obj["console"],
            )
            return await command.execute(owner, repo, pr_id)

    exit_code = asyncio.run(_run())
    sys.exit(exit_code)


@cli.command("list-awaiting")
@click.option("--deep", is_flag=True, help="Search older PRs (limit 500)")
@click.option("--include-draft", is_flag=True, help="Include draft PRs")
@click.pass_context
def list_awaiting(ctx: click.Context, deep: bool, include_draft: bool) -> None:
    """List PRs awaiting your review."""

    async def _run() -> int:
        # Get repo info from git
        git_service: GitService = ctx.obj["git"]
        repo_info = git_service.get_repo_from_remote()

        if not repo_info:
            console.print("[red]Error:[/red] Not in a git repository or no GitHub remote found")
            return 1

        owner, repo = repo_info

        async with GitHubService() as github:
            command = ListAwaitingCommand(
                github_service=github,
                config=ctx.obj["config"],
                console=ctx.obj["console"],
            )
            return await command.execute(owner, repo, deep_search=deep, include_draft=include_draft)

    exit_code = asyncio.run(_run())
    sys.exit(exit_code)


@cli.command("list-local")
@click.pass_context
def list_local(ctx: click.Context) -> None:
    """List all locally checked out PR worktrees."""
    command = ListLocalCommand(
        git_service=ctx.obj["git"],
        config=ctx.obj["config"],
        console=ctx.obj["console"],
    )
    exit_code = command.execute()
    sys.exit(exit_code)


@cli.command()
@click.argument("pr_ids", type=int, nargs=-1)
@click.option("--closed", is_flag=True, help="Remove all closed/merged PRs")
@click.option("--force", "-f", is_flag=True, help="Force removal even with uncommitted changes")
@click.pass_context
def remove(ctx: click.Context, pr_ids: tuple[int, ...], closed: bool, force: bool) -> None:
    """Remove PR review worktrees and their local branches.

    \b
    Examples:
      gh prreview remove 123           # Remove PR #123
      gh prreview remove 123 456 789   # Remove multiple PRs
      gh prreview remove --closed      # Remove all closed/merged PRs
    """
    if not pr_ids and not closed:
        console.print("[red]Error:[/red] Specify PR numbers or use --closed")
        sys.exit(1)

    async def _run() -> int:
        # Get repo info from git
        git_service: GitService = ctx.obj["git"]
        repo_info = git_service.get_repo_from_remote()

        if not repo_info:
            console.print("[red]Error:[/red] Not in a git repository or no GitHub remote found")
            return 1

        owner, repo = repo_info

        async with GitHubService() as github:
            command = RemoveCommand(
                github_service=github,
                git_service=git_service,
                config=ctx.obj["config"],
                console=ctx.obj["console"],
            )
            return await command.execute(
                owner, repo, list(pr_ids), remove_closed=closed, force=force
            )

    exit_code = asyncio.run(_run())
    sys.exit(exit_code)


def main() -> None:
    """Entry point for the CLI."""
    cli()


if __name__ == "__main__":
    main()
