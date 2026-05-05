"""Tests for remove command."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import AsyncMock, Mock

import pytest
from rich.console import Console

from gh_prreview.commands.remove import RemoveCommand
from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.models.pr import PullRequest
from gh_prreview.models.worktree import Worktree


@pytest.fixture
def mock_github_service():
    """Mock GitHub service."""
    return AsyncMock()


@pytest.fixture
def mock_git_service():
    """Mock Git service."""
    return Mock()


@pytest.fixture
def config(tmp_path):
    """Test configuration."""
    return GhPrreviewConfig(
        review_path=tmp_path / "reviews",
        watch_labels=["team/test"],
        watch_users=["testuser"],
        old_pr_days=30,
        debug=False,
    )


@pytest.fixture
def console():
    """Mock console."""
    return Console(force_terminal=False, force_interactive=False)


@pytest.mark.asyncio
async def test_remove_no_review_path(mock_github_service, mock_git_service, config, console):
    """Test remove when review path doesn't exist."""
    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [12345])

    assert exit_code == 0
    mock_git_service.remove_worktree.assert_not_called()


@pytest.mark.asyncio
async def test_remove_specific_pr(mock_github_service, mock_git_service, config, console):
    """Test removing a specific PR."""
    config.review_path.mkdir(parents=True)
    worktree_path = config.review_path / "pr-12345"
    worktree_path.mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=worktree_path,
        branch_name="review/pr-12345",
        has_uncommitted_changes=False,
        unpushed_commit_count=0,
    )

    mock_git_service.get_worktree_info.return_value = worktree

    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [12345])

    assert exit_code == 0
    mock_git_service.remove_worktree.assert_called_once_with(worktree_path, force=False)
    mock_git_service.delete_branch.assert_called_once_with("review/pr-12345", force=False)
    mock_git_service.prune_worktrees.assert_called_once()


@pytest.mark.asyncio
async def test_remove_nonexistent_pr(mock_github_service, mock_git_service, config, console):
    """Test removing a PR that doesn't exist."""
    config.review_path.mkdir(parents=True)

    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [12345])

    assert exit_code == 0
    mock_git_service.remove_worktree.assert_not_called()


@pytest.mark.asyncio
async def test_remove_with_uncommitted_changes_no_force(
    mock_github_service, mock_git_service, config, console
):
    """Test removing PR with uncommitted changes without force flag."""
    config.review_path.mkdir(parents=True)
    worktree_path = config.review_path / "pr-12345"
    worktree_path.mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=worktree_path,
        branch_name="review/pr-12345",
        has_uncommitted_changes=True,
        unpushed_commit_count=0,
    )

    mock_git_service.get_worktree_info.return_value = worktree

    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [12345], force=False)

    assert exit_code == 1
    mock_git_service.remove_worktree.assert_not_called()


@pytest.mark.asyncio
async def test_remove_with_uncommitted_changes_with_force(
    mock_github_service, mock_git_service, config, console
):
    """Test removing PR with uncommitted changes with force flag."""
    config.review_path.mkdir(parents=True)
    worktree_path = config.review_path / "pr-12345"
    worktree_path.mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=worktree_path,
        branch_name="review/pr-12345",
        has_uncommitted_changes=True,
        unpushed_commit_count=0,
    )

    mock_git_service.get_worktree_info.return_value = worktree

    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [12345], force=True)

    assert exit_code == 0
    mock_git_service.remove_worktree.assert_called_once_with(worktree_path, force=True)
    mock_git_service.delete_branch.assert_called_once_with("review/pr-12345", force=True)


@pytest.mark.asyncio
async def test_remove_multiple_prs(mock_github_service, mock_git_service, config, console):
    """Test removing multiple PRs."""
    config.review_path.mkdir(parents=True)

    worktrees = []
    for pr_num in [12345, 67890]:
        worktree_path = config.review_path / f"pr-{pr_num}"
        worktree_path.mkdir()
        worktrees.append(
            Worktree(
                pr_number=pr_num,
                path=worktree_path,
                branch_name=f"review/pr-{pr_num}",
                has_uncommitted_changes=False,
                unpushed_commit_count=0,
            )
        )

    def get_worktree_info(path: Path):
        for wt in worktrees:
            if wt.path == path:
                return wt
        return None

    mock_git_service.get_worktree_info.side_effect = get_worktree_info

    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [12345, 67890])

    assert exit_code == 0
    assert mock_git_service.remove_worktree.call_count == 2
    assert mock_git_service.delete_branch.call_count == 2


@pytest.mark.asyncio
async def test_remove_closed_prs(mock_github_service, mock_git_service, config, console):
    """Test removing all closed PRs."""
    from datetime import UTC, datetime

    config.review_path.mkdir(parents=True)

    # Create two worktrees
    for pr_num in [12345, 67890]:
        (config.review_path / f"pr-{pr_num}").mkdir()

    worktree1 = Worktree(
        pr_number=12345,
        path=config.review_path / "pr-12345",
        branch_name="review/pr-12345",
        has_uncommitted_changes=False,
        unpushed_commit_count=0,
    )
    worktree2 = Worktree(
        pr_number=67890,
        path=config.review_path / "pr-67890",
        branch_name="review/pr-67890",
        has_uncommitted_changes=False,
        unpushed_commit_count=0,
    )

    def get_worktree_info(path: Path):
        if path.name == "pr-12345":
            return worktree1
        elif path.name == "pr-67890":
            return worktree2
        return None

    mock_git_service.get_worktree_info.side_effect = get_worktree_info

    # Mock PR states - one closed, one open
    async def get_pr(owner, repo, number):
        now = datetime.now(UTC)
        if number == 12345:
            return PullRequest(
                number=12345,
                title="Closed PR",
                author="author",
                is_draft=False,
                created_at=now,
                updated_at=now,
                head_ref_oid="abc",
                reviews=[],
                labels=[],
                owner="test",
                repo="repo",
                state="CLOSED",
            )
        else:
            return PullRequest(
                number=67890,
                title="Open PR",
                author="author",
                is_draft=False,
                created_at=now,
                updated_at=now,
                head_ref_oid="def",
                reviews=[],
                labels=[],
                owner="test",
                repo="repo",
                state="OPEN",
            )

    mock_github_service.get_pr.side_effect = get_pr

    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [], remove_closed=True)

    assert exit_code == 0
    # Should only remove the closed PR
    mock_git_service.remove_worktree.assert_called_once()
    assert mock_git_service.remove_worktree.call_args[0][0] == config.review_path / "pr-12345"


@pytest.mark.asyncio
async def test_remove_worktree_error(mock_github_service, mock_git_service, config, console):
    """Test handling of worktree removal error."""
    config.review_path.mkdir(parents=True)
    worktree_path = config.review_path / "pr-12345"
    worktree_path.mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=worktree_path,
        branch_name="review/pr-12345",
        has_uncommitted_changes=False,
        unpushed_commit_count=0,
    )

    mock_git_service.get_worktree_info.return_value = worktree
    mock_git_service.remove_worktree.side_effect = Exception("Removal failed")

    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [12345])

    assert exit_code == 0  # Command continues despite error
    mock_git_service.remove_worktree.assert_called_once()


@pytest.mark.asyncio
async def test_remove_branch_deletion_fails_silently(
    mock_github_service, mock_git_service, config, console
):
    """Test that branch deletion failures are suppressed."""
    config.review_path.mkdir(parents=True)
    worktree_path = config.review_path / "pr-12345"
    worktree_path.mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=worktree_path,
        branch_name="review/pr-12345",
        has_uncommitted_changes=False,
        unpushed_commit_count=0,
    )

    mock_git_service.get_worktree_info.return_value = worktree
    mock_git_service.delete_branch.side_effect = Exception("Branch in use")

    command = RemoveCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", [12345])

    assert exit_code == 0
    mock_git_service.remove_worktree.assert_called_once()
    mock_git_service.delete_branch.assert_called_once()


@pytest.mark.asyncio
async def test_remove_debug_mode(mock_github_service, mock_git_service, tmp_path, console):
    """Test remove with debug mode enabled."""
    debug_config = GhPrreviewConfig(
        review_path=tmp_path / "reviews",
        watch_labels=["team/test"],
        watch_users=["testuser"],
        old_pr_days=30,
        debug=True,
    )
    debug_config.review_path.mkdir(parents=True)
    worktree_path = debug_config.review_path / "pr-12345"
    worktree_path.mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=worktree_path,
        branch_name="review/pr-12345",
        has_uncommitted_changes=False,
        unpushed_commit_count=0,
    )

    mock_git_service.get_worktree_info.return_value = worktree
    mock_git_service.remove_worktree.side_effect = Exception("Test error")

    command = RemoveCommand(mock_github_service, mock_git_service, debug_config, console)
    exit_code = await command.execute("owner", "repo", [12345])

    assert exit_code == 0
